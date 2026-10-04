# Cast E2E 测试手册（给 AI 助手的可执行版）

**日期**：2026-09-30
**适用**：`spike/cast-gateway-phase0`（lattice-apple）+ `dev`（reflux）+ lattice `docs/cast-gateway-design` 分支上的投屏栈。
**已验证基线**：93MB 小文件与 8.8GB 4K120 HDR 大文件全链（面板发起 → 通知 → 点击 → Reflux 从 Mac 文件服务 Range 拉流真实播放，含拖动进度条 seek）在真机上通过（2026-09-30）；大文件依赖 PlayerKit 帧内存预算与 seek 修复，见「已知坑」与 spike 文档附二。tvOS 接收端代码全链就绪（三仓分支 feat/tvos-cast / feat/tv-cast / feat/tv-pair），真机验证待执行。

---

## 0. 架构一页图

```
Mac 面板/调试钩子发起
  → CastMediaFileServer 注册文件会话（令牌 URL，监听 10.96.0.6:47823，仅 overlay）
  → overlay 命令包：UDP → 10.96.0.4:47822（引擎内截获 + ACK，主路）
  → 失败自动兜底：provider 消息 castPublish → 引擎 NATS 会话发布
    lattice.cast.<AppID>.cmd（AppID 从 netmap 取，手机 = "iPhone"）
手机:
  引擎截获/订阅 → 扩展写 App Group 待执行命令 + 发本地通知
  → 用户点通知 → Lattice 深链透传 reflux://cast?url=..&title=..
  → Reflux 解析 → RendererBridge.load() 真实拉流播放（媒体经 overlay 从 Mac 拉）
```

## 1. 健康检查（每次测试前全跑一遍）

```bash
# 1a. Mac 的 LatticeMac 活着吗？（退出原因见 /tmp/lattice-mac-lifecycle.log；死了就重新 open）
pgrep -f "MacOS/LatticeMac" || open /Users/francis/workspc/lattice-apple/apple/build/Build/Products/Debug/LatticeMac.app

# 1b. 媒体文件服务在监听吗？（开关：面板 → 投屏 → 允许其他设备播放本机文件，默认关）
lsof -iTCP:47823 -sTCP:LISTEN -nP | tail -1        # 期待: LatticeMac ... 10.96.0.6:47823

# 1c. overlay 数据面通吗？
ping -c 2 -t 3 10.96.0.4                            # 期待: 0% loss（直连 ~8ms；>40ms 说明走了 relay）

# 1d. 手机引擎活着吗？
xcrun devicectl device info processes --device A6D45DB9-9231-57C4-8121-5E69F33DEAB5 2>&1 | grep -c LatticeTunnel
```

## 2. 发起投屏（武装调试钩子，无需点 UI）

```bash
# 文件路径换成实际要投的；peer = 目标设备在 netmap 里的名字（手机 = "iPhone"）
echo '{"file":"/tmp/castmedia/test.mp4","peer":"iPhone"}' \
  > "$HOME/Library/Group Containers/group.io.lattice.shared/cast/cast-debug.json"
```

3 秒内 App 轮询到即自动发起（注册会话 → overlay 发令等 ACK → 失败自动 NATS 兜底）。

**替代：castcmd 命令行**（不经过 Mac App，用于隔离验证传输层）：

```bash
cd ~/workspc/lattice
GOTOOLCHAIN=go1.26.8 go run ./cmd/castcmd -transport overlay \
  -target 10.96.0.4:47822 -media "http://10.96.0.6:47823/<token>/x.mp4" -title t
GOTOOLCHAIN=go1.26.8 go run ./cmd/castcmd -appid iPhone \
  -media "http://10.96.0.6:47823/<token>/x.mp4"        # NATS 兜底通道
```

## 3. 手机侧（需要人的一次操作）

通知到达（标题 = 文件标题）→ **点它** → Lattice 闪现深链 → Reflux 打开并起播。
失败时 Reflux 会弹窗：**「源无法播放」= 连接/拉流失败；「源加载超时」= 路径太慢（大文件 moov）**——把弹窗文字记下来。

