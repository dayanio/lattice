# macOS 客户端能力补全：审批流 · 连接统计 · 子网路由 · 对外发布 · LatticeDNS

**日期**：2026-09-22
**状态**：Draft（待评审）
**范围**：`apple/LatticeMac`、`apple/engine`（Go 引擎）、`apple/LatticeTunnelMac`、`internal/server`（最小增量）、`internal/agent`（网关角色）。不含 Vue 网页控制台、iOS 端（复用 Shared 层的改动顺带生效，但不为 iOS 做适配）。
**关联文档**：[面板重设计](./2026-09-22-mac-panel-redesign-design.md)、[Exit Node / 子网路由设计](./2026-09-14-exit-node-subnet-route-design.md)、[LatticeDNS 设计](./2026-09-17-latticedns-design.md)、[Apple 差距路线图](./2026-09-13-apple-client-vs-tailscale-gap-roadmap.md)、[ADR-0003 入网审批](../../adr/0003-peer-enrollment-approval-and-client-side-keygen.md)

---

## 一、背景与普查结论

面板重设计落地后，客户端的信息架构已收敛（首屏 + 常驻 tab 栏 + 统一弹窗）。本设计补齐五项"UI 已有入口但能力空转 / 完全缺失"的功能。写设计前对代码做了普查，结论汇总：

| 功能 | 已有地基 | 真实缺口 | 动哪些层 |
|---|---|---|---|
| LatticeDNS | Swift 两侧已设 `NEDNSSettings(servers:["10.96.0.1"], matchDomains:["lattice"])`；引擎 `interceptLatticeDNS` 完整实现（`apple/engine/packet_tun.go:165-274`）+ `SetDNSResolver/SetPeerSource` 钩子 | **只差接线**：`engine.go` 从未调用这两个钩子，`t.dnsResolver != nil` 永假 | engine（几行）+ 重 bind + UI 解锁 |
| 连接统计 | wireguard-go fork 的 `Device.IpcGet()` 可取 tx/rx/last_handshake；解析器 `internal/agent/wireguard/ipc_stats.go` 已有（只解析 rx）；pathPing 已测 RTT 但只写日志；`TunnelManager` → NE `handleAppMessage` 轮询管道现成 | 解析器补 tx；引擎聚合 stats 并扩展快照 JSON；Swift 侧填空字段 + UI | engine + LatticeTunnelMac + TunnelManager + PeerDetailView |
| 入网审批流 | 服务端状态机（approved/pending/revoked）、`PUT /api/v1/peers/:name/approval`（admin）、agent 侧 pending 轮询、客户端 JoinFailure 分类全部就绪 | `PeerVo` 不返回 approval_status（管理端无法渲染）；workspace 门禁开关无 API；App 无审批 UI | server（VO 一处）+ LatticeAPI + App UI |
| 子网路由 | 声明/消费链路全通：`advertised-routes` API → netmap `AllowedIPs` 展开 → 引擎 `OnRoutesChanged` → Swift 装 `NEIPv4Route`；Linux 侧 iptables 转发参考实现已有 | 广播方向缺 CIDR 输入 UI（当前开关是 no-op）；Mac 引擎无用户态转发（收到发往所广播子网的包后丢掉） | App UI（v1）+ engine 转发层（v2） |
| 对外发布 | **零实现**（全仓无 funnel/ingress 代码）；Ferry 中继帧头无流 ID，不能直接复用为 TCP 通道 | 全部：发布注册表、ingress 数据面、Share 页真实现 | server（API+表）+ agent（网关角色）+ App UI |

通用约束：每个功能独立成 commit（`git commit -s`、无 Co-Authored-By、提交前 lint）；改 engine 后必须重跑 `apple/Scripts/build_framework.sh` 重 bind，并按惯例校验嵌入 framework 哈希；UI 文案中文。

---

## 二、LatticeDNS（引擎 DNS 应答接线）

### 现状

- Swift `PacketTunnelProvider`（Mac 与 iOS 相同）已配置：DNS 服务器 `10.96.0.1`、`matchDomains = ["lattice"]`——只有 `*.lattice` 的查询会进隧道，其余走系统 DNS，天然 split-DNS。
- 引擎侧 `interceptLatticeDNS`（packet_tun.go:165-274）已实现：拦截隧道内 IPv4/UDP 目标端口 53、后缀 `.lattice` 的查询，用 peer 表（`NormalizeAppID(name)` 匹配）返回 A 记录，手工构造应答并重算校验和；非 lattice 查询 fail-open 直通 WireGuard。
- 入口 `WriteInbound` 里 `interceptLatticeDNS` 仅当 `t.dnsResolver != nil` 才启用，而 `SetDNSResolver`/`SetPeerSource` 只在测试里被调用——**生产路径从未接通**。

