# ADR-0007：relay→直连的升级重试改为 make-before-break，不再整段拆除会话

- **状态**：已接受（Accepted）——2026-10-03 落地，见文末「决策记录」
- **日期**：2026-09-23
- **关联**：ADR-0006（信令在中继上回退；本文复用其 `peerSignaler` 与"待确认问题 2"里埋下的 `attempt_id`）、ADR-0002（ICE candidate race fix）

## 摘要

`probe_upgrade.go` 里"relay 用久了定期重试直连"的机制，目前是整个 `Probe` 全量重启（break-before-make）：先把 WireGuard peer 整个删掉（`UAPI: Removing all allowedips` / `UAPI: Removing`），再重新走 ICE 与 relay 双轨发现。代码自己的注释也承认这一点：

```go
// The retry is a full probe restart (the signaling has no "upgrade only" flag),
// which costs a brief tunnel interruption, so attempts back off exponentially.
```

问题是：每次升级尝试，不管最后成不成功，都会先造成一次真实的连接中断。如果 ICE 长期打不通（环境限制——本机代理接管了 UDP、对称 NAT、STUN 不可达等），"尝试升级 → 中断 → 失败 → 退避后再试"这个循环会无限重复，relay 连接永远等不到一段真正稳定、不被自己打断的窗口。

本文提议把升级尝试改成 make-before-break：在不影响现有 relay 连接的前提下，后台并行试探直连，试通了再原地把 WireGuard 的 endpoint 切过去；试不通，现有连接全程不受影响。

## 背景

### 当晚复现（2026-09-23，cloud-node-1 环境）

用 CLI 连 cloud-node-1（`app-id: macbook-pro.local`），relay-ready 建立约 114 秒后（与 `upgradeBaseInterval = 2 * time.Minute` 的首次重试时间点吻合），cloud-node-1 侧日志：

```
time=...15:15:40.589Z level=DEBUG ... msg="SYN on active Relay session — remote restarted, triggering restart" mod=relay-dialer remoteId=12433780925455272978
time=...15:15:40.589Z level=DEBUG ... msg="state transition" mod=probe-factory remoteId=macbook-pro.local from=relay-ready to=failed
DEBUG: () 2026/09/23 15:15:40 peer(rI2p…Z0lc) - UAPI: Removing all allowedips
DEBUG: () 2026/09/23 15:15:40 peer(rI2p…Z0lc) - UAPI: Removing
DEBUG: () 2026/09/23 15:15:40 peer(rI2p…Z0lc) - Stopping
```

这次重启恰好落在一次 `ping 10.96.0.2` 测试期间：抓包确认测试流量确实送达了 cloud-node-1 所在容器的中继 TCP 连接（`docker0`/`veth` 层面看到了对应字节数的突发流量），但 WireGuard peer 在同一时刻被整个移除，包无处可送，ping 全部超时。这一度被误判为"FERRY 中继数据面不转发包"，回头用时间线核对才发现是升级重试拆链路造成的假象。

### 根因

1. `internal/server/transport/probe_upgrade.go` 的 `tryUpgrade()`（`upgradeBaseInterval = 2 分钟`，翻倍退避到 `upgradeMaxInterval = 30 分钟`）调用 `p.upgradeRestart()` / `p.restart()`——一次完整的 probe 重启，等价于"当作这个 peer 重新连接一次"。
2. 这次重启产生的信令（`HANDSHAKE_SYN`），在接收端（`internal/server/transport/relay_dialer.go`）眼里，和"对端进程真的重启了"没有任何区别。现有的 `relaySynGrace`（5 秒宽限期，ADR-0006 留下的时间启发式）只保护"会话刚形成时的重传"，保护不了"会话已经稳定运行了很久，这次 SYN 其实是一次后台升级探测"——一律触发 `onRestart()`，把整个 WireGuard peer 拆掉重建。
3. 当晚同时确认：测试机上的本机代理（Clash，TUN 模式接管了包括 UDP 在内的全部默认路由）导致 STUN 探测全部超时，ICE 候选收集失败，直连在这个环境下永远打不通。这意味着像 cloud-node-1 这样的场景会持续每 2 / 4 / 8 / … 分钟重复一次"尝试升级 → 中断 → 失败"的循环，永远等不到退避封顶后的安静期。

Clash 本身是本机环境问题，不是本文要解决的；但它把"升级重试的中断代价"这个已知设计缺陷的影响放大到了显性可见、可复现的程度，值得借这次一并修掉。