## 4. 服务端取证（判定失败在哪一环）

```bash
tail -20 /tmp/lattice-castmedia.log
```

行含义：
- `cast to iPhone delivered via overlay` = 主路 ACK ✓；`via nats-fallback` = 兜底 ✓
- `conn: GET /<token>/<file> Range=bytes=N-` + `conn: -> 206` = 手机播放器来拉流了 ✓
- `conn: -> 401` = 令牌失效（App 重启会清空内存会话——重新武装即可）
- `body complete <N>` = 该段传输完成；`body send failed at X/Y` = 传输中断（路径闪断）
- 无任何 conn 行而通知已发 = 手机侧根本没来拉（手机网络/Reflux 问题）

## 5. 手机引擎日志（深挖用）

```bash
cd /tmp && rm -f lattice-ne.log
xcrun devicectl device copy from --device A6D45DB9-9231-57C4-8121-5E69F33DEAB5 \
  --domain-type appDataContainer --domain-identifier io.lattice.ios.tunnel \
  --source Library/Caches/lattice-ne.log --destination /tmp/lattice-ne.log
grep -a "castcmd: received" /tmp/lattice-ne.log | tail -3      # 命令到达记录
grep -aE "Discover transport failed|SYN canceled" /tmp/lattice-ne.log | tail -3  # 传输层异常
```

## 6. 已知坑（按命中概率排序）

1. **装了旧引擎**：macOS 隧道扩展按 bundle ID 经 LaunchServices 解析，`~/Applications/LatticeMac.app`
   里的旧副本会悄悄提供引擎，而 UI 是新构建。改完代码后把新构建 `ditto` 进 `~/Applications`，并用
   `ps` 确认隧道扩展路径与日期；
2. **LatticeMac 消失**：先看 `/tmp/lattice-mac-lifecycle.log`（`ExitForensics`）——`prevExit=unclean`
   说明被 SIGKILL/崩溃，`signal N` / `willTerminate` 说明有人让它退出。曾经的"静默退出"是 SIGPIPE
   （已修）；
3. **手机 `unavailable`（devicectl）**：锁屏/未在同一网络。装机和 `process launch` 都要求手机**解锁**；
   `make ios` 装机会断开手机隧道，装完需在手机上重连；
4. **8.8GB 4K120 大文件**：需要 PlayerKit 的帧内存预算修复（否则起播 1.5 秒被系统杀）。**不要**在 Mac 上用
   `10.96.0.6` 复现/测速（本机访问自己的 overlay 地址只有 ~100KB/s）；
5. **App 重启后旧令牌失效**（会话在内存里）→ 旧通知点开会 401，重新武装重投即可；
6. **手机在蜂窝网络下**：NATS 4222 可能被墙 → 引擎起不来、隧道反复重启——确保手机连家里 WiFi；
7. **手机锁屏 10-40 分钟后 overlay 疑似死亡**：**未证实**（见 spike 文档附二），复测时同时抓两侧日志。

## 6b. 调试手册（测吞吐 / 看播放器日志）

```bash
# Mac→手机 TCP 连接的真实状态（重传、RTT、发送窗口）；netstat -s 在本机不可用
nettop -m tcp -n -L 1 -J bytes_out,re-tx,rtt_avg,tx_win 2>&1 | grep "10.96.0.6:47823<->10.96.0.4"
#  tx_win 恒为几十~上千字节 = 拥塞窗口被丢包打崩；0 = 手机在流控（播放器不读，正常）

# 手机引擎内存与丢包：拉手机 lattice-ne.log 后看 mem[periodic] 与 tun stats
#  footprint 逼近 limit≈52428800 = 即将被 jetsam；tun stats 的 dropped 应为 0

# 直接看手机上 Reflux/PlayerKit 的日志（os.Logger）：带控制台启动并触发深链
xcrun devicectl device process launch --device <id> --terminate-existing --console \
  --environment-variables '{"OS_ACTIVITY_DT_MODE":"YES"}' \
  --payload-url "reflux://cast?url=<urlencoded http://10.96.0.6:47823/<token>/x.mp4>&title=t&position=3600000" \
  io.reflux.apple
#  position= 可选（毫秒），用来测 seek；关键字：memory warning / signal 9 / q=… dur=… / landed physically
```

