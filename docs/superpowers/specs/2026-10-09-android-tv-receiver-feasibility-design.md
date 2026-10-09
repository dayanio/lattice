# Android TV 收端可行性设计 — 引擎形态、媒体管线与落地路线

- 日期：2026-10-09
- 状态：Draft（待评审）
- 涉及仓库：`lattice`（引擎 androidlib + 构建脚本）、`zyno`（Android 收端 App，Kotlin + Media3）
- 关联：[zyno 智能投屏 v1 设计](2026-10-09-zyno-smart-cast-design.md)（D2 将 Android 列为二期，本文是其可行性前提）、[tvOS 投屏设计](2026-09-30-tvos-cast-design.md)（收端形态的直接参照）、[gVisor 嵌入式网络设计](2026-09-23-gvisor-embedded-networking-design.md)（§11 平台矩阵已预判 Android）、[引擎 c-archive 迁移](2026-10-05-apple-engine-carchive-migration-design.md)（"预编译二进制 + 窄 C ABI"的交付哲学）

## 一、问题与结论

zyno 要覆盖 Android TV 收端，前提是回答四个问题：

1. lattice 引擎（gVisor netstack + wireguard-go + ICE/LRP + NATS）能不能跑在 Android 上、以什么形态交付？
2. 媒体管线（overlay Range 拉流 → 4K HDR 硬解播放）在 Android TV 上怎么搭？
3. 收端保活与生命周期在 Android 的碎片化环境下怎么处理？
4. 与用户已有 VPN 共存吗？

**结论：可行，无结构性障碍。** 三个真实风险——16KB page 对齐、TV SoC 吞吐、保活碎片化——全部有明确对策或 spike 判据（§九、§十二）。核心依据：

- **引擎是纯 Go 用户态栈**：shim.Server（channel.Endpoint + netstack）+ wireguard-go，无 TUN、无 raw socket、无特权 syscall（gVisor 设计 §五）；Go 的 android/arm64 是一级编译目标，NATS 客户端同为纯 Go。**Android 上没有 tvOS 那类"平台缺口"**——只存在工程量，不存在能力缺口。
- **生产先例充分**：官方 WireGuard Android App 在应用进程内跑 wireguard-go 库；Tailscale Android 在 App 内跑 Go 网络引擎。本文方案与他们同构，且比他们更轻（不需要 VpnService）。
- **收端 App 形态已有完整模板**：tvOS 设计的单进程"引擎 + 收令即播放"架构、配对 UX、错误语义可整体镜像，唯一换掉的是播放器层（PlayerKit/AVPlayer → Media3/ExoPlayer）。

## 二、决策记录

| # | 决策 | 理由 |
|---|---|---|
| D1 | **引擎交付 = `-buildmode=c-shared` 产出 `libLatticeCore.so` + 手写窄 C ABI + JNI 薄桥（~100 行 C + ~200 行 Kotlin）；不用 gomobile** | 对齐 tvoslib 先例与 2026-10-05 迁移决策（gomobile 排障黑盒、9/30 断流事故教训）；Android 标准交付物就是 .so，c-shared 是 gomobile 内部也在用的同一 buildmode |
| D2 | **复用 `apple/engine/embedded` 零改动**，新包 `engine/androidlib` 平行于 `apple/engine/tvoslib`；`apple/` 路径是历史包袱，不因 Android 入场重构 | 沿用 2026-10-05 的约束哲学："只换打包/桥接，不动产品架构"；导出面直接镜像 tvoslib（§四） |
| D3 | **v1 范围 = 纯收端**（等待投屏页 + 扫码配对 + 播放），镜像 tvOS 的 RefluxAppleTV 范围；不含 zyno Android 完整播放器（媒体库/刮削/源管理） | 收端不需要库能力即可交付跨网投屏价值；完整 zyno Android 是后续独立排期 |
| D4 | **命令通道 v1 先 NATS 兜底路**（引擎已支持，零新代码），47822 直发随 shim P1 `ListenUDP` 落地自动解锁——与 zyno 设计 M0 是同一块砖 | tvOS 同款先例：NATS-only 起步已验收，直发是纯增强；Android 不引入任何新前置 |
| D5 | **媒体管线 = Media3 (ExoPlayer) 自定义 DataSource 桥引擎 `OpenURL/ReadAt/Close` 句柄**——Kotlin 侧镜像 tvOS 的 OverlayMediaReader 模式 | 媒体路径与 tvOS 完全同构（Go 侧 media handle 代码复用），只换宿主播放器的解码/渲染层 |
| D6 | **保活 v1 与 tvOS 同约束**（App 停在等待投屏页）；`MediaSessionService`（mediaPlayback FGS）作播放期增强；FCM 冷启动唤醒列为远期 | 等待期无 MediaSession，起 FGS 名不正且 Android 15 禁 BOOT_COMPLETED 起 mediaPlayback；先按已验收的 tvOS 约束交付 |

