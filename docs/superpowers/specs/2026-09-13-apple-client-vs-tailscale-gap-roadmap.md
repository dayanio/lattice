# Lattice 客户端 vs Tailscale：功能差距与路线图

**日期**：2026-09-13
**状态**：Active（阶段一落地中，见文末评审与进度）
**范围**：`apple/`（macOS 客户端）+ 对应后端接口
**背景**：当天把 macOS 客户端从"加入不了网络"调通到能看见在线节点，借这次审计顺带盘一下现状、跟 Tailscale 的差距，以及一份不推倒重来、顺着现有架构往上叠的路线图
**关联文档**：[Apple 客户端设计](./2026-09-13-apple-clients-design.md)

---

## 〇、评审记录（2026-09-13 晚，实现后修订）

按当天实际实现过程（提交 c99e44ad…d6178696）对本篇做核对，修正四处：

1. **"设备管理 ✅ 纯客户端工作"不成立，已补后端**：`UpdatePeer/DisablePeer/EnablePeer/DeletePeer` 原先只走 K8s client，standalone 下全部不可用。已为四者加上 `t_peer` 注册表分支（disabled 节点 netmap 构建器本就跳过，语义零新增），提交 ec6b12f3。
2. **"连接质量 ✅ 纯客户端工作"降级为已完成的引擎工作**：ICE/LRP 状态机在 `transport` 包里，Go 引擎原先完全没有对外暴露面。实际做的是 `ProbeFactory.PeerConnectionStates()` → `Node.ConnectionStates()` → 引擎轮询 → `EngineDelegate.OnPeerStates` → NE provider message 通道 → App UI 徽标（直连/中继）。跨了引擎、传输层、扩展、App 四层，不是纯客户端。
3. **"iOS ✅ 复用同一套引擎"补了两个隐藏前提**：(a) VictoriaMetrics 无 `GOOS=ios` 构建，引擎经 `transport`/`run.go` 的遥测边把依赖拖进来了——已加 `internal/metrics` 门面（iOS 为 no-op）并把 telemetry 管线抽到 `startTelemetry`（iOS stub）；(b) iOS NE 有 ~50MB 内存预算，当前引擎导入整棵 agent 根包（410 个依赖），上线真机前必须测量 RSS，超标则触发既定的"可移植核心拆分"。
4. **入网闭环曾卡五层，均已修复后才谈得上"调通"**：NSExtension 字典未进 Info.plist（f9b3aaff）→ 缺 NSExtensionPrincipalClass（8179a2d1）→ networkextension 受限授权缺描述文件（19275628）→ 签名授权不是描述文件子集的 app-groups（06bdfd59）→ VPN 配置创建时钉死代码要求、坏签名时代创建的配置永远拒绝新扩展（f4554cb5，join 时重建配置）。Mac 节点 10.96.0.4 已注册，闭环成立。

另：引擎注册现在回填 Name（此前 Mac 节点在列表里 name 为空），MagicDNS 的可行性在"差距表"里是 🔴、路线图却按可做列出的自相矛盾，按实现路径定为 🟡（workspace 内解析 = NE dnsSettings matchDomains + 引擎内置小型解析器，不涉及公网 DNS 子系统）。

---

## 一、现状盘点

标 **已验证** 的是当天实际调试/检索代码确认过的；其余按现有代码结构推断。

| 模块 | 现状 |
|---|---|
| 数据面 **（已验证）** | WireGuard 全互联，ICE 直连优先、LRP 中继兜底；状态机 `Created→Probing→ICEReady/LRPReady→Failed→Closed` |
| 控制面 | NATS 信令注册 + 心跳；`latticed` 单二进制（NATS+SQLite+API+UI）或 `manager` 接管 K8s CRD，两种部署形态二选一 |
| 策略执行 | Community 走 iptables；PRO + Linux 5.10+ 走 eBPF TC（`internal/agent/ebpf`），`SelectEnforcerMode()` 失败自动回落 |
| 审计日志 **（已验证）** | 后端 `t_audit_log` 表 + 前端 `frontend/src/pages/settings/audit` 页面已经是完整闭环，比预期完整 |
| macOS 客户端 **（今天调通）** | `NEPacketTunnelProvider` 内嵌 Go 引擎（gomobile bind）；输入服务器地址 + 加入 token 即可入网，节点列表走管理面 JWT 单独鉴权 |
| iOS / Android **（已验证）** | iOS 只有一个空的 `PacketTunnelProvider.swift` 壳子，未接引擎；仓库里没有 Android 工程目录 |

## 二、对比 Tailscale：差距在哪

口径：只比"客户端日常会用到"的能力，不比商业条款（价格、SLA）。

"可行性"是后来又核了一遍代码之后补的：只是客户端没露出来、后端已经齐全的标 ✅；能接在现成组件上、但要新写一部分的标 🟡；得动新子系统/新状态机的标 🔴；不是功能实现问题、是平台/工程量问题的标 ❌。

