# Lattice Cast 接收网关设计：协议层归 Lattice，Reflux 只负责播放

**日期**：2026-09-28
**状态**：Approved（用户 2026-09-28 确认采用网关方案；**先只做 Phase 0 spike**，结果出来后由用户决定是否进入 Phase 1+ 或走 §八 回退）
**涉及仓库**：`lattice-cast`（LatticeCastKit）、`lattice-apple`（Lattice iOS/macOS）、`reflux`（Reflux iOS/macOS）
**前提（用户已确认）**：用户设备上 **Lattice 与 Reflux 一定同时安装**。tvOS 不在本设计范围。

---

## 一、背景与问题

现状：`LatticeCastKit`（`lattice-cast/swift/LatticeCastKit`）是共享 Swift 库，Lattice 与 Reflux 各自把它编进 App，
**各自起一份**接收服务（HTTP `:7822` + mDNS `_latticecast._tcp`）并各自持有配对配置：

| | Lattice | Reflux |
|---|---|---|
| macOS | `LatticeMac/CastReceiver.swift`，播放用临时 AVPlayer（`CastSink.swift`） | `macOS/Resident/ReceiverMenuState.swift` 等 |
| iOS | 已删除（`lattice-apple` 工作区未提交改动，见 §九） | `Shared/LatticeCast/LatticeCastManager.swift`，App 启动时 `startIfNeeded` |

问题：
1. 协议层与播放层耦合在同一进程，两个 App 同机会抢 `:7822`，配对信息也要录两遍。
2. Lattice 是底座，"谁能被投屏、身份是谁、是否在线"应由底座回答；Reflux 应只关心"怎么把这个 URL 播好"。
3. iOS 后台会被挂起，接收服务放在会被挂起的 App 进程里，切到别的 App 后 `:7822` 即断
   （2026-09-28 实测：手机端多次 `Connection refused`）。

## 二、目标 / 非目标

**目标**
1. 协议层（身份、配对、鉴权、mDNS、五端点 + 状态机）只在 **Lattice** 里运行一份，称 **Cast Gateway（接收网关）**。
2. Reflux 只实现 **Cast Sink（播放端）**：把网关转来的 URL 交给自己的播放器，不再监听外部端口、不再发 mDNS、不再有配对 UI。
3. cast-agent 与线上协议 **零改动**（`docs/protocol.md` v1 五端点不动，契约夹具不动）。
4. 复用现有代码：网关 = 现有 `RendererServer` + 新的 `LoopbackSinkController`；Sink = 现有 `RendererServer` 绑回环地址。

**非目标**
- 不改 cast-agent（Go）与协议 v1。
- 不做 tvOS / Android（各自仍是独立渲染端）。
- 不做后台音频/画中画续播（Reflux 被挂起时不承诺能继续接收，见 §五）。
- 不引入新的播放内核（PlayerCore 计划另行推进，见 `2026-09-20-player-core-design.md`）。

## 三、总体架构

```
cast-agent (Go, 另一台机器/Mac)
   │  HTTP, Bearer <配对令牌>, POST /play {url,...}        ← 协议 v1，不变
   ▼
┌──────────────────────────── Lattice ────────────────────────────┐
│ Cast Gateway  (LatticeCastGateway)                              │
│  ├ 身份/配对：name / room / token（唯一一份，Lattice 保存）      │
│  ├ 对外 HTTP :7822（绑 0.0.0.0，overlay/局域网可达）+ mDNS 自报   │
│  ├ 协议状态机（RendererServer，原样复用）                        │
│  └ LoopbackSinkController ── 读 SinkStore 找到 sink ──┐          │
└──────────────────────────────────────────────────────│──────────┘
                                                        │ HTTP 127.0.0.1:<sinkPort>
                                                        │ Bearer <sink_secret>   （同 5 个命令，本机内）
                                                        ▼
┌──────────────────────────── Reflux ─────────────────────────────┐
│ Cast Sink  (LatticeCastSink)                                    │
│  ├ RendererServer 绑 127.0.0.1、不发 mDNS                        │
│  └ RendererBridge (现有) → PlayerController 播放                │
└─────────────────────────────────────────────────────────────────┘
```