## 三、引擎形态与构建（lattice 仓）

### 3.1 构建

镜像 `apple/Scripts/build_tvos_lib.sh` 的骨架（同一 GOTOOLCHAIN 钉死纪律）：

```bash
export GOTOOLCHAIN=go1.26.8
export CGO_ENABLED=1
export GOOS=android
export GOARCH=arm64
# NDK r27+（r28 起默认 16KB 对齐）；API level = minSdk
export CC="$NDK/toolchains/llvm/prebuild/darwin-x86_64/bin/aarch64-linux-android24-clang"

go build -buildmode=c-shared \
  -ldflags "-extldflags -Wl,-z,max-page-size=16384" \
  -o android/build/libLatticeCore.so ./engine/androidlib
```

- 产物：`libLatticeCore.so` + 导出头，打 APK 放 `jniLibs/arm64-v8a/`；引擎无特殊权限、无 entitlement，Play 审查无新增面。
- **16KB page size（Android 15+ 设备 / 2025-11-01 起 Play 对 targetSdk 35+ 硬性要求）**：Go runtime 不受影响（运行时从 OS 读 page size，容忍至 256KiB），**唯一要害是 ELF 段对齐**——显式 `-Wl,-z,max-page-size=16384`（NDK r28 默认已对齐），构建脚本加 `llvm-readelf -l` 断言对齐作为 gate。
- Makefile 加 `android-lib` target；引擎指纹机制（git tree hash stamp）原样沿用。

### 3.2 导出面（镜像 tvoslib，Android 侧命名）

```c
// 生命周期
char* AndroidStart(const char* cfgJSON, AndroidEventFn onEvent, void* ctx); // 异步，返回 NULL=成功
void  AndroidStop(void);
char* AndroidOverlayAddress(void);

// 媒体拉流句柄（Go 侧 HTTP 客户端：连接复用、Range、重试——与 TVOpenURL 同一实现）
char* AndroidOpenURL(const char* url, void** handleOut);
int64 AndroidTotalSize(void* handle);
int64 AndroidReadAt(void* handle, int64_t offset, int len, char* buf);
void  AndroidClose(void* handle);

// cast 状态回传（Phase 4 模式：overlay HTTP POST，Android 侧由 Kotlin 定时驱动）
char* AndroidHttpPost(const char* url, const char* body); // 与 tvOS 状态通道同语义
```

桥接三层（与 Apple 侧分层一一对应）：

| 层 | Apple 侧对应 | Android 侧 | 要点 |
|---|---|---|---|
| C ABI | tvoslib / corelib | `engine/androidlib`（Go） | tvoslib 字符串纪律照抄：`C.CString` → 同步回调 → 返回即 `C.free`，宿主只同步消费不存指针 |
| JNI 桥 | CoreEngineBridge.swift | ~100 行 C（JNI ↔ C ABI；回调经 `AttachCurrentThread` 转发 Kotlin） | `AndroidReadAt` 的 buf 用 `GetPrimitiveArrayCritical`，镜像 CoreSendPacketBatch 零拷贝纪律 |
| 宿主 API | LatticeTV 引擎静态库调用方 | ~200 行 Kotlin `LatticeEngine`（`external fun` + 生命周期 + 事件流） | zyno Android 只见 Kotlin API，不见 C 符号——D3 的"仓库 + 二进制边界"语义不变 |

风险备注：Go runtime 在 c-shared 形态下不接管进程信号处理，崩溃面收敛在引擎内部；Tailscale/WireGuard Android 量产多年，无已知结构问题，列入 A0 冒烟观察即可。

