# Lattice 代码库整体评审报告（2026-09-17）

- **评审范围**：隧道打通全链路（Signaling → ICE → WireGuard Bind → LRP Relay → Probe 状态机 → netmap 应用）、整体架构、稳定性、性能。
- **评审方法**：人工通读核心代码 + 定向抽查。未深读：gVisor 沙箱内部、`internal/server/service/ai.go`、K8s reconcilers 细节、前端、Windows Bind。
- **验证状态**：`go build ./...` 通过；`go test ./internal/relay/... ./internal/server/transport/... ./internal/agent/infra/...` 全绿；`go vet` 在已评审包上无告警。测试文件 99 / 源文件 400。
- **行号基准**：`new_dev` 分支，评审时工作区含 netmap-push 未提交改动（`internal/agent/node.go`、`internal/server/nats/nats.go`）。

---

## TL;DR

设计意图清晰、注释质量罕见——三阶段初始化、状态机防重入、STUN/WG 共享端口解复用都做得有章法。但 **LRP relay 是当前最薄弱的一环**：无鉴权、无帧长度上限、并发写无锁、断线不重连，四个问题叠加意味着"relay 兜底"这条承诺的路径在生产 NAT 环境下既不安全也不可靠。其次是 IPv6 数据面出站路径疑似从代码层面就是坏的。建议在继续堆功能（含 iOS）之前，先集中修 relay 与下列 P1。

---

## 一、隧道打通（连接生命周期）

设计链路：注册 → NATS 信令交换 SYN/ACK（携带 peer info）→ pion/ice 打洞（STUN-only，无 TURN）→ 成功后关闭 ICE agent、将 endpoint 写入 WireGuard，靠 liveness ticker（15s 查握手、45s 判死）+ PersistentKeepalive 维持 → ICE 失败回退 LRP relay（TCP/QUIC）；LRP 先就绪时给 ICE 500ms 加塞窗口，ICE 后到可升级回 P2P。状态机 + epoch + `restartInProgress` 防重入的骨架是好的。

### 已确认问题

| # | 级别 | 问题 | 位置 |
|---|------|------|------|
| 1 | **P0·安全** | relay 注册零鉴权，服务端直接采信客户端自报 ID，可冒充任意 peer 劫持其 relay 会话（收加密包做流量分析、注入垃圾包、DoS 其 relay 连通性），且无 workspace 隔离；叠加 QUIC 客户端 `InsecureSkipVerify: true`，MITM 也可接管 | `internal/relay/lrp_server.go:119`、`lrp_server_quic.go:116`、`lrp_client_quic.go:74` |
| 2 | **P0·安全/稳定** | relay TCP 服务端 `PayloadLen`（u32）无上限校验，`make([]byte, HeaderSize+int(h.PayloadLen))` 一个 12 字节恶意头即可触发 ~4GB 分配 → OOM DoS。客户端侧有检查（`lrp_client_tcp.go:248,264`），服务端漏了 | `internal/relay/lrp_server.go:151` |
| 3 | **P0·并发** | relay 转发写无锁：`SessionManager.Relay` 直接 `session.Stream.Write(frame)`，TCP 会话的 Stream 是 bufio 包装（非线程安全），两个来源 peer 同时向同一目标转发 → 数据竞争 + 帧交错损坏 | `internal/relay/session_manager.go:77`、`conn.go:32-39` |
| 4 | **P0·稳定** | 会话重注册竞态：旧连接 `defer Unregister(fromId)`，客户端重连时新连接 Register 覆盖 map → 旧连接断开时把**新会话**从 map 删除 → relay 黑洞直到下次重连。QUIC 侧同样 | `internal/relay/lrp_server.go:125`、`lrp_server_quic.go:119` |
| 5 | **P1·稳定** | relay 客户端无任何重连逻辑：只在构造时 Connect 一次，`writerLoop` 写失败直接 return，无 supervisor；relay 断开后该节点永久失去兜底路径（对比 NATS 客户端 `MaxReconnects(-1)`） | `internal/relay/lrp_client_tcp.go:68,159-198`、`lrp_client_quic.go:63` |
| 6 | **P1·需运行时验证** | IPv6 出站路径疑似整体损坏：`send6` 从不设置目标端口（代码被注释），且 `udpAddrPool` 缓冲被 `send4` 重切成 4 字节后 Put 回池，`send6` 的 copy 只能拷进 4 字节 → v6 地址截断 + 端口为陈旧值/0 | `internal/agent/infra/conn.go:388,426-427` |
| 7 | **P1·性能** | 信令回调里做重 OS 操作且持有 dialer 锁：`onPeerReceived` 在 `i.mu` 双检锁内直通 `RegisterPeer`/`ApplyRoute`（iptables/路由 exec）；NATS 订阅回调串行分发，一次慢路由操作卡住该节点所有 peer 的信令 | `internal/server/transport/ice_dialer.go:190-200` |
| 8 | **P2·资源** | ICE 先赢时 LRP 传输泄漏：`discover()` 中 ICE 先返回即 return，仍在拨号的 LRP 传输进缓冲 channel 后无人 Close，relay 会话挂到进程重启 | `internal/server/transport/probe.go:345-399` |
| 9 | **P2·稳定** | liveness ticker 被瞬时错误永久杀死：`getHandshake` 返回一次错误即退出整个监控循环，之后该 peer 静默死亡不再被检测。应 continue 而非 return | `internal/server/transport/probe.go:156-163` |
| 10 | **P2** | `iceDialer.Close` 不取消 SYN ticker（只有收到 ACK 才 cancel），每个 dialer 关闭后 goroutine 空转至多 60s；lrpDialer 的 Close 是对称正确的 | `ice_dialer.go:157` vs `lrp_dialer.go:301-312` |
| 11 | **P3** | ICE 接口过滤启发式太脆：只排除 `docker/veth/br-/wf*` 前缀，macOS 其他 VPN 的 `utun*`、`tailscale0` 会成为候选；`wf` 前缀也会误伤 | `ice_dialer.go:554-558` |