## 目标

1. 一次失败的升级尝试不能中断正在工作的 relay 连接——数据面（WireGuard peer/AllowedIPs 不变）和信令面（对端不能把这次探测误判为"重启"）都不能动。
2. 升级尝试成功时，WireGuard 的 endpoint 原地切换到直连地址；除了这次切换本身带来的一次路径迁移，不产生额外中断。
3. 接收端（非 initiator）能区分"这是一次后台升级探测"还是"对端真的重启了"，前者不触发 `onRestart()`。
4. 新旧版本节点互通：旧版本节点收不到新字段（或不认识）时，安全回退到现在的整段重启行为——不是本文要优化的场景，但不能让旧节点崩溃或状态错乱。

非目标：改变 relay 数据面协议本身（ADR-0005 范围）、改变"ICE 优先于 relay"的判定逻辑、解决 Clash 一类的本机网络环境问题。

## 决策

### 1. 信令包新增"这是一次升级探测"标记

`SignalPacket.Handshake`（`internal/signal/signal.go`）新增：

```go
type Handshake struct {
	Timestamp       int64  `json:"timestamp,omitempty"`
	PeerInfo        []byte `json:"peer_info,omitempty"`
	IsUpgradeProbe  bool   `json:"is_upgrade_probe,omitempty"`
	AttemptID       string `json:"attempt_id,omitempty"` // 复用 ADR-0006 留的字段
}
```

旧版本节点不认识这两个字段，JSON 反序列化时直接丢弃，行为回退到现在的"整段重启"——满足新旧互通的目标。

### 2. 升级尝试不再触碰现有 Probe/WireGuard 状态

`probe_upgrade.go` 的 `tryUpgrade()` 不再调用 `p.restart()` / `p.upgradeRestart()`。改为启动一个新增的 `shadowICEDialer`（`internal/server/transport/probe_shadow_upgrade.go`，新文件）：

- 复用 `ice_dialer.go` 的候选收集与 connectivity check 逻辑，但**不经过 `StateMachine`**，不调用 `configurator.RegisterPeer` / `RemovePeer`，只做纯粹的 ICE 协商。
- 生成一次性 `attemptID`，`peerSignaler` 发出的 SYN/OFFER/ANSWER 都带 `IsUpgradeProbe: true` + 这个 `attemptID`。
- 达到 ICE `Connected`（真正的 connectivity check 通过，不是"候选收集完成"）才算成功。
- 60 秒拿不到结果就放弃，按 `upgradeDelay(attempts+1)` 退避到下一次——不触碰现有连接，不触发 `probe.restart()`。

### 3. 升级成功后原地切换 endpoint

只调用 `configurator.SetEndpoint(pubKey, newDirectAddr, keepalive)`；不调用 `RegisterPeer`（AllowedIPs 没变，不需要重新下发）、不调用 `RemovePeer`。机制上类似现有 `probe_endpoint.go` 里"端点纠偏"（`ReassertDirectEndpoint`）已经在做的原地替换，区别是这次是主动发起而非被动纠偏。`probe.currentTransport` 原地替换为新的 ICE transport，旧的 relay transport 优雅关闭。

### 4. 接收端识别升级探测，不触发重启

`relay_dialer.go` / `ice_dialer.go` 收到 `HANDSHAKE_SYN` 时，`isActive` 分支里先看 `hs.IsUpgradeProbe`：

- **true**：不调用 `onRestart()`。正常回 ACK、走 OFFER/ANSWER 交换，但交换结果只喂给发起方对应 `attemptID` 的 `shadowICEDialer`，不触碰当前 `Probe` 的主状态机、不碰现有 WireGuard 配置。
- **false 或字段不存在**（兼容旧版本）：保持现在的行为不变——宽限期内当重传，超出宽限期触发重启。

### 5. 只有 initiator 发起升级

沿用现有的 `isInitiator(p.localId, p.remoteId)` 判断，避免两端同时探测、同时想切换造成竞态。

## 备选方案

- **保持全量重启，只是拉长 `upgradeBaseInterval` / 提高退避上限**：治标不治本。Clash 一类环境下 ICE 永远打不通，无论多久重试一次，每次都会真中断一下，只是频率变低。
- **先探测，成功了再触发一次"干净的"整段重启去完成切换**：仍然要经历一次真实中断，比 make-before-break 差，只是没有 IsUpgradeProbe 这个字段这么"干净"。
- **relay-ready 之后永远不再尝试直连**：省事，但违背"ICE 与 relay 赛跑"的设计初衷，网络条件变好之后也升级不回直连，长期占用中继资源。

