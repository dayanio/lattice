# Building the Apple Apps（Reflux 客户端构建指南）

Reflux 的 Apple 客户端（iOS / macOS / tvOS）源码在 reflux 仓库的 `apple/` 目录下，其中 tvOS 版内嵌 Lattice 引擎作为投屏接收端。本文说明从零编译并装机的完整流程。

## 仓库布局

构建涉及两个仓库，需**并排放置**（tvOS 工程按相对路径 `../../lattice/...` 引用引擎静态库）：

```
workspc/
├── lattice/    # 引擎 + 控制面（本仓库）
│   └── apple/build/tvos/LatticeTVCore.a   ← make tvos-lib 产物
└── reflux/
    └── apple/  # Apple 客户端（XcodeGen 工程）
        ├── project.yml
        └── tvOS/
            ├── Cast/        # App 侧：配对、投屏管理、IPC 编解码
            └── Tunnel/      # 隧道扩展（引擎运行处）
```

## 环境要求

| 依赖 | 用途 | 说明 |
|------|------|------|
| Xcode 15.4+ | 编译 | 需含 iOS / macOS / tvOS SDK |
| Go 1.26.x | 引擎静态库 | `GOTOOLCHAIN=go1.26.8`（脚本已内置） |
| [XcodeGen](https://github.com/yonaskolb/XcodeGen) | 生成 .xcodeproj | `brew install xcodegen` |
| Apple 开发者账号 | 真机签名 | `project.yml` 已配置开发团队与各 target 的 bundle id |

本地 SPM 依赖（XcodeGen 自动解析，无需手工操作）：

- `Packages/RefluxPlayerKit` — 播放内核
- `Packages/FFmpegKitLocal` — FFmpeg 封装。**首次构建前**需执行一次：
  ```bash
  cd reflux/apple/Packages/FFmpegKitLocal && ./build_xcframework.sh
  ```

## Step 1：构建 Lattice 引擎静态库（仅 tvOS 需要）

在 lattice 仓库根目录：

```bash
make tvos-lib
```

产物：`apple/build/tvos/LatticeTVCore.a` + `LatticeTVCore.h`（c-archive，`GOOS=ios GOARCH=arm64`，target `arm64-apple-tvos17.0`）。脚本 `apple/Scripts/build_tvos_lib.sh` 负责全部编译细节，包括用 appletvos SDK 的 clang 作为 CGO 编译器——这是 iOS 系交叉编译 Go 到 tvOS 的关键。

> 重新拉取 lattice 代码或引擎逻辑变更后需重新执行；纯 App 改动不需要。

## Step 2：生成 Xcode 工程

```bash
cd reflux/apple
xcodegen generate
```

生成 `RefluxApple.xcworkspace`。target 一览：

| Target | 平台 | 说明 |
|--------|------|------|
| `RefluxApple` | iOS 17.0+ | iPhone / iPad 播放器 + 投屏发送端 |
| `RefluxAppleMac` | macOS 14.0+ | Mac 播放器 + 投屏发送端（Designed for iPad 之外的独立 target） |
| `RefluxAppleTV` | tvOS 17.0+ | Apple TV 播放器（UI 壳） |
| `RefluxAppleTVTunnel` | tvOS 17.0+ | NetworkExtension 隧道扩展，**引擎在此进程运行**，内嵌于 TV App |

## Step 3：编译

```bash
# tvOS（真机 only：NetworkExtension 不支持模拟器）
xcodebuild -workspace RefluxApple.xcworkspace -scheme RefluxAppleTV \
  -destination 'generic/platform=tvOS' build

# iOS / macOS 同理，换 scheme 与 destination
```

也可直接在 Xcode 里选 target 后 Run。

## Step 4：真机安装（tvOS 示例）

```bash
# 列出已配对的设备
xcrun devicectl list devices

# 安装（DEVICE_ID 取上一步输出）
xcrun devicectl device install app --device <DEVICE_ID> \
  ~/Library/Developer/Xcode/DerivedData/RefluxApple-*/Build/Products/Debug-tvos/RefluxAppleTV.app

# 启动（--terminate-existing 保证替换旧进程；--console 可实时抓 NSLog）
xcrun devicectl device process launch --console --terminate-existing \
  --device <DEVICE_ID> io.reflux.apple.tv
```

> 启动失败提示 "System is asleep" 时先用遥控器唤醒电视再重试。

## tvOS 首次入网（配对）

1. 电视上打开 Reflux → 出现配对二维码
2. 手机 Reflux 扫码（手机与电视需加入同一 Lattice 网络）
3. 电视弹出 **VPN 配置允许弹窗 → 点允许**（创建隧道 profile 的系统要求）
4. App 显示"正在加入 Lattice…"数秒后进入主页，即入网成功

之后引擎常驻在隧道扩展进程里，由系统保活：App 退后台甚至被杀，电视仍能接收投屏。

## 常见问题

**`xcodebuild` 报找不到 `LatticeTVCore.a`**
Step 1 没跑，或两仓库没有并排放置。检查 `reflux/apple` 的同级目录下是否有 `lattice/apple/build/tvos/LatticeTVCore.a`。

**Go 交叉编译报 "using sysroot for 'AppleTVOS' but targeting 'iPhone'"**
不要绕过 `make tvos-lib` 手工编译——脚本里的 `-target arm64-apple-tvos17.0` 显式声明是必须的。

**电视上保存 VPN 配置报 permission denied**
tvOS 对隧道 profile 的创建需要用户在弹窗中确认，且批准会过期。删除 App 重装或等待弹窗重新出现后再点一次允许。

**投屏后电视没反应**
见 [Cross-network Cast（投屏链路）](/guide/cast) 的故障排查一节。