### TTFH 视角的行为（值得知晓而非缺陷）

对端离线时，一轮发现 = 65s Dial 超时（`ice_dialer.go:464`）+ 10s 退避（`probe.go:326-328`），约 140s 后 probe 进入 `StateClosed` 静默；对端重新上线靠 RESTART_NOTIFY / netmap 版本变更收敛。配合 netmap-changed 推送（见第六节），该路径收敛会显著加快。

---

## 二、稳定性

| # | 级别 | 问题 | 位置 |
|---|------|------|------|
| 12 | **P1·并发** | netmap 应用无串行化：`ApplyFullConfig` 有 5 个并发入口（Start、NATS MESSAGE push、`RunNetmapSync` 轮询、NATS reconnect handler、netmap-changed 回调）；`MessageHandler` 无锁，`Provisioner.mu` 标注 `nolint:unused`，`SetConfig` 的 IpcGet→比较→IpcSet 是裸 check-then-act | `internal/agent/message_handler.go:127`、`provision/provisioner.go:182`、`node.go:587-596,629-643` |
| 13 | **P2** | NATS reconnect handler 写 `node.current = peer` 与 `StatusSnapshot` 读无锁竞争 | `node.go:518` vs `node.go:677` |
| 14 | **P3** | DB 默认写进程 CWD（`lattice.db`），状态位置取决于启动目录（仓库根已出现 `-shm/-wal/.bak`） | `internal/db/db.go:59` |
| 15 | **P3** | agent 启动创建 JetStream 流 "LATTICE"（subjects `signals.>`），实际信令 subject 是 `lattice.signals.peers.*` 不匹配——纯死重，还有多 agent 并发创建竞态 | `internal/server/nats/nats.go:127-142` |
| 16 | **P3** | 信令/通知 subject 直接拼接 appID，含 `.` `*` `>` 或空格即可注入/错投，未见字符校验 | `internal/agent/infra/notify.go:24` |
| 17 | **P3** | `blackhole4/6` 死代码（只在 Close 置 false）；`FilteringUDPMux.DroppedCount()` 无任何调用方——过载丢包不可观测 | `conn.go:73-74`、`mux_filter.go:139` |

测试面：核心路径（probe 状态机、relay 协议、notify）有针对性用例且当前全绿；但 relay 的鉴权缺失/长度上限/并发写恰好都在测试盲区。

---

## 三、性能

