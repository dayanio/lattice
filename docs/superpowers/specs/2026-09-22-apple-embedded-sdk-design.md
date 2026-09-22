# Apple 嵌入式 SDK 设计 — lattice-shim tsnet 化 + Apple 无 NE 接入

**日期**：2026-09-22
**状态**：设计已评审，待写实现计划
**范围**：`lattice-shim`（新增 `shim.Server`）、`lattice` 主仓库 `apple/engine/embedded/`（新增）、新建 Swift Package（分发壳）
**关联文档**：
- [gVisor netstack 立项：Apple 端用户态协议栈](./2026-09-22-netstack-design.md) —— 已归档，本设计是其后续：实测推翻"扩展内终结连接"这个前提后，找到的另一个真实需求
- [lattice-copilot v1 设计](./2026-09-22-lattice-copilot-design.md) —— 家庭生态里未来内嵌式 App（文件同步/照片同步类工具）是本设计的第一批消费者

---

## 一、背景与动机

同日（2026-09-22）已经发生的事：`docs/superpowers/specs/2026-09-22-netstack-design.md` 原本假设"Apple `NEPacketTunnelProvider` 扩展进程无法终结 TCP/UDP 连接，必须引入 gVisor netstack 才能做监听"。当天用真实环境实测（本地 `mac-demo` workspace 的 3 个测试节点 + 云端 `cloud-node-1`）证明这个前提不成立：**一个完全独立于 NE 扩展的普通进程，直接在 overlay IP 上 `net.Listen()`，能正常收到对端主动发起的连接**——ping 和 TCP connect 都通过真实、当下连通的 peer 验证过。该项目因此归档。

但讨论延伸出一个不同的真实需求：**不经过 `NEPacketTunnelProvider`、不触发系统 VPN 授权弹窗、不在系统"VPN"列表里留记录，把 overlay 连接能力当一个库嵌入到其他 App 里**——类似 Tailscale 的 `tsnet`。这和归档项目的动机完全不同：

| | 归档的 netstack 项目 | 本设计 |
|---|---|---|
| 要解决的问题 | 扩展进程能不能终结连接 | 能不能不用 NE、不弹系统授权、纯用户态跑 overlay |
| 实测结论 | 前提不成立（不需要 gVisor） | 未验证，是全新需求 |
| gVisor 的作用 | 想用来"接住"NE 给的包 | 用来在没有内核 TUN 权限的情况下自己实现完整协议栈 |

代价已在会话里讨论清楚并被接受：这条线拿不到系统级流量拦截（只有内嵌了这段代码的进程自己能连 overlay），iOS 后台大概率保不住连接（没有 NE 撑着后台常驻）。这条线不是要替代现有 NE 版本 App，是给"某个具体功能需要内嵌 overlay 连接能力，但不想承担 VPN 授权/App 形态"的场景单独开一条路。

## 二、目标 / 非目标

**目标**

1. `lattice-shim` 新增一个 tsnet 风格的顶层类型 `shim.Server`：零权限（不建内核 TUN）、内嵌 gVisor netstack，直接暴露 `Dial`/`Listen`（标准 `net.Conn`/`net.Listener`），不经过本地转发/代理。
2. `lattice` 主仓库新增 `apple/engine/embedded/`：复用现有 `apple/engine` 的 NATS 注册、ICE/LRP 建连逻辑，实现 `shim.PeerManager` 接口，把 `shim.Server` 接进 Lattice 的控制面。
3. 提供 gomobile 导出层 + Swift Package，让 Apple 端的宿主 App 可以像加一个普通依赖一样嵌入 overlay 连接能力。
4. 第一个消费者是 Lattice 自己生态里未来的内嵌式 App（例如文件/照片同步类工具），不对外部第三方开放，因此不承诺公开 API 稳定性。

**非目标**

- 不做系统级流量拦截（那是现有 NE 版本 App 的职责，两条线并存，互不替代）。
- 不在这一期做 iOS 后台保活的特殊处理，行为限制写进文档即可。
- 不改动 `shim.Sandbox`/`ForwardListener`/`Socks5Server`——它们服务的是不同场景（转发/代理），`Server` 与它们平级新增，不是替换。
- 不承诺对外部第三方开发者的公开 SDK 契约（第一期是内部消费）。

## 三、架构

