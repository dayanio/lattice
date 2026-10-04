# tvOS 引擎下沉 NetworkExtension（引擎常驻化）设计

日期：2026-10-04
状态：已批准，实施中
前置：2026-09-30-tvos-cast-design.md（tvOS cast v1，引擎在 App 进程内）
关联：2026-10-04-appletv-connect-runbook.md（当晚排查记录）

## 1. 背景与问题

tvOS cast v1 把 Go 引擎（`LatticeTVCore.a`，cgo 静态库）跑在 RefluxAppleTV App
进程内。实测（2026-10-04 晚，《看见恶魔》6.5GB 投放全链路排查）暴露的硬伤：

- App 一退后台，tvOS 挂起进程 → 引擎的 NATS cast 命令订阅全断；
- 电视必须前台停在"等待投屏"页才能收到投放——"往电视投片子"这个产品
  形态下不可接受（电视常态应该是无人交互的）。

**平台地基（推翻旧结论）**：`NEPacketTunnelProvider` / `NETunnelProviderManager`
/ `NEVPNManager` 均 tvOS 17.0+ 可用（Apple 官方文档，tvOS 17 引入第三方 VPN；
reflux tvOS deployment target 已是 17.0）。2026-09-30-tvos-cast-design.md 中
"tvOS 不开放隧道扩展"的结论**已过时作废**。

**方案（已拍板）**：把引擎下沉到 `NEPacketTunnelProvider` 扩展进程——tunnel
启用后由系统保活，App 退后台/被杀引擎照常收命令。

## 2. 当晚实测结论（设计输入）

- 电视 NATS 订阅健康、服务器投递正常、电视→Mac overlay 拉流 206 通，6.5GB
  原片直投播放成功（经 nc 直发 NATS 命令绕过 Mac 端假投递验证）。
- **Bug A（lattice-apple Mac 版，记录待修）**：Mac 的 PacketTunnelProvider 没有
  实现 `castPublish:` 消息处理（iOS 版有，见 LatticeTunnel/PacketTunnelProvider.swift），
  `publishCastFallback` 静默丢弃 → "delivered via nats-fallback" 全是假象。
  教训：**TV 版 PTP 必须实现此 handler**。
- **翻案**：embedded 引擎（TV 现用）没有 47822 overlay 命令口（那是 gomobile
  引擎 packetTUN 的能力），电视侧 errno 61 是设计内现象——NATS 兜底是 TV
  唯一合法命令通道；cast NATS 订阅与 TUN 完全独立（node.go:603-608 只依赖
  castCommandHandler != nil）。
- **Bug C（独立任务）**：电视↔Mac overlay RTT 65-117ms（同 LAN 应 <2ms）——
  ICE 直连未建成、流量绕云中继，播放卡顿的根因。不在本方案内。

## 3. 侦察结论（关键事实）

### 3.1 tvOS App 现状（reflux 仓 apple/）

- 引擎 = cgo 静态库 `LatticeTVCore.a`（lattice 仓 `make tvos-lib`，GOOS=ios
  GOARCH=arm64、appletvos SDK、`-target arm64-apple-tvos17.0`、
  -buildmode=c-archive），`TVStart(cfg, TVEventFn, ctx)` C ABI，进程级单例。
- 配置存 Keychain（key `latticetv.config`，service `io.reflux.apple`，
  **无 access group**，扩展进程读不到）。
- `TVCastConfig = {serverURL, token, name, privateKey}`；privateKey 回写必须
  （同名不同 key 会被控制面拒绝 = 砖）。
- cast 链路：NATS 订阅 → `"cast: {json}"` 事件 → TVCastManager → `TVOpenURL`
  句柄 → OverlayMediaReader（PlayerKit MediaRandomAccessReader）→ AVPlayer；
  状态上报 `TVHTTPPost` → `/__cast/status`（Bearer = URL 首段路径）。
- 引擎事件只有 `"cast: {json}"`（embedded 引擎，tvoslib.go:74-92）——没有
  connected/error 事件，入网进度只能靠轮询 `TVOverlayAddress`。
