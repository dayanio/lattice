# Lattice 客户端 UI Mockup：现状草案、延伸差距与 AI 差异化

**日期**：2026-09-13
**状态**：Draft（可视化稿，未进入实现）
**范围**：`apple/`（macOS 客户端）UI 方向
**关联文档**：[对标 Tailscale 的功能差距与路线图](./2026-09-13-apple-client-vs-tailscale-gap-roadmap.md)、[Apple 客户端设计](./2026-09-13-apple-clients-design.md)

本文把路线图落成两版可交互 mockup（静态 HTML，用浏览器直接打开即可预览），并在此基础上补两块内容：还有哪些 Tailscale 有而 Lattice 没做、值得补的能力；以及 Lattice 已经具备、Tailscale 完全没有的 AI 原生能力应该怎么在客户端里露出来。

## 一、两版 mockup

### 1. 阶段一简版 —— 沿用现有独立窗口

文件：[`assets/2026-09-13-apple-client-ui/phase1-mockup.html`](./assets/2026-09-13-apple-client-ui/phase1-mockup.html)

窗口尺寸、列表行样式（7px 状态圆点、等宽字体地址）直接照抄 `apple/LatticeMac/ContentView.swift` 现有实现，只叠加路线图阶段一的三项：

- **连接质量展示**：状态行加"直连 / 经中继 + 延迟"徽标
- **设备操作入口**：列表行 `···` 菜单（重命名 / 查看 ACL / 移除设备）
- **ACL 调试视图**：点进设备看策略判定结果（能连 / 被拦截 + 命中的策略名）

### 2. 全量设计 —— 菜单栏形态，覆盖对比表全部 8 项 + AI 差异化

文件：[`assets/2026-09-13-apple-client-ui/full-menubar-mockup.html`](./assets/2026-09-13-apple-client-ui/full-menubar-mockup.html)

外壳换成菜单栏常驻应用（路线图阶段三的终态），四个部分：

| # | 内容 | 对应能力 |
|---|---|---|
| 01 | 主面板：连接质量、Exit Node 快捷开关、设备列表 / 设备详情（ACL、Taildrop 式发文件、key 过期续期） | 连接质量、ACL 可视化、Taildrop、设备管理 |
| 02 | 网络设置（Exit Node、子网路由、MagicDNS）、共享（Funnel/Serve 生成访问链接） | Exit Node、子网路由、MagicDNS、Funnel/Serve |
| 03 | 登录页：账号密码 + SSO 入口标"即将推出" | SSO/OIDC（如实反映 Phase 5 暂缓的决定，没有假装已可用） |
| 04 | AI 原生能力：见下文第三节 | —— |

移动端体验、审计日志两项没有重复做——前者是另一个平台的工作量，后者 Web 端（`frontend/src/pages/settings/audit`）已经是完整功能。

## 二、还有哪些 Tailscale 有、Lattice 没做、值得补

以下是对比表之外，逐项核对代码库之后补的差距，按"值得做"的判断附了可行性：

可行性标记跟第一份文档统一：✅ 纯客户端或轻量工作，🟡 能接在现成组件上但要新写一部分，🔴 需要新子系统/新状态机。

| 能力 | Tailscale | 代码库现状 | 可行性 |
|---|---|---|---|
| 设备标签 / 按标签写 ACL | 打 tag，策略按 tag 匹配而不是单个设备 | `t_peer` 表已有 `labels` 字段，说明底层数据模型留了口子，但没有客户端/前端的标签管理与"按标签建策略"界面 | 🟡 数据模型已经在，主要补 UI 和按标签查询/建策略的接口 |
| Ephemeral 节点 | 节点断开一段时间后自动从列表清除，适合 CI / 临时机器 | 代码库里没找到对应字段或清理逻辑（`LatticePeer`/`t_peer` 都没有 ephemeral 概念） | 🔴 要新增 TTL 字段 + reconciler，是个新状态机，只是量不大 |
| Mesh 内 SSH（类似 `tailscale ssh`） | 免密钥直接 SSH，走 tailnet 身份认证 | 没找到相关实现 | 🟡 能复用现有 WireGuard 隧道做端口转发，认证层可以借 `PeerIdentity` 的身份，但协议本身要新写 |
| 客户端内多 workspace 切换 | 一个账号下可以在多个 tailnet 间切换 | 后端 workspace 模型已经很完整（`workspaces/list` 等接口），但 macOS 客户端一次只认一个 `serverURL`，没有"切换工作区"的入口 | ✅ 纯客户端 UI 缺口，后端不用动 |
| 加入审批（设备上线前需人工批准） | 管理员可以要求新设备先审批 | 没找到"pending approval"状态，enrollment token 校验通过就直接签发身份 | 🔴 属于新状态机，工作量比前几项大 |

## 三、AI 原生能力 —— Tailscale 完全没有的部分

这块不是"补差距"，是反过来的：Lattice 已经具备、且已经在跑的能力，Tailscale 的产品形态里根本没有这个概念。核对下来，这些不是设计稿，是已经存在的代码：

- **`AgentIdentity` / `PeerIdentity` CRD**（`api/v1alpha1/agent_identity_types.go`、`peer_identity_types.go`，前端 `usePeerIdentityStore`/`useAgentIdentityStore`）——网络里的身份不只是"人的设备"，AI agent 也是有身份、有零信任校验的一等公民
- **Intent AI**（`internal/server/service/intent.go` + `frontend/src/pages/ai/intent.vue`）——已经上线的"自然语言描述 → 生成访问策略"能力
- **Agent Sandbox**（`internal/agent/sandbox`）——gVisor 隔离，agent 节点跑在沙盒里

这些目前都只存在于 Web 管理端，macOS 客户端里完全没有体现——打开客户端看到的设备列表，人的笔记本和一个在跑的 AI agent 长得一模一样。第 04 节的两张图示意怎么把这些露出来，而不是重新设计一套：

- **设备列表区分 AI Agent 类型**：agent 节点标 "AI" 角标 + 沙盒隔离状态徽标，复用已有的 `AgentIdentity`/沙盒状态数据 —— ✅ 纯客户端工作，三块数据后端都已经有
- **ACL 页面嵌入自然语言输入框**：直接调用已经在跑的 Intent AI 接口，生成结果落在阶段一已经做好的 ACL 判定组件里显示——两块已有的东西拼在一起，不是新起炉灶 —— ✅ 纯客户端工作

跟第二节那五项对比就能看出来：那五项大多数要么要新状态机（🔴）要么要补一部分后端（🟡），这两项是唯二"后端全齐、纯客户端就能做完"的——这也是为什么值得优先摆进阶段一附近，而不是放到路线图末尾。

这才是客户端真正应该讲的故事：不是"迟到的 Tailscale 平替"，是"Tailscale 做不到的 AI 原生网络"。前面两节的 8 项 + 5 项差距补完只是及格线，这一节才是差异化。