## 四、收端 App 架构（zyno 仓 android/，Kotlin）

```
Mac/iPhone zyno（发起端，不改）
  CastCommandSender：overlay 直发（等 ACK，失败自动兜底）
      └→ NATS lattice.cast.<电视AppID>.cmd
                                          │
Android TV：zyno 单进程（Kotlin）          ▼
  ┌─────────────────────────────────────────────────┐
  │ libLatticeCore.so（engine/androidlib，§三）       │
  │   EmbeddedEngine：入网 + gVisor netstack        │
  │   CastCommandHandler（NATS 订阅）→ 事件回调      │
  │   AndroidOpenURL/ReadAt：经引擎拨号的 Range 拉流  │
  └──────────────┬──────────────────────────────────┘
                 ▼ cast: {json} 事件（与 iOS/tvOS 同格式）
  AndroidCastManager（按命令 id 去重，保留 32 个，iOS 同款）
                 ▼
  Media3 ExoPlayer play(custom DataSource)
     —— DataSource.open(DataSpec) → AndroidOpenURL + ReadAt(offset)
     —— 媒体经 overlay 以 Range/206 从发送端拉流（语义同 tvOS）
```

三通道映射（cast 协议五端点语义零改动）：

| 通道 | tvOS 实现 | Android 实现 |
|---|---|---|
| 命令 | NATS 订阅 → cast 事件回调 | 同左（引擎内，D4；shim P1 后直发自动生效） |
| 媒体 | OverlayMediaReader → AVPlayer | 自定义 Media3 `DataSource`：`open(DataSpec)` 映射 `AndroidOpenURL` + seek offset，`read()` 映射 `AndroidReadAt`（D5） |
| 状态 | overlay HTTP POST /__cast/status | Kotlin 协程 5s 定时 → `AndroidHttpPost`（Phase 4 同款） |

**配对**：整体复用 tvOS §六 协议（电视亮 4 位码 + 二维码 → 手机扫码 → 管理面 enroll → 局域网 POST 下发 join payload）。Android 端：`ServerSocket`（仅绑局域网、一次性、code 校验）替代 NWListener；二维码生成用 ZXing；配置落 EncryptedSharedPreferences + Keystore（替代 Keychain，引擎 WG 私钥必须持久化复用的纪律同 tvOS §五）。

**UI 骨架**：等待投屏页（设备名 + overlay IP + "等待投屏"，`FLAG_KEEP_SCREEN_ON` 替代 `isIdleTimerDisabled`）、配对页、播放（Media3 PlayerView）。错误语义照 tvOS §八 全表：令牌 401 → 提示重投、重复投屏 → 直接替换、退后台 → 发起端视角离线、重进恢复。

**App 仓落点（开放问题，不阻塞可行性）**：建议 `zyno` 仓新增 `android/`（与 apple/ 平行）；`lattice-cast/android` 骨架属旧 LAN 协议线（minSdk 21 + 无引擎），不迁移、不承载本设计。

## 五、生命周期与保活

| 场景 | v1 行为 | 备注 |
|---|---|---|
| 停在等待页 | 引擎常驻在线（直发/兜底两路都可用） | 与 tvOS"电视当投屏显示器"同一产品设定 |
| 用户按 Home | 进程退后台，随时可能被 LMK 杀死 → 发起端视角离线 | v1 明示接受（同 tvOS）；UI 提示"保持 zyno 在前台" |
| 播放中 | `MediaSessionService`（mediaPlayback FGS）兜住进程优先级 | **mediaPlayback 不受 Android 15 的 6 小时 FGS 限时**（dataSync/mediaProcessing 才受）；需 `FOREGROUND_SERVICE_MEDIA_PLAYBACK` 权限 + 活跃 MediaSession——Media3 官方模式 |
| 开机自启 | 不做 | Android 15+ 禁止从 BOOT_COMPLETED 起 mediaPlayback FGS；电视场景用户本来就要手动开 App |
| 冷启动收令（App 未运行） | v1 不支持 | 远期增强：控制面 → FCM 高优先级推送唤醒（仅认证设备有 GMS）；但 cast 命令是 fire-and-forget，需 sender 侧重试配合，协议要动——独立立项，不进 v1 |
| 国产/白牌盒子 ROM | 杀后台更激进 | 三档对策：引导关电池优化 → 等待页常驻 → 侧载人群预期管理（§六 设备矩阵）；无结构性解法，诚实呈现 |