- 无任何 extension target；entitlements 全空；无 UIBackgroundModes。
- 配对：TVPairingService NWListener + 屏显码（前台 UX，留在 App）。
- XcodeGen：RefluxAppleTV target 编整个 `tvOS/` 目录（project.yml:119）；
  桥接头 `tvOS/Cast/LatticeTVBridge.h`；lattice build/tvos 链接设置
  （project.yml:144-147）；team JN4AC4DDAU；app bundle id `io.reflux.apple.tv`。
- RefluxAppleMacTests 把 tvOS/Cast 纯逻辑文件直编进 macOS 测试 bundle
  （project.yml:190-200）——新 IPC 纯逻辑可循此先例。

### 3.2 Mac NE 参照（lattice-apple 仓）

- XcodeGen `app-extension` target + Info.plist `NSExtensionPointIdentifier:
  com.apple.networkextension.packet-tunnel`、`CFBundlePackageType: XPC!`。
- App 依赖 `- target: <Tunnel>` 自动嵌入 appex。
- 配置经 `NETunnelProviderProtocol.providerConfiguration`（随 profile 持久、
  系统重启扩展可读）。
- App 侧 TunnelManager：loadAllFromPreferences 过滤 providerBundleIdentifier →
  saveJoin（**先删旧 profile**——OS 在 profile 创建时钉死 code requirement，
  旧 profile 会永远拒绝重签名的扩展）→ startVPNTunnel；轮询
  `sendProviderMessage`；2s/15s 无应答报"隧道进程无响应"。
- IPC = `NETunnelProviderSession.sendProviderMessage` 字符串协议。
- `setTunnelNetworkSettings` 完成 → `completionHandler(nil)`，跳过会卡
  "connecting"（Apple 文档 + Quinn 论坛帖）。

### 3.3 Go 引擎（lattice 仓）

- embedded 引擎**零改动**即可在 appex 跑：信令/订阅/TVOpenURL/TVHTTPPost
  全部与 TUN 无关（gVisor netstack 自有 socket，`Dial()` 直拨 overlay）。
- NE 进程适配已内置：CFFIXED_USER_HOME、dup2 日志到
  `<home>/Library/Caches/lattice-ne.log`、wg-identity 持久化
  （engine.go:415-440 / 664-673）。
- 构建技术已验证：c-archive + appletvos SDK + tvos17.0 target（现产物直接复用）。

## 4. 架构：路线 A'（embedded 引擎进 appex，不做 gomobile 迁移）

**核心形态**：引擎只在 appex 进程跑（信令常驻 + 媒体拉流句柄），App 进程
零引擎、只做 UI/播放。

```
[TV App 进程]                          [RefluxAppleTVTunnel.appex 进程]
  配对 UI / Keychain                     PacketTunnelProvider
  saveJoin(providerConfiguration)  ──►    TVStart(cfg from profile)
  startVPNTunnel                          NATS 订阅 lattice.cast.<id>.cmd
  darwin 通知监听 ◄─────────────────────   cast 命令 → App Group pending-cast.json
  sendProviderMessage("openMedia") ──►    + darwin notification
  AVPlayer ◄── http://127.0.0.1:port      TVOpenURL 句柄 + (P2)本地 HTTP 中继
                                          状态上报 TVHTTPPost（定时）
```

## 5. 关键设计决策

- **D1 整体下沉，不留过渡双引擎**：Phase 1 就把引擎完全搬进 appex——App
  target 移除 `LatticeTVCore.a`/桥接头/TVEngine.swift（编译期杜绝 App 调
  TVStart，同 key 双进程注册冲突从结构上不可能）。
- **D2 `setTunnelNetworkSettings` 必调，用零捕获配置**：`includedRoutes = []`、
  无 dnsSettings、mtu 1280、tunnelRemoteAddress=overlay IP → 设备流量不进
  隧道，引擎自有 socket（WG UDP/NATS/overlay 拨号）直走物理网卡，无自捕获
  环（macOS 版需要 bindInterface 正是因为装了真路由；我们不装）。VPN 显示
  connected 但零路由，正是我们要的：一个系统保活的执行上下文。
- **D3 privateKey 防砖三保险**：① appex startTunnel 时 providerConfiguration
  缺 key → 从 App Group `identity/identity.json` 恢复；② TVStart 后轮询
  `TVPrivateKey()`，变化即原子写 App Group；③ App 从 snapshot 应答拿到 key
  → 回写 profile 的 providerConfiguration（save-only 不重启，先例 lattice-apple
  `setSplitRouting`）+ Keychain。provider 在引擎起好前被 jetsam 也能靠
  App Group 文件在下次 startTunnel 自愈。
