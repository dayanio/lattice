# tvOS 投屏设计：RefluxAppleTV 内嵌引擎，苹果生态闭环

**日期**：2026-09-30
**状态**：Approved（用户 2026-09-30 确认：路径 A 统一新架构 + 手机扫码入网）
**涉及仓库**：`lattice`（引擎）、`lattice-apple`（发起端/配对）、`reflux`（电视接收端）
**前提**：iOS/macOS 投屏全链已验证（见 `2026-09-30-cast-e2e-testing-guide.md`）；tvOS 引擎 spike 已跑通（本地 `spike-tvos/`，不入仓）

---

## 一、背景与现状

投屏新栈（Mac 面板发起 → CastMediaFileServer（overlay 47823）→ overlay 命令包 UDP 47822 引擎内截获主路 + NATS `lattice.cast.<AppID>.cmd` 兜底 → iOS 通知接力 → Reflux 拉流播放）在 iPhone 与 Mac 上已全链验证（93MB 与 8.8GB 4K120 HDR）。

tvOS 与 iOS 的三个平台差异决定了不能照搬：

1. **无 NetworkExtension**：tvOS 不开放隧道扩展，「Lattice 扩展后台收令」这个角色不存在；
2. **不能跨 App 拉起**：`reflux://` 深链跳转在 tvOS 不可用；
3. **无通知系统**：`UNUserNotificationCenter` 在 tvOS 不可用。

因此 iOS 上「收令的（扩展）与播放的（Reflux）分进程 + 通知接力」在 tvOS 上必须压缩为**单进程**：引擎与播放器同进程，收令即播放。

已验证的 spike 事实（2026-09-30，本地 `spike-tvos/`）：

- Go 引擎以 `-buildmode=c-archive`（`GOOS=ios GOARCH=arm64` + appletvos SDK）构建静态库、链接进 tvOS App 运行成功；
- `EmbeddedEngine`（gVisor 用户态 netstack，无 utun）入网、`engine.Dial` 经 overlay 拉流工作正常；
- Mac 对照组测过吞吐（本机回环有已知测量失真 ~0.1MB/s）；**TV 端真实吞吐尚未采到**（Phase 0 补）。

## 二、目标 / 非目标

**目标**
1. Mac/iPhone 发起，Apple TV（RefluxAppleTV）真实播放，与 iOS/macOS 同一套投屏栈（命令通道、媒体文件服务、协议语义零改动）。
2. 电视入网（enrollment）零新服务端依赖，UX 为「电视亮码、手机扫码」。
3. 引擎承载形态为 tvOS 静态库 + cgo ABI，产品化 spike 的构建方式。

**非目标**
- 不改 cast 命令协议与五端点语义；不改 Mac 发起端的双传输逻辑（overlay 失败自动 NATS 兜底，电视只听 NATS 即插即用）。
- 不做 overlay 47822 主路在电视端的截获（需 netstack UDP 绑定，列为后续增强）。
- 不做电视端状态回传的完整协议化（Phase 4 用 overlay HTTP 临时通道）。
- 不做 Mac 端扫码/配对 UI（v1 手机配对一次即可；Mac 后续可用连续互通相机）。

## 三、总体架构

```
Mac/iPhone Lattice（发起端，已有，不改）
  CastMediaFileServer 注册会话（overlay 47823，令牌 URL）
  CastCommandSender：overlay 主路（等 ACK，失败自动兜底）
      └→ NATS lattice.cast.<电视AppID>.cmd
                                          │
Apple TV：RefluxAppleTV 单进程             ▼
  ┌─────────────────────────────────────────────────┐
  │ LatticeTV 引擎静态库（cgo ABI）                  │
  │   EmbeddedEngine：入网 + gVisor netstack        │
  │   CastCommandHandler（NATS 订阅）→ 事件回调      │
  │   TVOpenURL/TVReadAt：经引擎拨号的 Range 拉流     │
  └──────────────┬──────────────────────────────────┘
                 ▼ cast: {json} 事件（格式与 iOS 一致）
  TVCastManager（按命令 id 去重）
                 ▼
  PlayerKit play(reader: OverlayMediaReader, ...)
     —— 媒体经 overlay 以 Range 请求从 Mac 拉流（语义同 iPhone）
```

**产品限制（明确接受）**：tvOS App 退后台即挂起、引擎停。电视需停在 RefluxAppleTV 的「等待投屏」页（`isIdleTimerDisabled` 防锁屏）。这符合「电视当投屏显示器」的用法。