## 落地步骤

1. `internal/proto/signal.proto` / `internal/signal/signal.go` 加 `IsUpgradeProbe` + `AttemptID` 字段（`AttemptID` 是 ADR-0006 留的坑，这次一起填，两者本来就要配合使用）。
2. 新增 `shadowICEDialer`（很大程度上是 `ice_dialer.go` 的瘦身版本，去掉与 `StateMachine`/`configurator` 的耦合）。
3. `probe_upgrade.go` 的 `tryUpgrade()` 改为驱动 `shadowICEDialer`，不再调用 `restart()`。
4. `relay_dialer.go` / `ice_dialer.go` 的 SYN 处理分支加 `IsUpgradeProbe` 判断与对应的接收侧响应路径。
5. 确认/复用 `configurator.SetEndpoint` 的原地切换路径不会意外触发 `RegisterPeer`（AllowedIPs 重新下发）。

## 测试计划

- **单测**：`shadowICEDialer` 失败时，现有 `Probe` 的状态机、WireGuard peer 配置全程不变（mock provisioner 断言 `RegisterPeer`/`RemovePeer` 调用次数为 0）。
- **单测**：`shadowICEDialer` 成功时，只调用一次 `SetEndpoint`，不调用 `RegisterPeer`。
- **单测**：接收端收到 `IsUpgradeProbe: true` 的 SYN，不触发 `onRestart`；收到 `false`/无该字段的 SYN，行为与现在一致（回归保护）。
- **集成**：两节点 relay-ready 后人为切断 ICE（比如让 STUN 不可达，模拟 Clash 场景），跑满几个退避周期，relay 数据面应全程不中断（ping/流量不丢包），日志里应能看到多次升级尝试失败但连接保持稳定。
- **真机回归**：cloud-node-1 这个环境直接验证——在本文落地之前，这个环境永远等不到"稳定超过 2 分钟、不撞升级窗口"的干净测试窗口，这本身就是最直接的验收标准之一。

## 待确认的问题

1. 升级失败的退避计数（`upgradeTries`）要不要在多次"shadow 尝试失败"之间持续累加到 30 分钟封顶？现有实现里 `p.restart()` 会重置探测状态机，`upgradeTries` 是否连带被清零还需要确认；改成不重启主探测之后，这个计数的生命周期应该更自然地贴着 `Probe` 本身，需要在实现时核对现有代码有没有意外清零的路径。
2. `shadowICEDialer` 要不要复用主连接同一个 UDP 端口（`wg-port`）做候选收集，还是需要独立端口？复用端口能省一次 NAT 打洞，但要确认不会和主 relay/ICE 连接的收发路径混在一起。
3. 接收端因为 `IsUpgradeProbe` 走的是独立于主 `Probe` 状态机的响应路径，这部分临时状态（谁在跟谁做后台探测）要不要设过期清理，避免异常/恶意节点通过反复发起假的升级探测消耗接收端资源——可以先简单加一条"同一 remoteId 同时只允许一个 shadow 探测在途"的限流。

## 决策记录（2026-10-03，实现时敲定）

1. **`upgradeTries` 生命周期：维持现状**。核对结果：restart 从不清零（`cancelUpgrade(false)` 只拆 timer），仅 ICE 成功（`cancelUpgrade(true)`）与 `Close` 清零——恰好就是想要的语义，无需改动。
2. **UDP 端口：复用共享 `FilteringUDPMux`**（与主 ICE 拨号器同一套 UDP socket，pion 按 ufrag/pwd 分流），影子 agent 自生成独立 ufrag/pwd，不开新端口。多个 agent 共用一个 mux 与今天"每 peer 一个 agent"同量级，且仅 relayed peer 的 60 s 尝试窗口内存在。
3. **接收端限流：每 remoteId 同时最多 1 个在途 shadow**（实现于 `handleShadowSignal`）。在途期间后续带标记 SYN 只由既有 shadow 应答、不新开；60 s 尝试预算 + probe `Close`/restart 全量回收，不做更细的过期清理。

实现落点：`internal/server/transport/probe_shadow_upgrade.go`（shadow 拨号器）、`probe_upgrade.go`（`tryUpgrade` 改驱动 shadow，不再 `restart`）、`signal.Handshake.IsUpgradeProbe/AttemptID` 与 `Offer.AttemptID`（信令标记，旧节点 JSON 反序列化自动忽略）。