- **D4 命令投递 = App Group 文件 + darwin 通知**：appex 追加
  `cast/pending-cast.json`（数组 cap 8 淘汰最旧）+ 发 darwin 通知
  `io.reflux.apple.tv.cast.pending`；App 前台实时播，冷启动/回前台无条件
  drain；按命令 id 去重（现有 `TVCommandDedup` 不变，双投无害）。NE 不能
  主动发消息给 App，这是唯一通道；darwin 通知不会唤醒挂起的 App——接受，
  文件是持久兜底，接收（核心价值）发生在 appex 与 App 状态无关。
- **D5 状态上报 Phase 1 即迁 appex**（App 没引擎可拨号了）：App 保留
  `makeStatusRequest` + `TVStatusGate` 纯逻辑，阻塞的 `TVHTTPPost` 改走
  `status:` IPC 消息（appex 完成后才应答，闸门时序语义原样保留）。
- **D6 播放数据面两段走**：Phase 1 用 IPC `readAt` 桥（256KiB 钳制，验证级）；
  Phase 2 换 appex 内 `NWListener` loopback HTTP server（127.0.0.1，
  Range/206），先例 TVPairingService 的最小 HTTP 解析。`TVMediaHandle` 协议
  （OverlayMediaReader.swift:81-91）是不变接缝。
- **D7 XcodeGen 陷阱**：RefluxAppleTV sources 编整个 `tvOS/` 目录 → 必须
  excludes `["Tunnel", "Tunnel/**"]`，否则 appex 源码混进 app target；扩展
  bundle id 必须以 app 的为前缀：`io.reflux.apple.tv.tunnel`。
- **D8 真机 only**：tvOS NE 不支持模拟器；ATS 对 loopback（127.0.0.1）默认
  豁免，Info.plist 无需加例外（真机若被拦再补 `NSAllowsLocalNetworking`）。

## 6. IPC 协议（App ⇄ appex，`NETunnelProviderSession.sendProviderMessage`）

请求即消息字节，应答即 completionHandler 的 Data。全部 JSON 经
Codable/JSONSerialization，禁止拼接不可信字符串。

| 消息（App→Provider） | 载荷 | 应答 | Provider 动作 |
|---|---|---|---|
| `snapshot` | 无 | `{"engineUp","overlayIP","privateKey","lastError"}` | 读缓存状态 |
| `openMedia:<url>` | 媒体绝对 URL | `{"handle":i64,"size":i64}` / `{"error"}` | `TVOpenURL` + 句柄注册表 |
| `readAt:<handle>:<offset>:<len>`（仅 P1） | 十进制 `:` 分隔 | 原始字节 ≤256KiB；0 字节=EOF；空=错 | `TVReadAt` |
| `closeMedia:<handle>` | 句柄 id | nil | `TVClose` + 注销（未知 id no-op） |
| `status:<json>` | `{"endpoint","body","bearer"}` | `{"code":int}` | `TVHTTPPost` 同步 ≤15s |
| `ping` | 无 | `"pong <uptimeS>"` | 存活/调试 |

App 轮询 snapshot：connecting 期 2s、connected 后 10s，>15s 无应答报
"隧道进程无响应"（先例 lattice-apple pollPeerStates）。

## 7. 配置流

```
手机 App --QR--> TV App: TVPairingService(NWListener) 收配置
TV App: Keychain 存档(不变) + TVTunnelManager.saveJoin(
          NETunnelProviderProtocol{ providerBundleIdentifier="io.reflux.apple.tv.tunnel",
          providerConfiguration={serverURL,token,name,privateKey?} })
        → saveToPreferences → startVPNTunnel
系统拉起 appex: 读 profile → 缺 key 从 App Group 恢复 → TVStart(引擎唯一实例)
        → 轮询 TVOverlayAddress ≤60s → setTunnelNetworkSettings(零捕获) → completionHandler(nil)
App ←snapshot← {overlayIP, privateKey} → 回写 profile + Keychain，状态 .online
NATS cast 命令 → appex 引擎 → pending-cast.json + darwin 通知 → App 播放
```