## 四、引擎侧设计（lattice 仓）

### 4.1 EmbeddedEngine 接线 cast 事件

镜像 iOS 侧 `apple/engine/engine.go:410` 的接法：

```go
castSink := func(payload []byte) { e.emit("cast: " + string(payload)) }
// NodeConfig.CastCommandHandler: castSink  （NATS 订阅，engine.go:458 同款）
```

`EmbeddedEngine` 新增：

- `SetEventHandler(func(event string))`：事件即字符串，cast 命令为 `cast: {json}`（与 iOS gomobile 委托 `OnEvent` 同格式，上层去重逻辑可复用）；
- `Start` 构造 NodeConfig 时传 `CastCommandHandler`（`internal/agent/node.go:215`）。

### 4.2 cgo ABI（新包 `apple/engine/tvoslib`）

spike `goarchive` 的产品化。导出（C ABI，Swift bridging 直链）：

```c
// 生命周期
char* TVStart(const char* cfgJSON, TVEventFn onEvent, void* ctx); // StartAsync 立即返回，Swift 轮询 TVOverlayAddress；返回 NULL=成功，错误串=失败（TVFree 释放）
void  TVStop(void);
char* TVOverlayAddress(void); // 空串=未入网

// 媒体拉流句柄（Go 侧持 HTTP 客户端：连接复用、Range、重试）
char* TVOpenURL(const char* url, void** handleOut);   // 返回错误或 NULL
int64 TVTotalSize(void* handle);                       // -1 = 未知
int   TVReadAt(void* handle, int64_t offset, int len, char* buf); // 返回实际字节数，0=EOF，<0=错误
void  TVClose(void* handle);
```

事件回调 `TVEventFn(const char* event, void* ctx)`。

**Swift 映射**：`OverlayMediaReader: MediaRandomAccessReader`——`totalSize → TVTotalSize`、`read(offset:length:into:) → TVReadAt`（遵守协议并发契约：同步、信号量、线程安全由 Go 句柄内部保证）、`close → TVClose`。播放入口 `playerController.play(reader:seekTo:knownDuration:)`（`PlayerKit/Playable.swift:27`）。

### 4.3 构建

`make tvos-lib`（lattice 仓）：`CGO_ENABLED=1`、`GOOS=ios GOARCH=arm64`、appletvos SDK 的 clang、`-buildmode=c-archive`，产出 `apple/build/tvos/LatticeTVCore.a + .h` + 单测（`go test` 普通 darwin 跑逻辑层）。

## 五、电视端设计（reflux/apple，RefluxAppleTV target）

- **TVCastManager**（新，`tvOS/Cast/`）：
  - 持有引擎生命周期：读 Keychain 配置 → `TVStart` → 轮询 `TVOverlayAddress` → 状态机（未入网/入网中/在线/播放中）；
  - 收 `cast: {json}` → 按命令 `id` 去重（保留 32 个，与 iOS 同款）→ `action == "play"` → `playerController.play(reader:)`；
  - 播放失败（401 等）在等待页/横幅提示，重新发起即恢复。
- **OverlayMediaReader**（新）：见 §4.2 映射。
- **等待投屏页**：设备名 + overlay IP + 「等待投屏」；`isIdleTimerDisabled = true`。
- **配对页**（§六）：亮码 + 等待下发。
- **project.yml**：`RefluxAppleTV` 加 `HEADER_SEARCH_PATHS`/`LIBRARY_SEARCH_PATHS` → `../../lattice/apple/build/tvos/`、`OTHER_LDFLAGS: -lLatticeTVCore -lresolv`、bridging header（沿用 spike 验证过的链接方式）。
- 入网配置存 **Keychain**（serverURL + token + name + privateKey——引擎 `PrivateKey()` 返回的 WG 私钥必须持久化复用，重注册换 key 会被管理面拒绝，见 `embedded/engine.go:73`）。

## 六、手机扫码配对

电视没摄像头、不能跨 App、不能手输长 token → **电视亮码、手机扫码、手机经局域网下发**：