| 能力 | Tailscale | Lattice 现状 | 差距 | 可行性 |
|---|---|---|---|---|
| 设备管理 | 控制台重命名 / 下线 / key 过期踢出 | 后端 `peerService` 的 `UpdatePeer`/`DisablePeer`/`DeletePeer` 全都有，客户端就是没有下线 / 重命名的操作入口 | 部分具备 | ✅ 纯客户端工作 |
| Exit Node / 子网路由 | 可代理默认路由，或广播一段子网给全网 | 仅路由 overlay 网段（`10.96.0.0/24`），没有默认路由选项，代码里搜不到 exit-node 或路由广播的概念 | 缺失 | 🔴 路由广播语义要从零加 |
| ACL 策略可视化 | `tailscale status` 能看到策略生效结果 | `LatticePolicy` CRD + eBPF 执行引擎都在，`internal/server/service/policy_preview.go` 也有 `PreviewPolicy` 可以复用，但它现在做的是"改策略前 diff"，不是"这条连接现在被谁拦了"的运行时判定 | 部分具备（后端强，前端缺） | ✅ 主要是客户端工作，后端可能要补运行时判定 |
| MagicDNS | 节点名自动可解析，免记 IP | 没有内置 DNS，节点间只能靠 overlay IP（比如今天调的 `10.96.0.4`） | 缺失 | 🔴 没有任何 DNS 组件，等于起一个新子系统 |
| 菜单栏 + 连接质量 | 常驻菜单栏，显示直连 / DERP 中继、延迟 | 独立窗口 App；ICE/LRP 状态机的数据其实都在引擎里，只是没有透传到 UI | 部分具备（数据在，没露出） | ✅ 纯客户端工作 |
| Taildrop 文件互传 | 节点间直接发文件 | 无对应能力，但 `internal/relay` 已经有 QUIC 的 stream/session 抽象（`lrp_client_quic.go`、`session_manager.go`）可以复用做传输层 | 缺失 | 🟡 传输层有底子，分片/续传/UI 要新写 |
| Funnel / Serve | 把本机服务开放到局域网 / 公网 | 无对应能力 | 缺失 | 🔴 需要新的公网反代/路由服务 |
| SSO / OIDC 登录 | 企业版支持 SAML/OIDC | Phase 5 已设计（1 pro + 1 community 版本），按既定决定暂缓，先做用户名密码 | 已规划未落地 | 🟡 设计和骨架代码都在，捡回来接着做 |
| 审计日志 | 企业版可查操作记录 | 后端表 + 前端页面都已完整存在 | 已具备 | ✅ 已完整 |
| 移动端体验 | iOS / Android 均为一等公民 | iOS 仅隧道壳子未接引擎；无 Android 工程 | 缺失 | iOS ✅ 复用同一套引擎；Android ❌ 从零起一个新客户端，不是"实现某功能"的量级 |

## 三、路线图：顺着现有架构往上叠

排序依据：日常使用的刚需程度，以及后端是否已经准备好（避免又做一遍"后端齐全、前端空白"）。

### 阶段一：把已经跑通的东西露出来

今天调通的连接状态机（ICE/LRP、错误事件）已经在引擎里跑，只是没人看得见——这是投入产出比最高的一批。

- **连接质量展示** ✅ 已完成（ec6b12f3）：`ProbeFactory.PeerConnectionStates()` 快照经引擎 `OnPeerStates` 与 NE provider message 通道到 UI，节点行显示"直连/中继/连接中/失败"
- **设备操作入口** ✅ 已完成（ec6b12f3）：standalone 四个 peer 写操作补上 DB 分支；Mac 客户端节点右键菜单支持重命名 / 下线 / 上线 / 删除
- **ACL 调试视图**：读 `LatticePolicy` 生效结果，在节点详情里标"能连 / 被拦截"（P5/P6 的流统计已提供按策略命中计数，差每连接级判定）
- **iOS 接引擎** ✅ 已完成（d6178696）：`LatticeTunnel/PacketTunnelProvider` 实装同一 gomobile 引擎，iOS xcframework 入库；真机验证待做（模拟器跑不了 NE），且需先测内存预算

涉及：`apple/engine/engine.go`、`apple/LatticeTunnelMac/PacketTunnelProvider.swift`、`apple/LatticeTunnel/PacketTunnelProvider.swift`、`api/v1alpha1` `LatticePolicy`

### 阶段二：日常使用刚需

这两项是"能不能替代 Tailscale 日用"的分水岭，且服务端数据模型（`LatticePeer`/`LatticePolicy`）基本够用，主要是新增语义 + 客户端实现。

- **Exit Node**：一个节点广播"可代理默认路由"，客户端侧加一个 `includedRoutes` 之外的"全部流量走这个节点"开关
- **子网路由**：节点广播它背后的一段网段，其他节点自动学到路由——复用现有 `NEIPv4Route` 机制，服务端加广播语义
- **MagicDNS 雏形**：先做 workspace 内 `<name>.lattice.internal` 解析，不用一步到位做公网 DNS

涉及：`internal/agent/infra`、`apple/LatticeTunnelMac/PacketTunnelProvider.swift` 的 `makeSettings`、`api/v1alpha1` `LatticePeer`

### 阶段三：体验加分项

锦上添花，且部分依赖前两阶段先落地（比如 SSO 依赖 Phase 5 本来就规划好的双实现）。

- **菜单栏模式**：`LSUIElement` + `MenuBarExtra`，替代当前独立窗口
- **Taildrop 类文件互传**：走已有的 overlay 网络，加一个基于 QUIC 的传输通道（协议栈里已经有 QUIC）
- **SSO/OIDC**：捡回 Phase 5 搁置的设计文档直接实现，不是新设计

涉及：[`2026-05-29-tier-based-features-design.md`](./2026-05-29-tier-based-features-design.md)

---

**备注**：SSO/OIDC 的"暂缓"是既有决定（"Phase5先去掉吧，暂时不做第三方登陆了，后边再加"），这里不重新讨论是否要做，只是把它摆回路线图里该在的位置。