## 7. 重新构建（改了代码之后）

```bash
# 引擎 + iOS（装机 + 断隧道，装完需手机重连）
cd ~/workspc/lattice-apple && GOTOOLCHAIN=go1.26.8 make ios DEVICE=winston
# Mac
cd ~/workspc/lattice-apple && GOTOOLCHAIN=go1.26.8 make mac
# Reflux iOS
cd ~/workspc/reflux/apple && xcodebuild -project RefluxApple.xcodeproj -scheme RefluxApple \
  -configuration Debug -destination 'generic/platform=iOS' -derivedDataPath build build
xcrun devicectl device install app --device A6D45DB9-9231-57C4-8121-5E69F33DEAB5 \
  build/Build/Products/Debug-iphoneos/RefluxApple.app
```

改 Go 引擎代码后 `make ios`/`make mac` 会自动重建 xcframework；改 Swift 后同样。
注意：`make ios` 装机会断开手机隧道，装完需在手机上重连一次。

## 8. tvOS（Apple TV 接收端）【代码全链就绪，真机验证待执行】

**架构一句话**：RefluxAppleTV 单进程内嵌引擎——无隧道扩展、无本地通知、无深链；命令走 NATS 兜底通道
`lattice.cast.<电视AppID>.cmd`，媒体由引擎 `openMedia` 拨号经 overlay 从 Mac Range 拉流。入网配置
（serverURL/token/name/privateKey）存 Keychain，WG 私钥持久化复用（重注册换 key 会被管理面拒绝）。
电视退后台 App 即挂起 = 引擎停，**测试全程停在 Reflux 等待页**。

### 8.1 构建与安装

```bash
# ① 引擎静态库（产出 apple/build/tvos/LatticeTVCore.a + LatticeTVCore.h，10 个 _TV 导出：
#    TVStart/TVStop/TVOpenURL/TVReadAt/TVTotalSize/TVClose/TVFree/TVOverlayAddress/TVPrivateKey/TVHTTPPost）
cd ~/workspc/lattice && make tvos-lib

# ② 电视 App（Apple TV 需已与 Mac 配对；<ATV-ID> 用 xcrun devicectl list devices 查）
#    发现不了电视 / 连不上时先看：2026-10-04-appletv-connect-runbook.md（含 60 秒排查清单）
cd ~/workspc/reflux && git checkout feat/tv-cast && xcodegen generate
xcodebuild -project RefluxApple.xcodeproj -scheme RefluxAppleTV -configuration Debug \
  -destination 'generic/platform=tvOS' -derivedDataPath build build
xcrun devicectl device install app --device <ATV-ID> build/Build/Products/Debug-appletvos/RefluxAppleTV.app

# ③ 手机端配对用 iOS App
cd ~/workspc/lattice-apple && git checkout feat/tv-pair
# Xcode 选 Lattice scheme 跑真机，或：
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -configuration Debug \
  -destination 'generic/platform=iOS' -derivedDataPath build build
```

### 8.2 入网（两种方式，任选其一）

- **主入口·扫码**：电视「加入 Lattice」亮二维码（`lattice://tv-pair?…`）+ 4 位配对码 → 手机 Lattice
  设置 → 投屏 → 添加 Apple TV 扫码。token 经 `/api/v1/token/generate` 下发（与管理台生成同流，token 名
  带随机后缀），随后手机向电视 LAN URL `POST /config` 下发 TVCastConfig，header `X-Pair-Code` 必须
  等于屏显 4 位码。
- **次级兜底·粘贴链接**：电视等待页粘贴完整 `lattice://join?server=<管理地址>&token=<token>` 链接
  （残缺/非此形态的链接会被拒）。

提醒：tvOS 本地网络权限已在 project.yml 声明（`NSLocalNetworkUsageDescription`），首次弹窗必须
允许——拒绝则 LAN 配对下发不通。

### 8.3 投放与验收清单【待真机】