> **修正（2026-09-22，M1 研究阶段）**：读 `apple/engine` 和 `internal/agent` 现有代码后发现，本节最初设计的 `ApplePeerManager`（实现 `shim.PeerManager`）不需要写。`internal/agent.NodeConfig` 早就有 `CustomTUN`/`ProvisionerFactory` 这个扩展点，doc comment 原话："Used by the agent sandbox, which runs WireGuard over gVisor instead of a kernel TUN"——这正是 Linux AgentSandbox 用来接 gVisor 的同一个口子。配套的桥接代码 `internal/agent/gvisor.NewTUNAdapter`/`InjectIntoChannel`/`NewSandboxProvisionerFactory` 都已经是导出的、现成能跑的。Apple 嵌入式引擎复用 `latticeagent.NewNode` 本身（跟现有 NE 版本 `engine.go` 用的是同一个函数），WireGuard peer 生命周期完全由 `NewNode` 内部通过 `Provisioner` 管理，不需要 `shim.PeerManager` 这层。`shim.Server` 构造时 `PeerManager` 传 `nil` 即可。

```
lattice-shim（外部库，零 Lattice 依赖，已完成，见 M0）
  shim/server.go
  └─ Server：Netstack + PeerManager（Apple 场景传 nil，不用），直接暴露 Dial/Listen
       NewServer(overlayIP string, pm PeerManager, opts ...NetstackOption) (*Server, error)
       (s *Server) Dial(ctx, network, addr string) (net.Conn, error)
       (s *Server) Listen(network, addr string) (net.Listener, error)
       (s *Server) Channel() *channel.Endpoint

lattice（主仓库）
  apple/engine/embedded/  ← 新增
  ├─ engine.go       EmbeddedEngine：复用 latticeagent.RegisterSandboxViaNATSNotify +
  │                   latticeagent.NewNode（跟 apple/engine/engine.go 的 run() 同一套调用），
  │                   CustomTUN 换成 gvisor.NewTUNAdapter(server.Channel(), ...)，
  │                   ProvisionerFactory 直接用现成的 gvisor.NewSandboxProvisionerFactory
  └─ gomobile.go     gomobile 导出层：EmbeddedConn / EmbeddedListener 包
                      net.Conn / net.Listener（gomobile 不能直接导出 Go 接口）

新建 Swift Package（薄壳，包 gomobile 产物的 .xcframework）
```

`apple/engine` 现有的 NE 模式（`packet_tun.go` + `engine.go` 的 `run()`）完全不动，`embedded/` 是平行的新增目录，只换 `CustomTUN`/`ProvisionerFactory` 这两个参数，`internal/agent` 里的 NATS 注册、netmap 收敛、ICE/LRP 建连代码一字不改直接复用。

## 四、组件设计

### 4.1 `lattice-shim`：`shim.Server`（已完成，见 M0 实现计划）

职责边界：不知道 wireguard-go 内部实现、不知道 NATS、不知道 Lattice CRD——`PeerManager` 接口由调用方注入（可为 nil），`Server` 只负责把 `Netstack` 和 `PeerManager` 粘在一起，直接暴露 `Dial`/`Listen`，不经过 `ForwardListener`/`Socks5Server` 那层中继。Apple 嵌入式场景不用 `PeerManager`（见上方修正说明），只用 `Dial`/`Listen`/`Channel`。

### 4.2 `lattice` 主仓库：`apple/engine/embedded/`

- **`EmbeddedEngine`**：新增类型，`Start`/`Dial`/`Listen`/`Close` 方法。`Start` 内部依次调用：
  1. `latticeagent.RegisterSandboxViaNATSNotify(ctx, serverURL, token, name, privKey, onPending)` 拿到 `*infra.Peer`（跟 `apple/engine/engine.go` 的 `run()` 一样）
  2. `shim.NewServer(overlayIP, nil)` 创建 netstack
  3. `gvisor.NewTUNAdapter(server.Channel(), gvisor.InjectIntoChannel(server.Channel()))` 构造 `tun.Device`（`internal/agent/gvisor/wg_device.go` 里现成的，不改）
  4. `latticeagent.NewNode(ctx, &latticeagent.NodeConfig{CustomTUN: tunDevice, CustomName: "lattice-embedded", CurrentPeer: peer, ProvisionerFactory: gvisor.NewSandboxProvisionerFactory(overlayIP, "lattice-embedded"), ...})`（`internal/agent/gvisor/provisioner.go` 里现成的 factory，不改）
  5. `node.Start(ctx)`
  `Dial`/`Listen` 直接透传给 `shim.Server`。
- **`gomobile.go`**：`EmbeddedConn`（`Read([]byte) (int, error)` / `Write([]byte) (int, error)`）、`EmbeddedListener`（`Accept() (*EmbeddedConn, error)`），各自内部持有真实的 `net.Conn`/`net.Listener`，把 gomobile 不能直接跨语言导出的 Go 接口包成结构体方法。

### 4.3 Swift Package

包 gomobile 产物的 `.xcframework`，Swift 侧再包一层符合 Swift 习惯的 API（具体风格——delegate、callback 还是 async/await——留到消费者 App 定下来的时候再定，第一期不需要预先设计）。