## 六、设备与编解码矩阵

三档支持级别（海外优先 → 一档是主战场）：

| 档 | 设备 | 形态 | 级别 |
|---|---|---|---|
| 一档 | 认证 Google TV / Android TV（Sony、TCL、Hisense、Philips、Panasonic、Google TV Streamer、onn 4K Pro 等） | Play 商店 | **v1 全力支持**；最低规格建议 4×A55 2.0GHz + 2GB RAM + 4K 认证（S905X4 级，2020 年后主力） |
| 二档 | Fire TV（Fire OS = Android fork） | 同 APK 侧载 / Amazon Appstore | 二期；Amazon 商店上架独立排期 |
| 三档 | 国产盒子/电视（当贝、小米国内版等，多无 GMS） | 侧载 APK（GitHub Releases） | 社区支持级别，保活与编解码不承诺 |

编解码（Media3 + 平台 MediaCodec，设备相关项如实标注）：

| 项 | 判定 |
|---|---|
| H.264 / HEVC Main10 | 一档 4K 认证设备通吃 |
| AV1 | 新 SoC（S905X4+、2023 后 Google TV）才有硬解；软解 4K 不现实——设备能力探测，不支持则明确报错而非花屏 |
| Dolby Vision profile 5 | 仅 DV 认证设备正常显示；无 DV 设备**不自动回退**（紫绿画面风险），能力探测后降级提示 |
| TrueHD / DTS-HD MA 直通 | 设备相关（Shield 级才有完整直通）；收端 v1 让 Media3 默认选轨 + 渲染能力探测回退 AAC/EAC3 |
| 4K120 | **不承诺**：主流电视面板/解码器上限 4K60；8.8GB 4K120 文件在 Android 端按 4K60 设备验收，超限设备降级报错。验收线 = 4K60 HDR |

## 七、VPN 共存（嵌入式的核心卖点在 Android 成立）

- **零槽位冲突**：嵌入式形态不申请 VpnService、不弹授权、不占系统唯一的 VPN 槽位——用户开着公司 VPN / 代理工具时 zyno 照常工作（gVisor 设计 §11.3 的判断在 Android 同样成立，且 Android 的 VPN 槽位冲突比 iOS 更普遍）。
- **已知行为**：用户 VPN 开启时，引擎的 WG UDP 恒走用户 VPN 出网（隧道套隧道）——功能可用、延迟/吞吐降；无 `protect()` 可用（那是 VpnService 才有的 API），v1 如实接受并在文档标注。
- **远期双轨**（对齐 iOS NE 模式）：可选 VpnService 形态作"全机接入"升级项——届时 `protect(fd)` 可用，隧道套隧道问题同时消除。非本期。

## 八、分发

| 渠道 | 要点 |
|---|---|
| Google Play（TV） | Leanback 入口；targetSdk 35+ 必须过 16KB page 校验（§3.1 已是构建 gate）；引擎 .so 无敏感 API 调用，审查无新增面 |
| 侧载 APK | GitHub Releases，覆盖三档设备；与 Play 同一签名与版本号纪律 |

## 九、性能与 Phase 0（唯一硬风险，先行验证）

用户态栈 + WG 加密的搬运开销在 TV SoC 上是真实变量（gVisor 设计 §十四.3 的"按家庭码率验证"约束）。**A0b 真机吞吐 spike 镜像 tvOS Phase 0**：

- 方法：一档中端设备（S905X4 级盒子）+ 发送端 armed 47823，收端经 overlay 拉流，采集 20/50/80/120 Mbps 各档 5 分钟 sustained；
- 判据：**80 Mbps sustained 不触发解码 starvation**（4K remux 峰值码率线）；
- 不达标回退：LAN 直连媒体路径（ICE host candidate 下媒体绕过 WG 加密直达）——另立项，同 tvOS 备注的 fallback 语义，不在本设计展开；
- 附带采样：起播延迟（NATS 收令 → 首帧），对齐 tvOS"3 秒内起播 1080p"验收。