投放命令与 §2 同形，peer = 电视在 netmap 里的名字：

```bash
echo '{"file":"/tmp/castmedia/test.mp4","peer":"<电视名>"}' \
  > "$HOME/Library/Group Containers/group.io.lattice.shared/cast/cast-debug.json"

# 或 castcmd 直接走 NATS 通道（AppID = 电视名）
cd ~/workspc/lattice && GOTOOLCHAIN=go1.26.8 go run ./cmd/castcmd -appid "<电视名>" \
  -media "http://10.96.0.6:47823/<token>/x.mp4" -title t
```

验收清单（设计 §十，逐项【待真机】）：
1. 扫码入网：码一致确认 → 电视显示 overlay IP，netmap 出现该 peer；
2. 面板发起 → 3 秒内起播 1080p（NATS 兜底路径）；
3. 8.8GB 4K120 HDR 可播、进度条 seek 生效（overlay 拉流）；
4. 播放中重投新内容直接切换；
5. 电视退后台再回前台：重投恢复；
6. Mac App 重启后旧令牌 401：重新武装会话重投即恢复；
7. 配对 code 不匹配拒绝；入网后电视无监听端口（`netstat` 验证）；
8. Phase 0 吞吐数据在案（见 8.6）。

### 8.4 已知坑（按命中概率）

1. **电视退后台即挂起 = 引擎停**：等待页 `isIdleTimerDisabled`（TVCastManager.swift:163）只防息屏，
   不防用户按 Home/退出 App——退出后需重进 Reflux 等待页，引擎随 App 重启（PrivateKey 复用，入网快）；
2. **Mac App 重启清媒体会话 → 电视拉流 401**：重新武装 cast-debug.json 重投即可（同 §6 坑 5）；
3. **一次性配对监听只在电视配对页活着**：退出配对页监听即关——扫码要在亮码时完成；
4. **会话令牌在 Mac 内存**：LatticeMac 别在投放中途重启；
5. **推送失败残留的未消费 token**：在管理台可见、可删；
6. **cast-debug.json 下发的 serverURL 是手机配置的管理地址**：电视必须可达该地址。

### 8.5 取证

- Mac 侧：`tail -20 /tmp/lattice-castmedia.log`，行含义沿用 §4（`delivered via nats-fallback` = 命令
  经 NATS 到电视 ✓；`conn: GET /<token>/<file> Range=…` + `-> 206` = 电视播放器来拉流 ✓）；
- 电视侧：引擎事件经 TVCastManager 打 OSLog（subsystem `io.reflux.apple`，category `TVCastManager`），
  Console.app 连 USB 调试时可见【待真机确认】。

### 8.6 吞吐（Phase 0）【待真机采集，不预写数字】

工具链已就绪：SpikeTV.app（`spike-tvos/` 本地一次性目录，不入仓）从环境变量 `SPIKE_URL` 读当前会话
URL，跑 45 秒后上报，Mac 侧收进 `/tmp/spike-tv-report.log`：

```bash
# ① 按 §2 武装会话，记下 47823 媒体 URL 里的 <token>
# ② Xcode 跑 SpikeTV scheme 到 Apple TV：Edit Scheme → Run → Arguments →
#    SPIKE_URL=http://10.96.0.6:47823/<token>/x.mp4   SPIKE_SECONDS=45
# ③ Mac 侧收上报：
python3 /tmp/spike_report_server.py &   # 若未在跑
tail -2 /tmp/spike-tv-report.log        # 期待 JSON：MBps > 2（4K120 码率约 1-2 MB/s 留 2 倍余量）、Error 为空
```

注意：spike 上报地址硬编码在 `spike-tvos/app/Sources/Config.swift`（`reportURL`，Mac 的 LAN IP），
换网络要改它重装。判据来自设计文档：spike 已验证引擎 c-archive 入网 + overlay 拉流通；Mac 对照组
本机回环有 ~0.1MB/s 测量失真，**TV 端真实吞吐数字以本次采集为准**，达标（≥2MB/s）才继续后续
Phase，不达标停下讨论 LAN 直连 fallback。
