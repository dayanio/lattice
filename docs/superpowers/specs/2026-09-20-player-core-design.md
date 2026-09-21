# PlayerCore 共享播放内核设计

**日期**：2026-09-20
**状态**：Approved（下一个 PR 实施）
**范围**：`reflux/apple`（消费方迁移）+ `apple/`（Lattice 接收端接入）+ 新 SwiftPM 包
**关联文档**：[LatticeCast v2 设计](./2026-09-18-latticecast-design.md)、[Apple 客户端设计](./2026-09-13-apple-clients-design.md)、[晶格 UI](./2026-09-13-apple-client-ui-mockups.md)
**前置条件**：当前修复批次（密钥身份/静态端点/CPU 唤醒/框架自愈）先行合并

---

## 一、背景与动机

当前播放能力存在**三套并行实现**，同一批问题（格式覆盖、音画同步、缓冲策略）要在多处修：

| 实现 | 位置 | 技术栈 | 使用方 |
|---|---|---|---|
| **PlayerKit / NativeBackend** | `reflux/apple/SharedUI/Player/` + PlayerKit | FFmpeg 系（`avformat_open_input`、reader、NativeBackend） | Reflux 播放器（约 5300 行 UI 依赖它） |
| **AVPlayerPlaybackController** | `lattice/apple/LatticeMac/CastSink.swift` | AVPlayer（系统，格式受限） | Lattice 投屏接收端（临时实现） |
| **mpv 渲染端** | `lattice-cast` Go renderer（`--input-ipc-server` 驱动 mpv） | mpv + FFmpeg | latticecast Go 渲染端 |

此外投屏接收端已迁入 Lattice 客户端（2026-09-15 决议：接收是节点能力，媒体与接收解耦），Lattice 用的是临时 AVPlayer sink——格式受限（无 MKV/ASS），长期不可接受。

**动机**：把"解码 + 状态机 + 渲染"收敛为一个共享内核包，Reflux 与 Lattice 都只做壳。修复一次、两端受益；新后端（AV1、HDR）一次接入。

## 二、目标 / 非目标

**目标**
1. 单一 `PlayerCore` SwiftPM 包：协议层 + 可切换后端 + 渲染视图
2. Reflux 播放回归零回退（迁移后行为逐项对齐）
3. Lattice 投屏接收端获得全格式播放能力（经由内核后端）
4. 后端可插拔：AVFoundation（轻）与 FFmpeg/MPV（全格式）并存，按宿主选择

**非目标**
- 不做播放列表/媒体库/字幕样式编辑（留在 Reflux UI 层）
- 不做跨平台（Windows/Android 播放内核）——仅 Apple
- 不改 LatticeCastKit 线协议（`PlaybackController` 六命令与五端点不动）

## 三、总体架构

```
PlayerCore（SwiftPM 包）
├─ 协议层
│   ├─ PlaybackEngine（后端实现的最小协议）
│   ├─ PlayerCore（门面：状态机 + 命令排队 + AsyncStream<PlaybackStatus>）
│   └─ PlayerSurfaceView（渲染视图，后端注入 render surface）
├─ 后端
│   ├─ AVFoundationBackend（Phase A：系统播放器，零依赖）
│   └─ FFmpegBackend（Phase C：libmpv/FFmpeg，全格式）
└─（无媒体业务、无库、无 UI Chrome）

消费方（壳）
├─ Reflux：PlayerScreen UI → PlayerCore（FFmpeg 后端）
└─ Lattice：投屏接收窗 → PlayerCore（AVFoundation 后端）
```

分界原则：**内核拥有"传输 + 解码 + 状态机"，壳拥有"业务 + UI"**。Reflux 特有的
ScanCoordinator、auto-enrich、prepareForReuse 复用路径留在 Reflux 侧，以回调
（`willLoad`/`didStop` 钩子）注入内核，不进包。

## 四、包形态与仓库位置

**决策**：`lattice-cast/swift/PlayerCore`（与 LatticeCastKit 同仓相邻）。

理由：
- Reflux 与 Lattice 均已用**本地 SwiftPM 路径**引用 `../../lattice-cast/swift/LatticeCastKit`，
  新增 `PlayerCore` 是零摩擦复制该模式
- 与 cast 渲染同属"Apple 渲染/播放基础设施"主题
- 若未来内核独立壮大（被第三个 App 引用），提升为独立仓库即可（包内容不依赖仓库名）

注意：本地路径包要求两个仓库同机 clone（现状即如此，README 记录）。

## 五、协议设计

### 5.1 最小引擎协议（Lattice 接收端只依赖这一层）

```swift
public protocol PlaybackEngine: AnyObject {
    func load(url: URL, title: String?, resumePositionMS: Int64) async throws
    func play() async
    func pause() async
    func stop() async
    func seek(positionMS: Int64) async
    func setVolume(_ level: Double) async          // 0.0 - 1.0
    func attachSurface(_ surface: PlayerSurface) async
    var statusStream: AsyncStream<PlaybackStatus> { get }
}
```

- **异步语义**：全 async——FFmpeg 开流是后台线程阻塞操作（现状
  `avformat_open_input`），async 让调用方决定是否限时等待（RendererBridge
  现有的 15s 有界等待迁移为调用方逻辑）
- **状态机**：`idle → loading → playing ⇄ paused → (eof) idle`；`error` 为旁路
  状态（sticky，load 成功即清除）——与 LatticeCastKit /status 的粘滞语义一致
- **`PlaybackStatus`**：state、positionMS、durationMS、bufferedMS、title、error