profile 是引擎配置唯一真源，系统重启/jetsam 重拉 appex 后 startTunnel 重读
即自愈。

## 8. 实施步骤

### Phase 1 — tunnel target + 引擎下沉 + IPC 桥

新增（reflux 仓 apple/）：

1. `tvOS/Tunnel/Info.plist` — XPC! + NSExtension packet-tunnel +
   `NSExtensionPrincipalClass: $(PRODUCT_MODULE_NAME).PacketTunnelProvider`
2. `tvOS/Tunnel/RefluxAppleTVTunnel.entitlements` — networkextension
   `[packet-tunnel-provider]` + App Group `group.io.reflux.apple.tv`
3. `tvOS/Tunnel/PacketTunnelProvider.swift` — startTunnel（解析
   providerConfiguration → D3 恢复 → 组 cfgJSON 同 makeCfgJSON → TVStart →
   轮询 overlay IP ≤60s → D2 设置 → completion；超时 completion(error) 防僵尸）、
   stopTunnel（TVStop + 清句柄表）、handleAppMessage 按 §6 分发、
   `"cast: {json}"` → D4 双通知、句柄注册表（互斥锁，TVClose 后绝不碰指针）、
   全程 TunnelLog
4. `tvOS/Tunnel/TunnelLog.swift` — 文件日志进 App Group `logs/tunnel.log`
   （~256KiB 环形；设备上 appex 自己的 Caches 不可达），无 group 容器退回 caches
5. `tvOS/Cast/TVTunnelManager.swift` — 照 lattice-apple Shared/TunnelManager.swift
   瘦身：load 过滤 / saveJoin 先删旧 / connect/disconnect / NEVPNStatusDidChange /
   sendProviderMessage 封装 / snapshot 轮询 / updateProfileConfig(privateKey:)（save-only）
6. `tvOS/Cast/TVCastIPC.swift` — §6 消息编解码，纯 Foundation（可编入
   RefluxAppleMacTests，先例 project.yml:190-200）
7. `tvOS/Cast/TVCastHandoff.swift` — 双 target 共编：App Group pending-cast
   原子读写 + darwin 通知收发（CFNotificationCenterAddObserver + C trampoline）
8. `tvOS/Cast/TVIPCRemoteMediaHandle.swift` — 实现 TVMediaHandle，readAt 走
   信号量同步；CloseOnce 保证 closeMedia 至多发一次

修改：

9.  `project.yml` — RefluxAppleTV：dependencies 加
    `- target: RefluxAppleTVTunnel`、tvOS sources 加 excludes（D7）、**删**
    桥接头/HEADER/LIBRARY_SEARCH_PATHS/OTHER_LDFLAGS（D1 编译期守护）、
    entitlements 加 App Group；新增 RefluxAppleTVTunnel target（app-extension、
    tvOS 17.0、sources=[tvOS/Tunnel + TVCastHandoff + TVCastIPC]、复用
    LatticeTVBridge.h + lattice 链接设置、GENERATE_INFOPLIST_FILE: NO）
10. `tvOS/Cast/TVCastManager.swift` — 保持 State 枚举与公开面不变
    （TVWaitingView/ContentView/TVPlayerScreen 零改动）：去 TVEngine、加
    `tunnel: TVTunnelManager`；startIfNeeded → load+connect+snapshot 轮询；
    enroll → keychain + saveJoin + connect；play() → openMedia IPC →
    TVIPCRemoteMediaHandle → playerController 不变；状态发送体换
    tunnel.sendStatus；加 pending-cast drain（构造时/回前台/connected）
11. `RefluxAppleTV/RefluxAppleTVApp.swift` — 构造 TVTunnelManager 注入；
    `.onChange(of: scenePhase)` drain 接线
12. **删除** `tvOS/Cast/TVEngine.swift`

### Phase 2 — loopback HTTP 中继（字节路径升级）

- 新增 `tvOS/Tunnel/TunnelMediaServer.swift`：NWListener 绑 127.0.0.1:0，
  `GET /media/<handle>` 支持 Range/206/HEAD（Content-Length 用 TVTotalSize），
  TVReadAt 256KiB 分块喂；未知句柄 410
- 改 `TVIPCRemoteMediaHandle.swift`：读改为对 `http://127.0.0.1:<port>/media/<handle>`
  的同步 Range GET（port 经 openMedia 应答新增 `"port"` 字段下发）；删
  `readAt` 消息

