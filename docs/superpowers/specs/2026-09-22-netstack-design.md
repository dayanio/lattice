# gVisor netstack 立项：Apple 端用户态协议栈

> **⚠️ 重大修正（2026-09-22 当晚，立项同日）**：实验推翻了本立项的核心前提。实测证明 **Mac 原生就能在 overlay 上监听并接受入站连接**——cloud-node-1 与 lattice-gateway 均成功连入 Mac 上 `0.0.0.0` 监听的服务（NEPacketFlow 写回的包正确到达宿主栈，utun 接口持有 overlay 地址）。此前"Mac 只能出站、监听需要 netstack"的判断是把个别 peer 的会话故障误判为架构限制。**结论：本项目的核心卖点（监听能力）不成立，项目归档**；剩余的扩展内 L4 处理（连接粒度策略、性能优化）如未来出现真实需求再重启评估。文件传输改为两端内嵌收发端点的对称设计（无需本项目）。

**日期**：2026-09-22
**状态**：已归档（当日实验证伪核心前提）
**范围**：`apple/engine`（Go 引擎）、`apple/LatticeTunnelMac`（可选接线）、go.mod。不改变 Linux 端（内核 TUN 更优，netstack 在 Linux 仅作为可选模式）。
**关联文档**：[客户端能力补全设计 §五-二期](./2026-09-22-mac-client-capabilities-design.md)、[对外发布设计](./2026-09-22-mac-client-capabilities-design.md)、[文件传输 v1 设计（会话记录，节点接收器先行）](./2026-09-22-mac-panel-redesign-design.md)

---

## 一、背景与动机

Apple 端现状：`NEPacketTunnelProvider` 收发裸 IP 包，`packetTUN`（`apple/engine/packet_tun.go`）把它们与 wireguard-go 直连——**引擎在 overlay 上没有任何监听能力**，一个 TCP/UDP 包都无法在扩展内终结。这阻塞了四个已立项/已感知的需求：

| 被阻塞的能力 | 现状 |
|---|---|
| 文件传输对称化（Mac 也当接收端、Mac↔Mac） | 文件传输 v1 只做"Mac→节点推送"，Mac 无法监听 |
| 子网路由二期（本机用户态转发） | 无 TCP/IP 栈，收到发往所广播子网的包只能丢弃 |
| LatticeDNS 完整化 | `interceptLatticeDNS` 手搓 UDP 应答，仅 A 记录、仅 53 端口 |
| 开发模式（本地 SOCKS5/HTTP 代理） | 不可能 |

引入 gVisor netstack 后，wireguard-go 的对端从"裸包桥接"换成用户态协议栈：引擎获得完整的 TCP/UDP 终结与监听能力，以上四项共用同一次架构投入。

## 二、目标 / 非目标

**目标**
1. 引擎内嵌 netstack：host→overlay 的 TCP/UDP 流经 netstack 终结后按 L4 重拨（Tailscale iOS 同款），行为与现 packetTUN 路径对等
2. 引擎可在 overlay 地址上监听 TCP/UDP（文件接收端点、SOCKS5、子网代理的承载基座）
3. LatticeDNS 迁移为 netstack UDP 端点上的完整解析器
4. 运行时可回退：netstack 与 packetTUN 由引擎配置开关切换，默认初始为 packetTUN
5. Linux 端不受影响（内核 TUN 优先）

**非目标**
- 不取代 `NEPacketTunnelProvider`——它是 Apple 允许的系统流量唯一入口，VPN profile 依然必需
- 不做内核 TUN 替代（Linux 维持现状）
- 不在 netstack 内实现 L7 策略（策略引擎独立演进）
- "纯用户态无 VPN"模式（WG+netstack+本地 SOCKS，无 NE）不在本期；如需可作 M2 的附带产物

## 三、架构

### 现状

```
宿主 App ── NEPacketFlow ──> packetTUN（chan 裸包桥接） ──> wireguard-go ──UDP──> 对端
```

要点：ICE/LRP 数据面（`infra.DefaultBind`，直连 UDP 套接字）**不经 TUN**——netstack 改造不触及 ICE；只有"宿主应用发往 overlay 的包"走 packetTUN。

### 目标

```
宿主 App ── NEPacketFlow ──> [注入 netstack]
                                │  TCP/UDP → 终结后经 L4 重拨 ──> wireguard-go ──> 对端
                                │  监听套接字（file-drop :7818 / SOCKS / DNS:53）
                                └─ 未匹配（含 WG 自身、ICMP 等）→ 透传 wireguard-go
```

三个语义要点：

1. **L4 代理语义**：宿主应用的 TCP 连接在扩展内终结，扩展以新连接拨向对端 overlay 地址——端到端仍是一层 WG 加密，但每连接多一次 TCP 终结（性能见 §六风险）。
2. **监听语义**：netstack 上的 TCP listener 绑定 overlay IP:port，由扩展内 Go 代码直接 serve——这就是 Mac 端文件接收、SOCKS 的基座。
3. **移除语义**：对端从 netmap 消失时，listener 不受影响；对端连接直接断开（netstack 关联 endpoint 清理）。

### packetTUN 改造方案