### 5.2 扩展协议（仅 Reflux 消费，Reflux 侧定义）

音轨/字幕轨选择、播放列表、倍速、音频设备——以 `PlaybackEngine` 的
`underlyingPlayer`（后端特化对象，`Any?`）或独立扩展协议提供，不进最小协议。

### 5.3 渲染 Surface

```swift
public protocol PlayerSurface: AnyObject {
    func attach(_ layer: CALayer)   // AVPlayerLayer 或 mpv Metal 层
    func detach()
}
```

`PlayerSurfaceView`（SwiftUI）持有 surface；mpv 后端用 render API 直绘 Metal；
AVFoundation 后端用 `AVPlayerLayer`。两后端的 surface 实现不同，视图协议相同。

### 5.4 与 LatticeCastKit 的关系

`PlaybackController`（六命令 + Status）**保留**，成为内核之上的适配器协议：

```
LatticeCastKit.PlaybackController ← CastReceiver 的内核适配器 → PlayerCore
```

即投屏协议五端点不动；Lattice 侧把 `AVPlayerPlaybackController` 替换为
`PlayerCoreController`（实现同一 PlaybackController，内部驱动 PlayerCore）。

## 六、后端与许可

| 后端 | 格式 | 许可 | 用途 |
|---|---|---|---|
| AVFoundationBackend | MP4/HLS/HEVC-in-MP4（系统能力） | 无（系统框架） | Lattice 接收端；Reflux 轻量模式 |
| FFmpegBackend（Phase C） | 全格式（MKV/ASS/AV1） | LGPL 动态链接 | Reflux 全格式模式 |

**许可约束**：FFmpeg/libmpv 以 LGPL 构建并动态链接（独立 framework，可替换），
不静态链入二进制；macOS 不走 Mac App Store 分发（现状即非商店），无额外限制。
若未来上架商店，砍掉 FFmpeg 后端、退 AVFoundation 即可（架构已支持）。

## 七、两侧接入

### 7.1 Reflux 迁移（Phase A，回归成本主体）

1. `PlayerKit` 内部改为门面：`PlayerController` 方法转调 `PlayerCore`
   （NativeBackend 的 avformat/reader 逻辑平移进 FFmpegBackend——Phase C 前先以
   AVFoundation 后端并行灰度）
2. ScanCoordinator / auto-enrich / prepareForReuse 以 willLoad/didStop 钩子注入
3. 回归清单（逐项）：起播（含续播位置）、seek 精度、EOF→idle、音量映射
   （0-100 线性→0.0-1.0）、错误路径（source_unreachable）、缓冲条、字幕/音轨
4. LatticeCastRendererBridge **不改**：它面向 PlaybackController 协议，
   底层换内核对桥透明

### 7.2 Lattice 接收端（Phase B）

1. `AVPlayerPlaybackController` → `PlayerCoreController`（实现
   LatticeCastKit.PlaybackController，内部驱动 PlayerCore/AVFoundation 后端）
2. 接收窗 = `PlayerSurfaceView` 全屏窗（替换 AVPlayerView）
3. 面板"投屏接收"区增加状态行：播放中标题 + 位置（来自 statusStream）

## 八、线程与状态模型

- 引擎内部：命令串行队列（对齐 mpv 单线程命令语义）；状态发布
  `AsyncStream<PlaybackStatus>`（多播）
- 壳层 UI：SwiftUI 订阅 statusStream；命令调用任意线程
- RendererServer 的 stateQueue 调用适配器 → 适配器 async 等待内核命令完成
  （有界超时沿用现状 15s）
- 禁止轮询：进度一律 AsyncStream/timer 驱动（吸取隧道 2ms 轮询被内核
  CPU-wake 击杀的教训）

## 九、测试与回归

- **内核契约测试**：同一套用例参数化跑两个后端（load/seek 精度/EOF/eject/
  损坏源错误透传）
- **投屏契约测试**：LatticeCastKit RendererServer 五端点 × 两后端（沿用
  integration_auth_test 模式）
- **Reflux 回归清单**：起播续播、暂停恢复、seek、音量、EOF 清零、错误展示、
  下线/上线切换

## 十、里程碑（对应 PR 拆分）

| PR | 内容 | 验收 |
|---|---|---|
| N+1（本设计） | PlayerCore 包骨架 + AVFoundationBackend + Reflux 门面化（行为不变） | 回归清单全绿 |
| N+2 | Lattice 接收端切 PlayerCore；接收窗换 PlayerSurfaceView | 投屏五端点契约测试绿 |
| N+3 | FFmpegBackend（或 MPVBackend）+ Reflux 切全格式后端 | 全格式回归 + 许可审查 |

## 十一、开放问题

1. FFmpeg 后端选型：libmpv（render API 成熟、字幕渲染强）vs 纯 FFmpeg +
   自绘（更轻、字幕自己排）——倾向 libmpv，Phase C 拍板
2. mpv/FFmpeg 二进制的分发形态（嵌 framework vs 下载）——影响包体积
   （FFmpeg 全量约 +40MB）与商店合规
3. AV1 硬解：8295 车机/新 Mac 支持，AVFoundation 覆盖有限——是否成为
   FFmpeg 后端的首个需求
4. 网络流协议范围：115 直链（http/https）必须；SMB/WebDAV 是否进内核
   （倾向不进，Reflux 侧转直链）

---

**决策记录**：本设计取代"接收端内嵌 AVPlayer"的临时实现（CastSink.swift 保留
为 AVFoundationBackend 的雏形）；"接收端是节点能力"的方向自
2026-09-15 讨论确立，本设计是其在播放层的落地。
