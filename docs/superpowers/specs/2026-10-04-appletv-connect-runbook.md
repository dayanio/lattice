# Apple TV 真机连接 Runbook（Xcode / devicectl 发现不了设备的修复记录）

**日期**：2026-10-04
**状态**：已修复并验证（装机 + 启动成功）
**适用**：Mac ↔ Apple TV 无线开发连接（配对/枚举/装机）；电视无 USB-C 口，只能走无线
**相关**：`2026-09-30-cast-e2e-testing-guide.md` §8、`plans/2026-09-30-tvos-cast.md`

---

## 0. 一句话结论

**电视和网络都没问题，卡的是 Mac 的发现守护进程。** 一条命令重启它们即可恢复：

```bash
pkill -f "RemotePairing.framework.*remotepairingd"
pkill -f "CoreDevice.framework.*CoreDeviceService"
# launchd 会自动拉起（新 PID，无需 sudo）；若 Xcode 开着，顺手重启 Xcode
```

本次实测：重启后约 1 秒两个进程以新 PID 出现，`devicectl list devices` 立即出现
`主卧 … available (paired)`，隧道 `tunnelState: connected`，装机、启动一次成功。

---

## 1. 症状

- Xcode 26.3 的 Devices and Simulators 窗口**看不到** Apple TV；
  `xcrun devicectl list devices` 里没有电视（只有 iPhone）。
- 电视端已停在「设置 → 遥控器与设备（Remote App and Devices）」页，且网络互通
  （IPv4/IPv6 ping 均通，配对端口 TCP 可达）。
- 历史失败（9/27–10/1）：`error 4000（连接无法建立）`、`tunnelState: unavailable`。

## 2. 先分清是哪一侧的问题（本次的关键教训）

### 2.1 电视侧：停在配对页时广播正常

```bash
dns-sd -B _remotepairing-manual-pairing._tcp local     # 应看到实例名 = 电视名（如「主卧」）
dns-sd -L 主卧 _remotepairing-manual-pairing._tcp local
# 10:23 … 主卧._remotepairing-manual-pairing._tcp.local. can be reached at zhuwo.local.:50724
#   identifier=69AFE7C6-… authTag=… model=AppleTV11,1 name=主卧 ver=24 minVer=17
dns-sd -B _apple-pairable._tcp local                   # 同一台电视也会广播这条
```

### 2.2 陷阱：iPhone 也会广播 `_remotepairing._tcp`

`dns-sd -B` 只给实例名（UUID），**必须用 `-L` 解析出主机名再判断是谁**：

| 实例解析目标 | 设备 |
|---|---|
| `zhuwo.local`（电视名 zhuwo / 主卧） | ✅ Apple TV |
| `winston.local` | ❌ 是 iPhone（本次排查中差点被当成电视） |

### 2.3 Mac 侧：dns-sd 看得到、CoreDevice 看不到 ⇒ 守护进程卡死

```bash
xcrun devicectl list devices        # 修复前：只有 iPhone，没有电视
xcrun devicectl manage pair --device 主卧
# 修复前：ERROR … The specified device was not found … (CoreDeviceError error 1000)
```

同一个 mDNS 世界，`dns-sd` 能看到、CoreDevice（Xcode 与 devicectl 共用的发现栈）看不到，
即可判定为 **Mac 侧发现守护进程的状态问题**（本次 `remotepairingd` 已连续运行 5 天半未重启）。

## 3. 修复

```bash
pkill -f "RemotePairing.framework.*remotepairingd"
pkill -f "CoreDevice.framework.*CoreDeviceService"
```

- 均为当前用户进程，`launchd` 按需自动拉起，无需 sudo；
- 若 Xcode 开着，建议重启 Xcode（它缓存的设备列表不会自动刷新）。

## 4. 修复后的验证（按顺序）

```bash
xcrun devicectl list devices
# 期待：
# Name    Hostname                       Identifier                             State                Model
# 主卧     zhuwo-1.coredevice.local       BB895BFC-6197-5C56-B8C8-C49A6FC0863B   available (paired)   AppleTV11,1

xcrun devicectl device info details --device BB895BFC-6197-5C56-B8C8-C49A6FC0863B
# 期待：
#   pairingState: paired        tunnelState: connected
#   transportType: localNetwork tunnelTransportProtocol: tcp
#   ddiServicesAvailable: true  developerModeStatus: enabled
```

两个"看起来像错误其实是好信号"的输出：

- `manage pair` 报 **4001 "Manual pairing … already in progress"** = 设备已被 CoreDevice 认识且已配对；
- `--device 主卧` 报 **"multiple devices have the name '主卧'"** = 名字重名，改用 identifier
  `BB895BFC-6197-5C56-B8C8-C49A6FC0863B` 即可。

## 5. 装机（本次用它完成验证）

```bash
cd ~/workspc/reflux/apple
# 如需重构建：
#   git checkout feat/tv-cast && xcodegen generate
#   xcodebuild -project RefluxApple.xcodeproj -scheme RefluxAppleTV -configuration Debug \
#     -destination 'generic/platform=tvOS' -derivedDataPath build build
xcrun devicectl device install app --device BB895BFC-6197-5C56-B8C8-C49A6FC0863B \
  build/Build/Products/Debug-appletvos/RefluxAppleTV.app

# 启动（看配对页）：
xcrun devicectl device process launch --device BB895BFC-6197-5C56-B8C8-C49A6FC0863B \
  --terminate-existing io.reflux.apple.tv
```

本次实测：60MB Debug 包经本地网络安装成功；电视上现有 bundle：
`io.reflux.apple.tv`（RefluxAppleTV，本仓 tip 构建）与 `io.lattice.cast.renderer`（LatticeCast）。

## 6. 设备档案（本次环境，换 Mac/换网络时先复核）

| 项 | 值 |
|---|---|
| 电视 | 主卧 · Apple TV 4K (2nd gen) · AppleTV11,1 · tvOS 26.6 |
| 电视 devicectl id | `BB895BFC-6197-5C56-B8C8-C49A6FC0863B` |
| 电视主机名 | `zhuwo.local`（CoreDevice 显示 `zhuwo-1.coredevice.local`）· Wi-Fi 192.168.1.10 |
| Mac | Xcode 26.3 (17C529) · macOS 26.6.2 (25G83) · en0 192.168.1.8 |
| 配对方式 | localNetwork / TCP 隧道（无 USB 口，只能无线） |
| 备注 | Mac 上 Clash Verge TUN 常开（exclude 10.96.0.0/16 等），不影响局域网发现与隧道 |

## 7. 下次"连不上"的 60 秒排查清单

1. **电视**：唤醒并停在「设置 → 遥控器与设备」页，**别退出、别进屏保**（离开该页配对服务即停止广播）；
2. **广播**：`dns-sd -B _remotepairing-manual-pairing._tcp local` + `dns-sd -L <电视名> …`，
   确认指向 `zhuwo.local`（而不是 `winston.local` 那台 iPhone）；
3. **Mac**：`xcrun devicectl list devices` —— 没有电视或 `manage pair` 报 1000 ⇒ 执行 §3 的 pkill 重启；
4. **配对状态**：隧道恢复后直接装机（§5），不再需要 Xcode 里点 Pair；
5. 历史 `error 4000` / `tunnelState: unavailable` 的复现基本都是"配对页不在"或"守护进程卡死"两类原因，
   按本清单第 1、3 步处理。