**网关跑在哪个进程**

| 平台 | 网关宿主 | 原因 |
|---|---|---|
| macOS | Lattice.app 主进程（菜单栏常驻，替换现有 `CastReceiverManager`） | 常驻、非沙盒（`LatticeMac.entitlements` 无 sandbox 项） |
| iOS | **`LatticeTunnel` 隧道扩展进程**（`NEPacketTunnelProvider`） | 唯一在后台常驻的 Lattice 进程；主 App 会被挂起 |

> iOS 落在隧道扩展里是本设计**最大的不确定项**，必须先过 §七 的 Phase 0 spike，
> 不通过则回退方案见 §八。

## 四、组件设计

### 4.1 LatticeCastKit 新增（`lattice-cast/swift/LatticeCastKit`）

现有公共 API 不改（`LatticeCastRenderer` / `PlaybackController` / `LatticeCastProvisioning` 保持向后兼容，tvOS 与 Go 契约测试不受影响）。新增：

1. **`RendererServer` 绑定地址选项**
   Swifter 的 `HttpServer` 已有 `listenAddressIPv4`（`HttpServerIO.swift`）。给 `LatticeCastConfig`
   增加向后兼容字段（Codable 用 `decodeIfPresent`，缺省值等于现状行为）：
   - `bindAddress: String?`（缺省 nil = 全接口；Sink 传 `"127.0.0.1"`）
   - `announce: Bool`（缺省 true；Sink 传 false，不发 mDNS）
   `RendererServer.start()` 在 `server.start` 前设置 `server.listenAddressIPv4 = bindAddress`。

2. **`LatticeCastGateway`**（新文件）：
   - `init(config: LatticeCastConfig, sinkStore: SinkStore)`
   - `start() throws` / `stop()`：内部持有 `RendererServer(config, controller: LoopbackSinkController(sinkStore))` + `Announcer`
   - `var sinkOnline: Bool { get }`（供 Lattice UI 显示）

3. **`LoopbackSinkController: PlaybackController`**（新文件）：把六个方法翻译成对 sink 的 HTTP 调用
   （`URLSession`，**必须绕过系统代理**：`connectionProxyDictionary = [:]`，否则 Clash 等会把 127.0.0.1 请求代成 502）。
   行为约定见 §五。

4. **`SinkStore`**（新文件，协议 + 默认实现）：
   ```swift
   public struct SinkEndpoint: Codable, Equatable { public var port: Int; public var secret: String; public var pid: Int32; public var updatedAt: Date }
   public protocol SinkStore { func load() -> SinkEndpoint?; func save(_ e: SinkEndpoint); func clear() }
   ```
   默认实现 `FileSinkStore(directory:)`：单个 JSON 文件 `cast-sink.json`，原子写（`.atomic`），权限 0600。
   目录由宿主传入（iOS：App Group 容器；macOS：见 §4.5）。

5. **`LatticeCastSink`**（新文件）：
   - `init(controller: PlaybackController, sinkStore: SinkStore, secret: String)`
   - `start()`：起 `RendererServer(port: 0, bindAddress: "127.0.0.1", announce: false, token: secret)`，
     取实际端口写入 `SinkStore`；`stop()`：停服务并 `clear()`（仅当文件里的 pid 是自己时才清，防止误清新进程的记录）。

6. **共享密钥 `sink_secret`**：32 字节随机、hex；由 **Lattice 生成并保存在 SinkStore 所在共享容器内**
   （`sink_secret` 文件，0600），Reflux 只读。**不写入代码或日志。**

### 4.2 Lattice（`lattice-apple`）