### Phase 3 — 加固与收尾

- TVTunnelManager 接 removeProfile()（解除配对路径）；`.disconnected` 前台
  一键重连，不跟用户在设置里的手动关闭对抗
- TVWaitingView 展示隧道状态 + 可选调试行 tail tunnel.log
- appex 重启韧性：未知句柄应答错误 → App 自动重试 openMedia 一次（重投兜底）
- CI grep 守护：TVStart 只允许出现在 tvOS/Tunnel/ 下
- 本文档与 2026-09-30-tvos-cast-design.md 的过时结论修正

## 9. 风险与对策

| # | 风险 | 对策 |
|---|---|---|
| R1 | 跳过 setTunnelNetworkSettings 卡 connecting | D2 必调零捕获配置 |
| R2 | NE 签名/描述文件 | packet-tunnel-provider 是标准开发能力，自动签名首次构建注册 App Group；失败在 Xcode Signing & Capabilities 点一次 |
| R3 | 双引擎 | D1 结构性排除（App 编不过）；Phase 3 CI grep |
| R4 | 配对流 | 不变：TVPairingService 留在 App（前台 UX）；零路由不影响 LAN 可达 |
| R5 | appex 内 loopback server | 先例：lattice-apple iOS appex 内跑 Swifter（spike 验证过）；NWListener loopback 在 NE 进程允许 |
| R6 | 用户在 tvOS 设置关隧道 | stopTunnel 跑 TVStop，面板见离线；App 不自动对抗；重开即自愈（startTunnel 重读 profile） |
| R7 | appex 内存上限（Go runtime + gVisor） | 先例：同引擎已在 iOS LatticeTunnel 跑通；Phase 2 打 resident_size 每 60s + 查 jetsam |
| R8 | privateKey 砖窗 | D3 三保险；验证步骤 7 专门演练重启路径 |
| R9 | 播放中 appex 被杀 | 系统自动重拉（profile 仍启用）→ 同身份重注册；App 读失败 → OverlayError.readFailed，用户重投；自动重开上次 URL 列 Phase 3 候选 |
| R10 | tvOS VPN 首次启用 UX | tvOS 17 设置里有 VPN 项；若 saveToPreferences 需用户确认，TVWaitingView 给引导文案而非报错 |
| R11 | darwin 通知不唤醒挂起 App | 接受（D4）：文件是持久兜底；接收发生在 appex 与 App 状态无关 |
| R12 | readAt-over-IPC 吞吐（仅 P1） | 有界过渡路径，不调优，Phase 2 整体替换 |

## 10. 验证

**Phase 1**：
1. `make -C lattice tvos-lib`（产物不变，双 target 共用）
2. `xcodegen generate` → 确认 app 产物 `PlugIns/` 内嵌 RefluxAppleTVTunnel.appex
3. `xcodebuild -scheme RefluxAppleTV -destination 'generic/platform=tvOS' build`
   （模拟器不支持 NE）
4. devicectl 装到 Apple TV → 扫码配对 → 设置-VPN 里 profile 出现并连接（R10）
5. **核心价值验证**：按 Home 退后台 → 服务器确认电视 NATS 连接仍在
   （`tcpdump host <TV_IP> and port 4222`）→ Mac 投放 → tunnel.log 记录 cast
   事件 + pending-cast.json 有命令 → 重启 App 自动 drain 并播放
6. 前台回归：前台投放立即播；面板持续收到 5s 状态上报（现经 provider 中继）
7. 身份演练：首次连接后确认 profile/Keychain 均拿到 privateKey → 设置里关
   再开隧道 → 引擎同身份重新上线（服务器接受重注册）

**Phase 2**：6.5GB 原片播放 + 全文件段 seek + tunnel.log 确认 206 + 起播延迟
对比 Phase 1 + resident_size 观察（R7）

**Phase 3**：退出配对 → profile 移除干净；设置手动关隧道 → 面板显离线、App
不自动对抗

## 11. 提交约定

reflux 仓按阶段各一个 conventional commit（`feat(tv): cast engine 常驻
NetworkExtension`），`git commit -s`，不加 Co-Authored-By，提交后立即 push。
lattice 仓仅文档改动（本文档 + 旧文档修正）单独提交。