### 设计

1. **引擎接线**（`apple/engine/engine.go`，packetTUN 创建后）：`tun.SetPeerSource(node peerManager 的 GetAll)`；`tun.SetDNSResolver` 置为启用应答（resolver 始终 on；peer 表为空时返回 NXDOMAIN 语义的"无记录"）。v1 不做开关——隧道在，DNS 就在。
2. **搜索域**：Swift `dnsSettings.searchDomains = ["lattice"]`，让短名 `node-a` 自动补全为 `node-a.lattice`。文案统一为 **`<节点名>.lattice`**（现行 mockup 里的 `lattice.internal` 弃用；`matchDomains` 与拦截器本就匹配短后缀，避免为后缀美观而改两处匹配逻辑）。
3. **UI 解锁**（`NetworkPages.swift`）：LatticeDNS 行从 `disabledToggle` 换成真 Toggle 旁路说明？——不。v1 无服务端记录、无自定义域，无可关性可言：改为**状态行**："已开启 · `<本机名>.lattice`"，描述改"用节点名代替 overlay IP 互相访问，例：node-a.lattice"。原 disabled 样式删除。
4. **非目标**：TCP/53、应答缓存、自定义记录 API、公网 DNS、`AllowCustomDNS` 工作区标志——全部留待后续（服务端 DNS 记录表见 LatticeDNS 专项设计 2026-09-17）。

### 验收

- 连上隧道后，`dscacheutil -q host -a node-a.lattice` / `ping node-a.lattice` 解析到该节点 overlay IP；本机名 `.lattice` 也能解析到自己。
- 非 lattice 域名解析不受影响（走系统 DNS）。
- iOS 端同链路，行为一致（未单独适配但应天然可用；不作为验收项）。

---

## 三、设备连接质量与流量统计

### 现状

- wireguard-go（fork `wireflowio/wireguard-go`）的 `Device.IpcGet()` 输出每 peer 的 `tx_bytes/rx_bytes/last_handshake_time_sec`；解析器 `PeerStatsFromIpc`（internal/agent/wireguard/ipc_stats.go:34）已在心跳链路使用，但**只解析 rx_bytes**。
- ICE path ping 已测 RTT（internal/server/transport/probe_pathping.go:85）但结果只写日志。
- 数据管道：引擎 2s ticker 聚合 peer states → NE 进程内缓存 → App `TunnelManager.pollPeerStates` 用 `sendProviderMessage("peerStates")` 拉取 JSON 快照（`{peerStates, lastError, publicKey, overlayIP, peers, phase}`）→ UI。**扩展此快照即可，不需要新增跨进程通道。**

### 设计

1. **Go 侧**：
   - `ipc_stats.go` 补 `tx_bytes` 解析（对齐 rx 的写法），`PeerStats` 增加 `TxBytes`。
   - pathPing 的 RTT 存入 Probe 结构并暴露 getter（probe_factory.go），取不到（probing/中继态）时为空。
   - 引擎在现有 2s 轮询处（engine.go:363-389）聚合 `map[peerName]PeerStatsJSON{state, rx, tx, lastHandshakeAgo, rttMs}`，替换/扩展现有 `OnPeerStates` 的 JSON 载荷（字段全部带 `omitempty`，旧 Swift 解析不受影响）。
   - **改 engine 必重跑 `Scripts/build_framework.sh`**（gomobile rebind），这是本功能唯一的构建链负担。
2. **Swift 侧**：
   - `PacketTunnelProvider` 缓存与 `TunnelManager` 快照结构同步加 `peerStats` 字段；`PeerNode` 增加 `rxBytes/txBytes/lastHandshakeDate/rttMs` 可选字段（TunnelCore.swift）。
   - `TunnelManager` 为每 peer 维护**内存环形缓冲**（60 样本 × 2s = 2 分钟）：rtt 序列 + 由 tx/rx 差分算出的瞬时上下行速率。不落盘，进程退出即清。