- **iOS**
  - `LatticeTunnel/PacketTunnelProvider.swift`：`startTunnel` 成功后，若已配对且启用 → 起 `LatticeCastGateway`；`stopTunnel` 时停。
    配对配置经 App Group 的 `UserDefaults(suiteName: "group.io.lattice.shared")`（`LatticeCastProvisioning.loadConfig(defaults:)` 已支持注入 suite）。
    主 App 修改配对后经现有 `handleAppMessage` 通道发 `"castReload"` 通知扩展热重启网关。
  - `Lattice/`：新增 `CastGatewayPage`（设置 → 工具 → 投屏接收，**恢复入口但不再含播放**）：
    开关、设备名/房间/令牌展示与编辑、状态行（"Reflux 在线/未运行"、当前播放标题）。**不得 import PlayerKit。**
    （`project.yml` 里 iOS target 的 `PlayerKit`/`PlayerKitNative` 依赖已移除，保持移除；`LatticeCastKit` 依赖需重新加回 `Lattice` 与 `LatticeTunnel` 两个 target。）
  - 需要 Reflux 未运行时提醒用户：扩展内发本地通知（见 §五 与 Phase 0 第 3 项）。
- **macOS**
  - `LatticeMac/CastReceiver.swift`：`CastReceiverManager` 改为持有 `LatticeCastGateway`（不再传 `AVPlayerPlaybackController`）；`CastPage` UI 保留，新增"播放器在线"状态行。
  - `LatticeMac/CastSink.swift`（AVPlayer 临时 sink）：Phase 3 验证 Reflux macOS Sink 可用后删除。
  - 收到 /play 且无 Sink 时，`open reflux://` 拉起 Reflux（`NSWorkspace`），最多等 6s Sink 上线（见 §五）。
- 两端在 `Info.plist` 注册 URL scheme `lattice://`（当前没有），供 Reflux "打开 Lattice 配对页"使用：`lattice://cast`。

### 4.3 Reflux（`reflux/apple`）

- `Shared/LatticeCast/LatticeCastManager.swift`：**重写为 Sink 管理器**：
  - `startIfNeeded(playerController:)`：App 启动/回前台时读取 `sink_secret`（无则视为 Lattice 未安装/未配对，状态置 `latticeMissing`），
    起 `LatticeCastSink(controller: LatticeCastRendererBridge(...), ...)`。
  - 删除：`config`/`isEnabled`/`enabledKey`/`clearPairing`/`save`/mDNS 相关逻辑；保留 `agentURLKey`/`agentTokenKey`（语音入口用，与本设计无关）。
- `Shared/LatticeCast/RendererBridge.swift`：**不改**（PlaybackController 适配与 `latticeCastPlaybackStarted` 通知照旧）。
- `Shared/LatticeCast/LatticeCastProvisioningScreen.swift`：改为**只读状态页**：
  "投屏由 Lattice 接收"、网关状态、`打开 Lattice` 按钮（`lattice://cast`）；移除令牌/房间/端口编辑。
- 入口点 `RefluxAppleApp.swift`（iOS）与 `RefluxAppleMacApp.swift`：`.task` 与 `scenePhase == .active` 时调用 `startIfNeeded`（回前台重起 Sink，因为 iOS 挂起会杀死监听）。
- **entitlements / 共享容器**：
  - iOS：`RefluxApple.entitlements` 目前为空 `<dict/>`，需加入 App Group `group.io.lattice.shared`（两个 App 同一 Team `JN4AC4DDAU`；**需要用户在 Apple Developer 后台为 `io.reflux.apple` 启用该 App Group 并重新生成描述文件**）。
  - macOS：GLM 需先确认 Reflux macOS 是否启用 sandbox；使用 macOS 的 App Group 命名规则（团队前缀 `JN4AC4DDAU.io.lattice.shared` 或 `group.` 前缀，取决于签名方式），两端 entitlements 必须一致。
