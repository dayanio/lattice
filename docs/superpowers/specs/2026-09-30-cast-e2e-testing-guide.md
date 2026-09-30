# Cast E2E 测试手册（给 AI 助手的可执行版）

**日期**：2026-09-30
**适用**：`spike/cast-gateway-phase0`（lattice-apple）+ `dev`（reflux）+ lattice `docs/cast-gateway-design` 分支上的投屏栈。
**已验证基线**：93MB 小文件与 8.8GB 4K120 HDR 大文件全链（面板发起 → 通知 → 点击 → Reflux 从 Mac 文件服务 Range 拉流真实播放，含拖动进度条 seek）在真机上通过（2026-09-30）；大文件依赖 PlayerKit 帧内存预算与 seek 修复，见「已知坑」与 spike 文档附二。

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