- 新增 `apple/engine/packet_netstack.go`：
  - gvisor `stack.Stack()` + 一个 `LinkEndpoint` NIC 挂给 wireguard-go 作 `tun.Device`（WG 读写 = netstack NIC 读写）
  - `NEPacketFlow → netstack` 注入路径：复用现 `SendPacket` 入口，按目的地址分流——netstack 拥有的（监听端口、DNS）进栈终结，其余 `Inject` 给 NIC（→WG）
  - 反向：WG 解密包 → netstack NIC → 已建立连接写回 `DeliverPacket`
- `engine.go`：`run()` 按配置选择 `newPacketTUN(...)` 或 `newPacketNetstack(...)`（配置字段 `netstack bool`，默认 false）
- `interceptLatticeDNS`：netstack 模式下由 UDP:53 listener 承接（解析器复用现有 peer 表逻辑），packetTUN 模式保留原实现
- 文件接收端点（`file-drop`）与 SOCKS：netstack listener 上的独立 handler，复用文件传输 v1 的端点协议

## 四、gVisor 依赖评估

| 项 | 结论 |
|---|---|
| 模块 | `gvisor.dev/gvisor`，无语义化 tag，pin master 伪版本（当前解析 `v0.0.0-20260922014844-164b166ce347`，当日新鲜） |
| 既有接触面 | `go.sum` 已存在 `github.com/google/gvisor` 间接引用（pro-bing 链路），模块已在依赖图内 |
| 引入面 | 只 import `pkg/tcpip`、`pkg/tcpip/stack`、`pkg/tcpip/transport/{tcp,udp}`、`pkg/tcpip/link/*`——避免引入整个 gVisor 应用内核 |
| gomobile 兼容 | Tailscale iOS 同栈先例，纯 Go 可交叉编译；M0 用真实 `gomobile bind` 验证（含 iOS） |
| 二进制体积 | 预估 +15~25MB（Frameworks/LatticeCore 当前 ~68MB），可接受；M0 实测 |
| NE 内存 | netstack 默认缓冲偏大，需按 NE jetsam 预算下调（M0 给出水位基线）；`lattice-ne.log` 监控内存 |
| CPU 唤醒 | netstack 定时器（探测/keepalive）新增唤醒源——NE 上限 45000 次/300s 的已知红线，M1 起 `lattice-ne.log` + 内核日志双监控 |

**备选已否决**：手写最小 TCP 栈（正确性代价远超引入 gVisor）；控制面中转文件（违 P2P 原则，仅作 Mac↔Mac 的 v2 备选）。

## 五、里程碑

| 里程碑 | 内容 | 验收 | 预估 |
|---|---|---|---|
| **M0 依赖验证 spike** | linux 裸机 harness：netstack ⇄ wireguard-go 直连跑通；gomobile bind（macos+ios）+ 二进制体积 + 内存水位基线；pin gvisor 版本 | harness 内 TCP/UDP 双向通；bind 产物可启动；体积/内存数据入档 | 2-3 天 |
| **M1 出站 L4 代理对等** | `netstack` 配置开关 + 出站 TCP/UDP 走 netstack；与 packetTUN A/B（连通性/吞吐/延迟/长稳） | 开关切换后宿主流量行为对等；无 NE 崩溃/唤醒超标；可随时回退 | 1 周 |
| **M2 监听基座** | netstack TCP listener + 文件接收端点迁到 Mac 端（文件传输对称化）+ 本地 SOCKS5（附带产物） | 云节点/容器节点可向 Mac 推送文件；curl --socks5 走 overlay | 3-5 天 |
| **M3 LatticeDNS + 子网路由迁移** | DNS 换 netstack UDP 端点（完整解析器）；子网路由二期用户态转发基于 netstack handler | DNS 各类记录/上游转发；对端可达本机广播子网 | 3-5 天 |
| **M4 默认切换** | `netstack` 默认 true（packetTUN 保留为回退开关）；文档 + 数据面长稳报告 | 一周灰度无回归 | 2 天 |

每个里程碑独立成 commit；M1 起每步在真机 NE 上验证（非仅 linux harness）。

## 六、风险

1. **NE 内存/wake 超标**（最高风险）：M0 出基线，M1 起长稳监控；超标则调 netstack 缓冲参数或降级回 packetTUN（开关保底）。
2. **TCP 终结性能**：每连接多一跳；以吞吐/延迟 A/B 数据说话，Web/文件场景预期可接受。
3. **gVisor 跟进成本**：pin 伪版本，升级非强制；API 面（tcpip/stack）多年稳定。
4. **UDP 会话语义**：QUIC 等长活 UDP 流在 netstack 是按 4-tuple endpoint 承接，需在 M1 用 QUIC 流量实测（游戏/语音类同理）。
5. **与删除节点下发 bug 无耦合**：netmap 版本号问题独立修复，不阻塞本项。

## 七、与相邻工作的关系

- **文件传输 v1**（节点接收器 + Mac 推送）：不等待本项，先行落地；其端点协议在 M2 原样迁到 Mac 监听上，实现对称化。
- **子网路由二期**：M3 之前不启动；M3 即它的实现载体。
- **LatticeDNS v1**：不受影响，继续服务 packetTUN 模式；M3 完成后 packetTUN 模式仍保留原拦截实现（回退一致）。