- Reflux 不再在 `:7822` 监听；`tvOS` 目标不变。

### 4.4 配对信息归属

配对配置（name / room / token）**只在 Lattice 保存与编辑**。cast-agent 的 `renderers.<key>` 仍按原方式配（key = 设备名，`token` = Lattice 里显示的令牌，host 用 Lattice 的 overlay IP）。

### 4.5 SinkStore 目录

| 平台 | 目录 | 备注 |
|---|---|---|
| iOS | `FileManager.containerURL(forSecurityApplicationGroupIdentifier: "group.io.lattice.shared")/cast/` | 隧道扩展、Lattice、Reflux 三方可读写 |
| macOS | 对应的 App Group 容器 `.../cast/` | 同上，见 §4.3 entitlements 说明 |

## 五、行为约定（网关 ↔ Sink）

**总预算**：cast-agent 的 HTTP 客户端超时是 **15s**（`lattice-cast/internal/cast/adapter/latticecast/client.go` `NewClient` 默认 `Timeout: 15s`）。
网关对一次 `/play` 的总耗时（拉起 + 等待 Sink + Sink 起播）必须 **≤ 14s**，否则 agent 会把它误报成 `device_offline`。

`LoopbackSinkController` 逐命令语义：

| 命令 | Sink 在线 | Sink 不在线（`SinkStore` 无记录 / 连接被拒 / 超时） |
|---|---|---|
| `load` (`/play`) | 转发；HTTP 超时 = 14s − 已耗时；Sink 返回的 `error` 原样抛出（如 `源无法播放`） | **macOS**：`open reflux://` 拉起，轮询 `SinkStore` 最多 6s；仍无 → 抛 `player_not_running`。**iOS**：立即抛 `player_not_running`，并发本地通知"点击打开 Reflux 接收投屏"（`reflux://` 深链）。**不做排队重放**：用户打开 Reflux 后重新投一次。 |
| `pause` / `stop` / `seek` / `volume` | 转发 | 视为幂等空操作，返回成功（协议：不适用状态下幂等） |
| `status()` | 转发 `/status`，映射为 `Status` | 返回 `Status(state: "idle")`（网关状态机据此迁到 idle，不谎报 playing） |

- **超时补偿**：网关对 Sink 的 `load` 调用超时后，必须再发一次 `/stop` 给 Sink，防止 Sink 稍后自行起播造成状态错配。
- **`player_not_running`** 是新增的 `error` 字段取值（应用层字符串），协议 v1 允许自由字符串，**契约夹具不需要改**；请在 `lattice-cast/docs/protocol.md` 的错误说明处补一行文档。
- **Sink 生命周期**：Reflux 进后台被挂起 → 监听消失 → 网关下次调用连接被拒 → 按"不在线"处理。Reflux 回前台重起 Sink 并重写 `SinkStore`。
  Reflux 挂起后仍在播的内容（若有后台音频）网关 `status()` 会返回 idle，属已知限制（非目标）。
- **安全**：Sink 只绑 `127.0.0.1` + Bearer `sink_secret`；`SinkStore` 与 `sink_secret` 文件权限 0600；
  网关向外仍是协议 v1 的 Bearer 配对令牌。媒体 URL 里可能带 Reflux 的 `api_key`，**日志里必须脱敏**（不得打印完整 URL 的 query）。

## 六、错误与边界情况

1. Lattice 未连接隧道：iOS 网关随隧道生命周期起停，隧道断开则不可达——预期行为，UI 状态行提示"隧道未连接"。
2. Lattice 未配对：网关不启动；Reflux 状态页显示"请先在 Lattice 配对"。
3. `SinkStore` 里有陈旧记录（Reflux 崩溃）：连接被拒即判不在线；`pid` 用于 Sink 停止时防误清。
4. 两个 Reflux 实例（iOS 不会，macOS 可能）：后启动者覆盖 `SinkStore`。
5. macOS 上旧版 Reflux 仍监听 `:7822`：新 Lattice 网关 `start()` 会因端口占用失败，UI 展示 `startError`；升级说明里要求 Reflux 与 Lattice 同版本升级（见 §十）。
6. 端口：网关固定 `7822`（对外）；Sink 用临时端口（`port: 0`）。