- **数据面 inbound**：单 goroutine readLoop（单核）→ 每包一次分配 + copy → 512 深度 channel（满则丢、无指标）→ Bind 再 copy（`mux_filter.go:108-143`、`conn.go:264-276`）。百兆级没问题，Gbps 级是瓶颈；outbound 在 Linux 已用 WriteBatch。短期先补 droppedCount 指标，长期批量化。
- **relay TCP 转发**：每帧新分配 + `ReadWriterConn.Write` 每帧强制 Flush；已有 sync.Pool（`pool.go`）但只用在客户端 header。
- **控制面**：`notifyWorkspacePeers` 每次注册向全 workspace 逐个 publish（`peer.go:383-399`），配合客户端刷新逻辑，大 workspace 连续 join 会造成 N×全量刷新风暴——建议服务端去抖（合并 1-2s）+ 客户端合并刷新。
- **基准**：`hack/bench/`（TTFH/吞吐/API p99 + Shields 徽章）方法论成型，SLA gate 按计划推迟到 4 周基线，决策合理。

---

## 四、架构

| # | 级别 | 问题 |
|---|------|------|
| 18 | **P1·架构** | `internal/agent` 与 `internal/server` **双向 import**，边界名存实亡：agent 侧 import `server/client`、`server/nats`、`server/transport`（`node.go:34-36`）；server 侧 20+ 文件 import `agent/infra/config/log`，`server/run.go` 直接 import agent 包。数据面核心（Probe/ICE/LRP dialer）住在 `server/transport` 却被 agent 当运行时用。建议把共享运行时抽成 `internal/dataplane`（或 pkg/），`server/` 收敛为纯控制面 |
| 19 | **P2** | 巨型文件：`server/service/ai.go` 1868 行、`agent/controller/peer_controller.go` 1075 行、`server/service/peer.go` 1059 行——服务层上帝对象 |
| 20 | **P2** | Pro 门控（build tags，7 文件 + `requireFeature` 402 中间件）可用，建议把 community/pro 交叉点收敛到统一 capability 注册处 |
| 21 | **P3** | All-in-one（内嵌 NATS + SQLite + gVisor + wireguard-go 同进程）部署体验好，但各组件内存水位无上限配置，大 workspace 下的隔离性需文档化 |

---

## 五、设计亮点（值得保持）

1. `NewNode` 三阶段初始化把每个竞态窗口的消除理由写成注释（`node.go:184-207, 417-421`），是教科书级的"为什么是这个顺序"文档。
2. `FilteringUDPMux` 单读者解复用（STUN→ICE / 非 STUN→WG）干净消除共享端口读取竞争，v6 对称处理。
3. Probe 状态机 + epoch + `restartInProgress` 防重入，LRP→ICE 升级路径明确标注 "P1 bug fix"（`probe_factory.go:329-341`），踩坑被系统性吸收。
4. 推送 + 轮询 + reconnect 三通道收敛的 netmap 设计，鲁棒性思路正确。

---

## 六、netmap push 未提交改动评审意见（2026-09-16 改动）

方向正确、闭环完整（服务端 `peer.go:395,417` 发布 → agent `node.go:491` 订阅刷新）。建议：

1. `SubscribeRaw` 回调目前在 NATS 分发 goroutine 里同步跑最长 10s 的 HTTP+全量应用——通知风暴时会排队积压。改成丢 worker goroutine + 250ms 去抖 + 原子标志防重入。
2. 服务端目前只接了注册/更新两个点，policy/route/relay 变更路径尚未发布通知，push 与 30s 轮询并存期行为不一致是暂态——在 `docs/superpowers/plans/2026-09-16-netmap-push-and-endpoint-ui.md` 里列清剩余接入点。
3. agent 侧 `RefreshConfig` 每次全量应用，可像 `RunNetmapSync` 一样先比 `ConfigVersion` 再应用。
4. 补 push→refresh 的 agent 侧单测（publish 侧已有 `notify_test.go`）。

---

## 七、优先级行动清单

| 优先级 | 事项 |
|--------|------|
| P0 | relay 三件套：Register 鉴权（token/签名）、PayloadLen 上限、Session 写锁 |
| P0 | relay 重注册 Unregister 竞态（按会话身份删除） |
| P1 | relay 客户端自动重连（对齐 NATS `MaxReconnects(-1)` 姿势） |
| P1 | 验证/修复 `send6` IPv6 出站路径 |
| P1 | netmap 应用串行化 + netmap-changed 回调去抖 |
| P2 | 信令路径 OS 操作移出锁外、LRP 泄漏关闭、liveness 容忍瞬时错误、droppedCount 暴露为指标 |
| P2+ | 架构：抽 `internal/dataplane`，拆 `ai.go` / `peer_controller.go` 巨型文件 |