3. **UI**（`PeerDetailView` 增加两段）：
   - **连接质量**：状态 pill（现有）+ 延迟 `RTT 23 ms`（无数据显示"—"）+ 60 样本 RTT 迷你折线（SwiftUI `Path` 手绘，无第三方依赖）。
   - **流量**：`↑ 1.2 MB/s ↓ 8.4 MB/s`（实时速率）+ 会话累计 `↑ 120 MB ↓ 1.1 GB`（字节人性化格式）+ 最近握手时间。
   - 面板不展示这些；详情页从 peerRow 点入，数据在详情页可见期间持续刷新（`TimelineView` 或沿用 2s 快照驱动）。

### 验收

- 两台节点互 ping 产生流量后，详情页能看到延迟数值与曲线、上下行速率随 `ping`/`httpd` 请求变化、累计字节与 `wg` 语义一致（单调递增）。
- 中继态（relay-ready）节点显示经中继延迟或"—"，不崩溃、不显示过期数据。

---

## 四、入网审批流接入 App

### 现状（服务端几乎全备）

- 状态机：`approved / pending / revoked`（models/peer.go:21-25）；workspace 级门禁 `RequirePeerApproval`（默认 false）开启后新设备 enroll 即 pending 且**不分配 overlay 地址**。
- 审批 API 已有：`PUT /api/v1/peers/:name/approval`，body `{"status": "approved"|"revoked"}`，叠加 RoleAdmin 中间件；approve 会补分配地址，revoke 连带 `disabled=true`。**没有 rejected 独立态，"拒绝"= revoked。**
- agent 侧：pending 设备收 stub netmap，指数退避轮询等待；revoked 报"这台设备已被管理员停用"。
- **缺口**：`PeerVo`（vo/peer.go）与 `peerItem` 不带 approval_status —— 客户端看不到谁是 pending；`PageRequest.Status` 过滤参数存在但 `renderPeerPage` 未实现；workspace 门禁开关没暴露到 WorkspaceDto；管理端无推送（websocket 是死代码）。

### 设计

1. **服务端最小增量**（一个 commit）：
   - `vo/peer.go` 的 `PeerVo` 加 `approvalStatus`（json `approval_status,omitempty`）；`peerItem` 与两条 list 组装路径透传该字段 + `approvedAt`。
   - `renderPeerPage` 实现 `PageRequest.Status` 过滤（`status=pending` 只返回待审批），供 App 与网页共用。
   - 门禁开关 API 留到二期（见下），一期演示用 `latticed` 侧已有能力：对 workspace 记录直接置位（SQL 或 `lattice` CLI），文档写明。
2. **App 侧**：
   - `LatticeAPI.listPeers` 的 `PeerItem` 增加 `approvalStatus`。
   - **设备页 pending 分区**：`displayPeers` 里 `approvalStatus == "pending"` 的设备单独成组置顶（标题"待审批"，计数），行样式：名字 + 平台 + "等待审批"徽标，点开详情或行内 swipe 暴露两个动作：**批准** / **拒绝**（调 approval API，成功后刷新）。无管理登录时分区隐藏（与现有 needsLogin 逻辑一致）。
   - **徽标提醒**：设备 tab 图标加绿点（复用投屏 tab 的 `showsDot` 机制），有 pending 且已登录时点亮。
   - **刷新策略**：不做推送。设备 tab 可见期间每 30s 静默 `loadPeers()`（App 前台时），加上现有的手动刷新/进入页面刷新，够用且省一个 SSE 端点。
   - JoinFailure 的"等待管理员批准"文案补充一句"批准后本 App 会自动连上"（agent 在等待期间会自动完成入网，无需重试）。
3. **二期（本设计不实现，列出来源）**：WorkspaceDto 暴露 `requirePeerApproval` + 连接设置页开关；管理端 SSE（仿 ai.go 样板）替代轮询；`ApprovedBy` 审计落值；revoked 与 disabled 的状态语义理顺（netmap_builder 先查 disabled 的顺序问题）。

### 验收

- 开启门禁的 workspace 里新设备入网 → App 设备 tab 绿点亮、待审批分区出现该设备 → 点批准 → 设备移入正式列表并拿到地址；点拒绝 → 该设备端收到"已被管理员停用"。
- 未登录时看不到待审批分区；`status=pending` 过滤与全量列表分页正确。

---

## 五、广播子网路由 CIDR

### 现状