## 七、实施阶段

### Phase 0 — iOS 可行性 spike（**先做，不通过则不进 Phase 1 的 iOS 部分**）
在 `lattice-apple` 的 `LatticeTunnel` 扩展里加一个最小 Swifter 服务（仅 `GET /ping`），逐项验证并把结果写进 `docs/superpowers/specs/` 下的 spike 记录：

1. **可达性**：扩展内监听的端口，能否从另一台设备经 overlay IP（如 `10.96.0.4:7822`）访问；手机锁屏 / Lattice 与 Reflux 都在后台时是否仍可访问（持续 10 分钟）。
2. **内存**：iOS 扩展内存上限内，Go 引擎 + Swifter 常驻的峰值内存（Instruments 或 `os_proc_available_memory()` 日志），需低于上限 80%。
3. **跨进程**：Reflux（前台）能否连上扩展进程的 `127.0.0.1:<port>`；扩展能否发本地通知（`UNUserNotificationCenter`，主 App 已获授权）并深链打开 Reflux。
4. **App Group**：三方进程能否读写同一 `cast-sink.json`。

### Phase 1 — LatticeCastKit（lattice-cast）
实现 §4.1，含单元测试：
- `bindAddress` 生效（绑 127.0.0.1 后外部地址连不上）；旧配置 JSON（无新字段）解码不变；现有契约测试全绿。
- `LoopbackSinkController` 对 §五 表格逐行的测试（用 fake sink server），含超时补偿 `/stop`。
- `FileSinkStore` 原子写、pid 防误清。

### Phase 2 — Reflux Sink（reflux）
实现 §4.3；回归清单：本地文件播放不受影响；`RendererBridge` 原有单测通过；Sink 启停不残留监听（`lsof -i :端口`）。

### Phase 3 — Lattice 网关（lattice-apple）
- macOS 先做：§4.2 macOS 部分；验证 Reflux macOS 作为 Sink 后删除 `CastSink.swift`。
- iOS：Phase 0 通过后实现 §4.2 iOS 部分。

### Phase 4 — 端到端验收
按下方 §十一 执行，记录写入 `lattice-cast/docs/e2e-checklist.md`（新增一节）。

## 八、回退方案（Phase 0 任一关键项失败时）

- **回退 A（iOS 网关放 Lattice 主 App 进程）**：仅当 Lattice App 在前台时可接收。体验差，仅作为过渡；文档需写明限制。
- **回退 B（iOS 保持协议层在 Reflux）**：iOS 上 Reflux 继续持有 `LatticeCastRenderer`（现状），macOS 仍按本设计做网关。此时 iOS 的 Lattice 只提供只读引导页。

选择哪个回退由用户在看到 Phase 0 结果后决定，GLM 不要自行决定。

## 九、当前工作区状态（实现前需知道）

`lattice-apple` 工作区有**未提交**的改动（本次会话产生，用于"iOS 不在 Lattice 里播放"的初步尝试）：
- 删除 `apple/Lattice/CastReceiverPage.swift`；
- `apple/Lattice/SettingsView.swift`：去掉"投屏接收"入口；
- `apple/project.yml`：去掉 iOS target 对 `LatticeCastKit`/`PlayerKit`/`PlayerKitNative` 的依赖与 `PlayerKit` 包声明；
- 重新生成了 `LatticeApple.xcodeproj/project.pbxproj`，`README.md` 去掉 PlayerKit 目录说明。