```
电视：「加入 Lattice」→ 起一次性局域网 HTTP 监听（NWListener 随机端口）
      屏显大号 4 位码 + 二维码 lattice://tv-pair?lan=http://<LAN-IP>:<port>&code=XXXX&name=...
手机：Lattice iOS 扫码（复用现有相机扫描，JoinView 同款）→ 确认页（电视名 + 码）
      → POST /api/v1/agent-enroll {agentName: 电视名, agentType: "device", ...}
        （管理面已有，internal/server/server/agent.go:45；客户端 LatticeAPI 需新增封装）
      → 把 join payload（lattice://join?server=&token=&name=，JoinPayload.swift 同格式）
        经局域网 POST 给电视端点（带 code 校验）
电视：收配置 → 存 Keychain → 引擎入网 → 等待投屏页显示 overlay IP
```

- 电视端监听是**一次性**的：入网成功即关；监听仅绑局域网、要求 code 匹配才收配置（防同网恶意下发）。
- 手机侧扫码连接会触发 iOS 本地网络权限（Lattice 已有 `NSLocalNetworkUsageDescription`）；电视端 tvOS 同样需 `NSLocalNetworkUsageDescription`（授权一次）。

## 七、命令与状态通道

- **命令（Mac → 电视）**：v1 只接 NATS（`lattice.cast.<AppID>.cmd`，fire-and-forget）。Mac `CastCommandSender` overlay 等不到 ACK 自动落 NATS，电视零监听端口即插即用。语义与 iOS 同：`{"id","action":"play","url","title","ts"}`，按 id 去重；命令丢失=电视没反应，重投即可（§13 既定取舍）。
- **状态（电视 → Mac，Phase 4）**：电视引擎拨号 POST 到 Mac CastMediaFileServer 新增的状态端点（overlay 内、令牌鉴权），Mac 面板显示播放状态。不引入引擎改动。

## 八、错误与边界

1. **电视挂起**（退后台）→ 引擎停 → 发起端视角电视离线；回前台引擎重启（PrivateKey 复用）。
2. **会话令牌失效**（Mac App 重启清内存会话）→ 电视拉流 401 → 电视提示，重新发起即恢复（iOS 已知坑 5 同源）。
3. **管理面不可达** → 引擎起不来（入网/NATS/心跳全依赖），与 iOS 已知限制一致（既定取舍，另行处理）。
4. **重复投屏**：新 play 命令直接替换当前播放（stop 旧会话、起新 reader）。
5. **大文件**：Reader 直读不落盘；PlayerKit 帧内存预算已修（8.8GB iPhone 已验证）；tvOS 内存上限更宽裕；吞吐以 Phase 0 实测为准。
6. **spike 收尾**：`spike-tvos/` 保持本地一次性目录不进仓；产品代码落地后可删。

## 九、实施阶段

- **Phase 0（半天，先行）**：真实 Apple TV 跑 SpikeTV 采 overlay 拉流吞吐（47823 武装后 `SPIKE_URL` 指向会话 URL），确认 4K 带宽可行——唯一未验证前提；不达标则回头讨论（LAN 直连 fallback 另立项）。
- **Phase 1（lattice）**：`EmbeddedEngine` 事件接线 + `apple/engine/tvoslib` + `make tvos-lib` + 单测。
- **Phase 2（reflux）**：RefluxAppleTV 集成（TVCastManager + OverlayMediaReader + 等待投屏页；入网先用「粘贴 join payload」占位）→ **电视出画面**。
- **Phase 3（lattice-apple + reflux）**：手机扫码配对（电视端 PairingService + 二维码；手机端 LatticeAPI.agentEnroll + 扫码确认页 + 下发）。
- **Phase 4**：状态回传 + E2E 验收 + 文档（E2E 手册补 tvOS 节）。

## 十、验收清单

同一 overlay 内（Mac 跑 cast 发起端）：

1. 电视「加入 Lattice」→ 手机扫码 → 码一致确认 → 电视显示已入网（overlay IP），netmap 出现该 peer；
2. Mac 面板发起（peer=电视名）→ 电视 3 秒内起播 1080p（NATS 兜底路径）；
3. 8.8GB 4K120 HDR 文件可播、进度条 seek 生效（overlay 拉流）；
4. 重复投屏：播放中投新内容直接切换；
5. 电视退后台再回前台：重投恢复；
6. Mac App 重启后旧令牌 401：重新武装会话重投即恢复；
7. 鉴权：电视端局域网配对监听 code 不匹配拒绝；入网后无监听端口（`netstat` 验证）；
8. Phase 0 吞吐数据记录在案（4K 可播的带宽证据）。