- 声明：`POST /api/v1/peers/:name/advertised-routes` body `{routes: [...]}`（清空=空数组），服务端做 CIDR 校验并落 `t_peer.AdvertisedRoutes`。
- 消费：其他节点选中本机为 provider 后，路由被展开进其 netmap `AllowedIPs` → 引擎 `OnRoutesChanged` → Swift `setTunnelNetworkSettings` 装 `NEIPv4Route`——**这条链路已通**（exit node 的 0.0.0.0/0 同机制）。
- 广播：本机收到发往所广播子网的 overlay 包后，当前直接丢弃（Mac 引擎没有转发层；Linux agent 有 iptables FORWARD+MASQUERADE 参考实现，task-H 分支有子网路由实现未合并）。

### 设计（分两期）

**一期（本次实现）：CIDR 编辑器，把 no-op 开关变成真能声明**

1. `NetworkSettingsView` 的"广播子网路由"行点开后从整页切换为**页内子页**（沿用退出节点选择的容器化模式）：路由列表（每行 CIDR + 删除）+ "添加路由"输入行（自动补全建议：读取本机 en0 的 IPv4 地址推算 `/24` 作为默认建议值，`getifaddrs` 实现，一行一个，可编辑）+ 保存按钮。
2. 校验（纯逻辑，进 `apple/Shared` 或 LatticeMac，配套加进 `test_apple_logic.sh`）：合法 IPv4/IPv6 CIDR 解析；拒绝 `0.0.0.0/0` 与 `::/0`（"要全局出口请用退出节点"）；去重；RFC1918 之外给出黄色提示但不阻止（home lab 常见 100.64.0.0/10 等）。
3. 保存即调 `setAdvertisedRoutes(selfName, routes)`，失败显示错误并可重试；成功后行描述显示当前广播列表。
4. **诚实边界**：本机作为 provider 的**转发**不在一期（见二期）。一期交付后，其他设备可以把本机选为"到 192.168.x.x 的路由"并安装路由，但流量到本机后仍不通——因此一期 UI 在广播页明确标注："其他设备尚未可达（本机转发能力开发中）"，避免演示翻车。

**二期（引擎用户态转发，单独立项）**：在 `packet_tun` 的入站路径（现成插入点 packet_tun.go:92-159，LatticeDNS 拦截器同层）识别目的地址命中自身广播子网的包，做用户态 NAT/代理后发往局域网并回注应答——Apple NE 沙箱无 iptables，需用户态实现；设计时评估直接引入 gVisor netstack 或手写最小 UDP/TCP 代理（provisioner.go:25-26 已有"不引 gVisor"的取舍注释，需重新评估）。Linux 侧以 task-H 分支为准合并。

### 验收（一期）

- 添加/删除 CIDR 落库正确（API 返回后列表即时反映），非法输入被拦截；`0.0.0.0/0` 被拒绝并提示改用退出节点。
- 其他节点选本机为 provider 后，其 netmap/路由表出现该 CIDR（服务器日志或对端验证）。

---

## 六、对外发布 v1（网关模式）

### 现状与决策

全仓零实现；Ferry 帧头（12 字节：Seq/PayloadLen/Cmd/ToID）没有连接/流 ID，无法在一条 agent 会话上多路复用公网 TCP 连接——把 Ferry 改造为流通道是协议手术，不适合 v1。

**v1 决策：发布网关跑在 mesh 成员上，不经服务端中转。** 选一台能被公网访问到的节点（演示环境 = 与 latticed 同机的 `lattice` agent）作为**发布网关**：公网 HTTP 请求 → 网关的 ingress 端口 → 网关在自己已建立的 WireGuard 隧道内直接 `dial <目标节点 overlay IP>:<port>`（网关本来就是 mesh 成员，数据面零新协议）→ 返回应答。这是 Tailscale Funnel 的反向思路（他们经 DERP 入站，我们经网关节点入站），换来的是 v1 不动 Ferry。v2 再把网关角色收进 latticed 进程（届时做 Ferry 流扩展：新增 Open/Data/Close 命令 + connID）。

### 设计

1. **发布注册表**（服务端，一个 commit）：
   - 新表 `t_publish`：`id, workspace_id, name(唯一), peer_name, port, path_prefix(=name), enabled, created_by, created_at`。GORM 迁移挂入现有 migrate 列表。
   - API（挂 `/api/v1/publish`，RoleAdmin 写 RoleViewer 读）：
     - `POST {name, peerName, port}` 创建（校验：name `[a-z0-9-]{3,32}`、port 1-65535、peer 存在且 approved）
     - `GET /list`、`DELETE /:name`（下线）
   - 网关如何拿到路由表：网关 agent 用自身管理凭证（或复用 enrollment 身份）定期 `GET /api/v1/publish/list`（30s）+ 变更时立即刷新。不新造推送通道。