这些与本设计**方向一致**（Lattice iOS 不再依赖 PlayerKit）。实现时在此基础上：把 `LatticeCastKit` 依赖加回 `Lattice` 与 `LatticeTunnel` 两个 target，并新增 `CastGatewayPage`。**不要恢复 PlayerKit 依赖。**

## 十、迁移与兼容

- 三个仓库必须**同一版本**发布（沿用 `lattice-apple/docs/RELEASING.md` 的 lockstep 流程）；`LatticeCastKit` 通过本地 SwiftPM 路径引用（`../../lattice-cast/swift/LatticeCastKit`），需三仓同机 checkout。
- 升级后用户在 Reflux 里的旧配对配置作废：首次启动 Lattice 网关时，若 Lattice 无配对而 Reflux 旧 `latticecast.renderer.config` 存在，可**一次性迁移**该配置到 Lattice 的配对存储（GLM 可选实现；不实现则需要在 Lattice 里重新配对并更新 cast-agent 配置的 token）。

## 十一、验收清单

在同一个 Lattice overlay 网络里，Mac 上跑 cast-agent（使用 `lattice-cast/scripts/cast.py`，配置见 `config.local.yaml`；启动 agent 时设 `NO_PROXY=10.96.0.0/16`，否则代理会干扰）：

1. **iOS，Reflux 在前台**：`cast.py devices` 显示 `online`；`play` 一个 1080p H.264 MP4 → 手机 Reflux 出画面出声；`status` 位置前进；`pause`/`seek`/`volume`/`stop` 均生效。
2. **iOS，Reflux 未运行**：`play` 快速返回 `player_not_running`（<2s），手机收到本地通知；点通知打开 Reflux，再次 `play` 成功。
3. **iOS，Reflux 在后台/锁屏 5 分钟后**：`devices` 仍显示网关在线（Sink 不在线则 `player_not_running`，而不是 `device_offline`）。
4. **macOS**：Reflux 未运行时 `play` → 自动拉起 Reflux 并在 14s 内起播；Reflux 已运行则直接起播。
5. **端口**：同机同时装 Lattice 与 Reflux，`lsof -i :7822` 只有 Lattice；Reflux 无外部监听端口。
6. **鉴权**：无 token / 错 token 访问 `:7822` 返回 401；不带 `sink_secret` 访问 Sink 端口返回 401；从局域网其他机器无法连到 Sink 端口。
7. **协议回归**：`lattice-cast` 的 Go 侧与 Swift 侧契约测试全绿；`docs/protocol.md` 五端点行为不变（仅新增 `player_not_running` 说明）。
8. **Reflux 单独运行**（未装/未运行 Lattice）：本地播放完全正常，投屏状态页显示"请先安装并配对 Lattice"，无崩溃。

## 十二、开放问题（需用户拍板，GLM 遇到时停下来问）

1. Lattice macOS 没有 Reflux 时是否保留 AVPlayer 兜底（当前设计：不保留，直接报 `player_not_running`）。
2. 旧配对配置一键迁移是否要做（§十）。
3. iOS Phase 0 失败后选回退 A 还是 B（§八）。
4. Reflux macOS 的 sandbox 状态与 App Group 命名方式（§4.3，需 GLM 先查实再定）。

---

## 十三、NATS 推送信令（2026-09-28 Phase 0 结论后用户拍板的新方向）

**背景**：Phase 0（结果见 `2026-09-28-cast-gateway-phase0-spike.md`）实测：指令通道若放在
「监听端口」上，iOS 上不可行——隧道扩展绑定的端口入站被系统静默丢弃（overlay 与本机回环
一致，指向本地网络隐私权限默认拒绝且无法触发授权弹窗）；Reflux App 监听则受后台挂起限制
（§一 旧问题）。**用户 2026-09-28 拍板：指令传输改为 NATS 推送，渲染端零监听端口**（即本
文档 §八 未列的「回退 C」，打破「cast-agent 与协议零改动」目标——协议五端点语义不变，新增
一个传输绑定）。