## 十、里程碑

| 里程碑 | 内容 | 仓库 | 前置 |
|---|---|---|---|
| A0 引擎 android-lib | `engine/androidlib` + `make android-lib` + 16KB 对齐 gate + 冒烟：入网、`engine.Dial` 拉流、NATS 在线 | lattice | NDK r27+ |
| A0b 吞吐 spike | §九 真机采样，数据记录在案（4K 可行的带宽证据） | lattice + 真机 | A0 |
| A1 收端骨架 | JNI 桥 + `LatticeEngine` Kotlin API + 等待页 + NATS 收令 → Media3 起播（1080p） | zyno | A0 |
| A2 配对与 4K | 扫码入网全流程 + 4K60 HDR/seek/重复投屏 + 状态回传 | zyno | A1 |
| A3 双路与验收 | shim P1 落地后直发/NATS 双路矩阵 + E2E（清单见下） | lattice + zyno | shim P1（zyno 设计 M0 同一块砖） |

**验收清单**（镜像 tvOS §十，Mac 发起）：

1. 电视"加入"→ 手机扫码 → 入网成功、netmap 出现 peer；
2. NATS 路径 3 秒内起播 1080p；shim P1 后直发路独立成路（断 NATS 对照）；
3. 4K60 HDR 大文件可播、seek 生效、A0b 吞吐数据在案；
4. 播放中投新内容直接切换；退后台回前台重投恢复；
5. 配对监听 code 不匹配拒绝；入网后无额外监听端口；
6. 用户 VPN 开启时投屏仍通（性能降级可感知但功能可用）；
7. 16KB 设备（Android 15 模拟器切 16KB kernel）安装与入网正常。

## 十一、风险清单

| 风险 | 等级 | 对策 |
|---|---|---|
| TV SoC 吞吐不达 4K 线 | 中 | A0b 先行 spike，不达标走 §九 回退讨论；最低规格明示（S905X4 级） |
| 保活碎片化（白牌 ROM 杀后台） | 中 | 产品约束 + 三档对策（§五）；一档认证设备行为可控 |
| 编解码设备相关（DV p5 / AV1 / TrueHD） | 中 | 能力探测 + 明确降级提示，不花屏不假死；验收线钉 4K60 |
| 16KB 对齐遗漏 | 低 | 构建脚本 gate（readelf 断言）+ A3 含 16KB 设备用例 |
| JNI 桥生命周期错误 | 低 | 照 tvoslib 字符串纪律 + 零拷贝纪律逐条镜像；A1 长时间挂机观察 |
| Go runtime on Android 未知问题 | 低 | Tailscale/WireGuard 量产先例；A0 冒烟覆盖 |

## 十二、非目标

- zyno Android 完整播放器（媒体库/刮削/源管理）——独立排期；
- sender（Android 手机发投屏）——v1 只做收端；
- Fire TV 商店上架、VpnService"全机接入"、FCM 冷启动唤醒——各自独立立项；
- 鸿蒙 NEXT——Go 无 ohos/arm64 工具链，gVisor 设计 §11.2 已定"投入前先 spike"，不在本设计；
- cast 协议任何改动。

## 十三、参考

- [Support 16 KB page sizes — Android Developers](https://developer.android.com/guide/practices/page-sizes)（2025-11-01 起 targetSdk 35+ 硬性要求）
- [Generating Android 16 KB page size libraries from Go — Dan Ballard](https://danballard.com/2025/09/28/generating-android-16kb-page-size-libraries-from-go)（Go c-shared 16KB 对齐实践）
- [Changes to foreground service types for Android 15](https://developer.android.com/about/versions/15/changes/foreground-service-types)（mediaPlayback 禁 BOOT_COMPLETED、6h 限时范围）
- [Foreground service types — Android Developers](https://developer.android.com/develop/background-work/services/fgs/service-types)（mediaPlayback FGS 要求）
- [Background playback with MediaSessionService — Media3](https://developer.android.com/media/media3/session/background-playback)（播放期保活官方模式）
- [Optimize for Doze and App Standby — Android Developers](https://developer.android.com/training/monitoring-device-state/doze-standby)（FGS 不受 Doze 杀）