## 五、数据流

- **启动**：宿主 App 调 `EmbeddedEngine.Start(config)` → `RegisterSandboxViaNATSNotify` 拿到 overlay IP → `shim.NewServer` + `gvisor.NewTUNAdapter` + `latticeagent.NewNode`（用 `gvisor.NewSandboxProvisionerFactory` 作 Provisioner）→ `node.Start(ctx)`——netmap 订阅、ICE/LRP peer 发现与建连、WireGuard 握手全部由 `NewNode` 内部按现有逻辑跑，peer 变化时 `NewNode` 自己通过 `Provisioner.AddPeer`/`RemovePeer` 调 `wireguard-go` 的 `IpcSet`,`apple/engine/embedded` 不用感知这个过程。
- **出站**：宿主 App 调 `Dial("tcp", "10.96.0.x:port")` → gomobile 壳转给 `shim.Server.Dial(...)` → gVisor 构造 SYN，经 `channel.Endpoint` → `gvisor.NewTUNAdapter` 的 `Read` 把包交给 wireguard-go → 加密 → 走 ICE/LRP 传输发出去。
- **入站**：宿主 App 调 `Listen("tcp", ":port")` → `shim.Server.Listen(...)` 在 netstack 里注册监听 → 对端连接进来的包被 wireguard-go 解密后经 `gvisor.InjectIntoChannel` 注入 `channel.Endpoint` → 三次握手在 gVisor 内完成 → `Accept()` 返回的连接包成 `EmbeddedConn` 交给宿主 App 自己读写。

## 六、错误处理与状态

- 没有 NE：没有系统 VPN 图标、没有系统级连接状态。`EmbeddedEngine.Status()` 复用 `apple/engine` 已有的 peer 连接状态查询（跟 `lattice status` 背后同一套），供宿主 App 自己决定怎么展示。
- iOS 后台限制：不做特殊处理，行为限制在 SDK 文档里明确写清楚——宿主 App 切后台后应预期连接会断，回到前台自行重连。
- `shim.Server.Dial`/`Listen` 的 error 原样透传到 gomobile 壳，不额外包装分类。

## 七、里程碑

| 里程碑 | 内容 | 验收 |
|---|---|---|
| **M0** | `lattice-shim` 加 `shim.Server`，纯 Go 单测（`go test ./shim`）——**已完成** | Dial/Listen 在本地两个 `Netstack` 实例之间打通（不涉及 Apple/gomobile） |
| **M1** | `apple/engine/embedded` 骨架（`EmbeddedEngine`，复用 `latticeagent.NewNode`/`gvisor.NewTUNAdapter`/`gvisor.NewSandboxProvisionerFactory`，不写 `ApplePeerManager`），先在 macOS 上（非 gomobile，普通 Go test）验证接线正确 | 对着今天已搭好的 `mac-demo` workspace + node-a/b/gateway 容器，Dial/Listen 都能连通 |
| **M2** | gomobile bind 验证（第一个真正未知的风险点） | `lattice-shim` + `apple/engine/embedded` 能被 `gomobile bind` 编译出 iOS+macOS 产物，不需要跑通功能，先确认编译链路通 |
| **M3** | Swift Package 封装 + 端到端验证 | 一个最小 Swift 测试壳，Dial 一边、Listen 一边，两个方向收发数据确认 |

## 八、测试策略

- `lattice-shim` 侧：`shim.Server` 的单元测试跟现有 `shim` 包测试风格一致，两个 in-process `Netstack` 互相 Dial/Listen，不需要真实 WireGuard 网络。
- `apple/engine/embedded` 侧：复用今天已经验证过的本地测试环境（`latticed --standalone` + `mac-node-a`/`mac-node-b`/`lattice-gateway` 三个容器），不需要碰云端 101.36.119.12。
- gomobile 编译验证是独立的、必须做的一步，但只是编译产物验证，不额外设计功能测试——功能正确性已经在纯 Go 层验证过。

## 九、风险

1. **gomobile 交叉编译未验证**：`lattice-shim` 从未在 gomobile/iOS 环境下编译过，M2 是第一次验证，可能遇到未知的交叉编译问题（比如 gVisor 某些包对 CGO 或平台特定代码的依赖）。
2. **两仓库协同**：`lattice-shim` 需要先发新版本（带 `Server`），主仓库 `go.mod` 再升级依赖——顺序上有硬性先后关系，`lattice-shim` 那部分要先合并。
3. **iOS 后台存活**：已知限制，非本设计要解决的问题，但要在 SDK 文档里明确写清楚，避免消费者 App 踩坑。