**架构（2026-09-28 用户定稿：双传输，主 = 引擎内截获 overlay 命令包，兜底 = NATS 推送）**：

```
cast-agent ──HTTP 协议 v1（不变）──▶ 渲染网关/桥（Mac 侧，Phase 3 的 Gateway 兼任）
                                        │ 按可达性选择传输
                 ┌──────────────────────┴──────────────────────┐
                 ▼ 主路：overlay 命令包                          ▼ 兜底：NATS 推送
   发往手机 overlay IP 的保留 UDP 端口，            lattice.cast.<peerid>.cmd
   手机引擎用户态截获（LatticeDNS 同款，            （既有出站 NATS 会话接收，零监听；
   不交给 OS 栈 → 无监听 socket，                    NATS 服务器不可达/桌面渲染端时用 HTTP）
   本地网络权限无从拦截）
                 └──────────────────────┬──────────────────────┘
                                        ▼ 引擎 → delegate 事件 "cast: {json}" → 扩展
                        App Group 待执行命令 + 本地通知
                                        ▼
                    通知点击 → Lattice 主 App → reflux://cast?url=..&title=.. → Reflux 播放
```

- 媒体流不变：仍是渲染端自行拉取 URL；两个传输只承载指令（play/pause/seek/stop/status 语义
  与 §五 表格一致）。
- 双通道去重与可靠性：命令带 id/ts；主路 UDP 应用层 ACK + 重试（at-least-once），兜底 NATS
  核心语义为 fire-and-forget（离线补投需 JetStream，产品语义待定）；接收端按命令 id 去重、
  按时间戳保序。
- 信任模型：主路包在 WireGuard 隧道内，引擎可校验来源 peer；兜底 NATS 为匿名连接，主题级
  授权在 Phase 1' 补。
- macOS 渲染端（Reflux Mac / tvOS）仍走 HTTP 监听（桌面无此限制），传输绑定按平台选择。

**已实现（Phase 0b spike，`spike/cast-gateway-phase0` 等）**：

- 引擎：`internal/agent` NodeConfig 新增 `CastCommandHandler`，node 订阅
  `lattice.cast.<peerid>.cmd`（`SubscribeRawPayload`），apple/engine 把载荷以 `cast: {json}`
  经既有 `EngineDelegate.OnEvent` 抛给 Swift（gomobile 委托协议零改动）。
- 发布端：`cmd/castcmd`（测试工具，从设备公钥推导主题，发布 {action,url,title,ts}）。
- iOS 扩展：收令 → 写 App Group `cast/cast-pending.json` + 本地通知（带 url/title）；
  Lattice 主 App：通知点击 → 深链透传；Reflux：onOpenURL 弹窗确证。

**Phase 0b 待验证的两个关键点**：

1. 锁屏 ≥30 分钟期间，NATS 指令是否仍可达（整个方向押在这上面；注意 Phase 0 已观察到
   锁屏 ~10 分钟后 overlay 数据面断过一次，NATS 走物理网络、独立于 overlay，理论更稳，须实测）。
2. 扩展 → 通知 → 主 App → 深链 → Reflux 全链（人工点击环节 + Reflux 弹窗确证）。

**后续（转 Phase 1'）**：`lattice-cast` 定义 NATS binding（五端点语义、`player_not_running`
的返回通道——命令式信令下改为「状态查询走 NATS 请求-响应」或渲染端状态经 App Group 暴露）、
Mac 网关桥、Reflux/Lattice 收令与播放集成、安全（主题授权、指令来源校验）。

**已知取舍**：指令路径依赖管理面可用性（管理面挂则投屏挂，即使双方同局域网）。原生演进方向
是引擎内截获 overlay 命令包（LatticeDNS 同款模式，用户态收，不依赖任何监听 socket 也不依赖
管理面），待 NATS 版验证价值后再立项。
