# Lattice 客户端 vs Tailscale：功能差距与路线图

**日期**：2026-09-13
**状态**：Draft
**范围**：`apple/`（macOS 客户端）+ 对应后端接口
**背景**：当天把 macOS 客户端从"加入不了网络"调通到能看见在线节点，借这次审计顺带盘一下现状、跟 Tailscale 的差距，以及一份不推倒重来、顺着现有架构往上叠的路线图
**关联文档**：[Apple 客户端设计](./2026-09-13-apple-clients-design.md)

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

| 能力 | Tailscale | Lattice 现状 | 差距 |
|---|---|---|---|
| 设备管理 | 控制台重命名 / 下线 / key 过期踢出 | 管理面已有 `peers/list`，但客户端没有下线 / 重命名操作入口 | 部分具备 |
| Exit Node / 子网路由 | 可代理默认路由，或广播一段子网给全网 | 仅路由 overlay 网段（`10.96.0.0/24`），没有默认路由选项，代码里搜不到 exit-node 概念 | 缺失 |
| ACL 策略可视化 | `tailscale status` 能看到策略生效结果 | `LatticePolicy` CRD + eBPF 执行引擎都在，前端没有"这条策略对我生效了吗"的调试视图 | 部分具备（后端强，前端缺） |
| MagicDNS | 节点名自动可解析，免记 IP | 没有内置 DNS，节点间只能靠 overlay IP（比如今天调的 `10.96.0.4`） | 缺失 |
| 菜单栏 + 连接质量 | 常驻菜单栏，显示直连 / DERP 中继、延迟 | 独立窗口 App；ICE/LRP 状态机的数据其实都在引擎里，只是没有透传到 UI | 部分具备（数据在，没露出） |
| Taildrop 文件互传 | 节点间直接发文件 | 无对应能力 | 缺失 |
| Funnel / Serve | 把本机服务开放到局域网 / 公网 | 无对应能力 | 缺失 |
| SSO / OIDC 登录 | 企业版支持 SAML/OIDC | Phase 5 已设计（1 pro + 1 community 版本），按既定决定暂缓，先做用户名密码 | 已规划未落地 |
| 审计日志 | 企业版可查操作记录 | 后端表 + 前端页面都已完整存在 | 已具备 |
| 移动端体验 | iOS / Android 均为一等公民 | iOS 仅隧道壳子未接引擎；无 Android 工程 | 缺失 |

## 三、路线图：顺着现有架构往上叠

排序依据：日常使用的刚需程度，以及后端是否已经准备好（避免又做一遍"后端齐全、前端空白"）。

### 阶段一：把已经跑通的东西露出来

今天调通的连接状态机（ICE/LRP、错误事件）已经在引擎里跑，只是没人看得见——这是投入产出比最高的一批。

- **连接质量展示**：把 `EventConnecting/Connected/Disconnected` 和 ICE vs LRP 状态透传到 `onEvent`，UI 上标"直连"或"经中继"
- **设备操作入口**：管理面已有的 peer 增删接口接到客户端菜单里（重命名 / 下线）
- **ACL 调试视图**：读 `LatticePolicy` 生效结果，在节点详情里标"能连 / 被拦截"

涉及：`apple/engine/engine.go`、`apple/LatticeTunnelMac/PacketTunnelProvider.swift`、`api/v1alpha1` `LatticePolicy`

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