2. **网关角色**（agent，一个 commit）：
   - `lattice --ingress :8090`：起一个 `httputil.ReverseProxy`，按首段路径 `/ name /…` 路由到 `http://<解析出的该 peer overlay IP>:port/…`（剥离 name 前缀、透传 Host 改写为目标 peer 名便于排查）。peer 名 → overlay IP 从**本地 netmap** 解析（agent 本来就持有），目标节点不在线返回 502 + 简体中文错误页。
   - 明确边界（写进 README 与 UI 文案）：v1 是 **HTTP/HTTP only、路径前缀路由**；不含 TLS/自定义域名/WebSocket 保证/带宽限制。访问控制 v1 = 可选 `?token=`（创建时可生成随机 token，网关校验 query 参数）；更细的策略接 policy 体系，留 v2。
3. **App 共享页真实现**（一个 commit）：
   - `ShareView` 重做：顶部"发布入口"列表（name、peer、端口、完整 URL 复制按钮、启停状态）+ "新发布"表单（选择本机还是指定节点、本地端口、名称），创建成功展示 URL + **二维码**（CoreImage `CIQRCodeGenerator` 生成 CGImage，无第三方依赖）+ 复制按钮。
   - URL 主机名取当前服务器地址的 host + ingress 端口（服务器地址已知；端口常量 8090，后续可入 discovery API）。
   - 手机演示路径：手机浏览器扫二维码 → 经公网/局域网访问网关 → 网关过隧道拿到节点上 busybox httpd 页面。
4. **演示环境**：在 latticed 同机起一个网关 agent（`./bin/lattice --server ... --token ... --ingress :8090`），它自己也是设备列表里的一个成员。

### 验收

- 创建发布 `demo → node-a:8080` 后，浏览器访问 `http://<网关IP>:8090/demo/` 返回 node-a 的 httpd 页面；删除发布后同 URL 返回 404。
- 目标节点下线时返回中文 502 页；带 token 的发布不带 token 访问被拒。
- App 共享页创建/删除/复制/扫码全流程可用。

---

## 七、实施顺序

按"价值/成本比 + 构建链依赖"排序，每期独立可发布、独立成 commit：

| 期 | 内容 | 主要改动 | 备注 |
|---|---|---|---|
| P1 | LatticeDNS 接线 | engine 几行 + searchDomains + UI 文案 | 最小改动，顺带验证 rebind 链路 |
| P2 | 连接统计 | engine 聚合 + 快照扩展 + 详情页 | 同样动 engine，紧接 P1 一次 rebind 熟练 |
| P3 | 审批流 | server VO 一处 + App 待审批分区 | 服务端改动最小的一期；演示需开 workspace 门禁 |
| P4 | 子网路由一期 | App CIDR 编辑器 + 校验（纯逻辑测试） | 明确标注转发未通 |
| P5 | 对外发布 v1 | t_publish + API + agent 网关角色 + Share 页 | 唯一的大块新地皮；建议 spec 评审后再动 |

P1/P2 都要 rebind framework，连续做省一遍心智负担；P3 起服务端/客户端可并行。

## 八、风险与开放问题

1. **gomobile rebind 风险**：P1/P2 改 engine 后 framework 重建，沿用 postBuildScript 自愈 + 手动校验哈希的流程；iOS 侧 framework 同步重生成（构建脚本已覆盖）。
2. **审批演示的开关**：门禁开关 API 二期才有，一期演示要动库。若评审认为不可接受，把 WorkspaceDto 字段提前并入 P3（改动很小，dto + update handler 透传）。
3. **对外发布的公网假设**：演示网关绑局域网 IP 也能完整走通功能（"公网"只是网关位置问题）；真正上公网需要 TLS/域名，属 v2。
4. **统计的字节口径**：tx/rx 是 WG 层字节（含加密开销），与应用层流量有偏差；详情页文案标注"含隧道开销"即可，不做换算。
5. **子网路由二期的转发实现选型**（gVisor netstack vs 手写代理）是 P5 后最大的技术不确定点，届时单独立 spec。
