# macOS/iOS 引擎集成切换：gomobile → c-archive（路线统一）

- 日期：2026-10-05
- 状态：已批准，实施中
- 关联：`2026-09-30-tvos-cast-design.md`（tvOS c-archive 先例）、`2026-10-04-tvos-engine-ne-design.md`（tvOS NE 现状）、ADR-0005（FERRY）

## 1. 背景与动机

三平台引擎集成路线分裂：

| 平台 | 现路线 | 产物 |
|------|--------|------|
| macOS / iOS | gomobile bind（自动 ObjC binding） | `apple/Frameworks/{iOS,MacOS}/LatticeCore.xcframework`（动态 framework 壳包静态 ar） |
| tvOS | `-buildmode=c-archive` + 手写 C ABI（`apple/engine/tvoslib`） | `apple/build/tvos/LatticeTVCore.a` |

tvOS 线实测证明 c-archive 更轻更可控：构建链一条命令、FFI 面 ~200 行可读完、无 binding 黑盒。gomobile 的痛点是排障黑盒——9/30 的 cast 断流事故（autoreleasepool / NE jetsam / socketpair）排查时 gomobile 桥接地带不可见；且 gomobile 不支持 tvOS，路线天然统一不了。

本设计把 macOS/iOS 也切到 c-archive，三平台统一为「手写窄 C ABI + 静态库」一条路线。

**约束（用户拍板，必须遵守）**：只换引擎打包/桥接方式，不动产品架构——
- macOS/iOS 保持「lattice 独立 App（引擎在 NE appex）+ 收 cast 唤醒 reflux 播放」的双 App 模式；
- tvOS 保持 reflux 内嵌现状，本次完全不碰 tvoslib / embedded / tvOS 相关代码。

## 2. 决策

### D1：绑定 `apple/engine`（Core 包），不是 embedded

两个引擎平行非子集：

| 能力 | `apple/engine`（Core） | `apple/engine/embedded` |
|------|----------------------|------------------------|
| packetTUN socketpair 桥 | ✅ | ❌（gVisor 全用户态） |
| 47822 cast 主路 | ✅ | 仅 NATS 兜底 |
| LatticeDNS / OnPeerStates / OnRoutesChanged | ✅ | ❌ |
| SetSplitRouting / SetUpstreamDNS | ✅ | ❌ |
| IPv6 overlay / BindInterface / 身份自持久化 | ✅ | 部分 |

macOS/iOS 客户端全部能力在 Core 里，切 embedded 会丢功能。**保留 NE 数据面架构（tunFD socketpair + SendPacketBatch 4 字节 LE 帧）原样不动，只把 gomobile binding 层换成手写 C ABI。**

### D2：产物 = c-archive 各 slice → static xcframework（同路径替换）

- 编 2 个 slice：iOS device（arm64，GOOS=ios）、macOS（arm64，GOOS=darwin；x86_64 需要时加一条命令）。
- 不编 iOS 模拟器 slice：NE 不支持模拟器；同时把 App 主 target 的引擎 framework 依赖解绑（App 零符号引用，见 D4），模拟器构建不再需要它。
- 旧 `apple/Frameworks/` 先备份为 `apple/Frameworks-gomobile-backup/`，新产物写同路径 `apple/Frameworks/{iOS,MacOS}/LatticeCore.xcframework`（static library 型）→ lattice-apple 的 `engine_env.sh` / `FRAMEWORKS_DIR` 零改动，回退 = 换回备份目录。
- gomobile 依赖（`golang.org/x/mobile`）最终从 go.mod 移除（Phase D）。

### D3：FFI 设计（新包 `apple/engine/corelib`，package main，照 tvoslib 模式）

进程级单例 + 4 个回调全是「一个字符串」签名（OnEvent/OnTunnelUp/OnPeerStates/OnRoutesChanged 都只带一个 string），所以一个 C fn 指针类型通吃：

```c
typedef void (*CoreCallbackFn)(const char* value, void* ctx);
```

导出面（12 个 export）：

```go
CoreResetIdentity() C.int                                 // 包级，New 前调用
CoreNewEngine(cfg *C.char) *C.char                        // 错误串 or nil；存单例
CoreSetCallbacks(onEvent, onTunnelUp, onPeerStates, onRoutesChanged unsafe.Pointer,
                 ctx unsafe.Pointer)
CoreStart() *C.char                                       // 异步启动，事件回调驱动
CoreStop()                                                // 阻塞至 run loop 退出
CorePublicKey() *C.char
CorePeers() *C.char                                       // netmap JSON，Swift 用完 CoreFree
CoreSetUpstreamDNS(dns *C.char) *C.char
CoreSetSplitRouting(on C.int) *C.char
CorePublishCastCommand(appID, payload *C.char) *C.char
CoreSendPacketBatch(data *C.char, length C.int) C.int     // 零拷贝，不持有
CoreFree(p *C.char)
```

要点：

- **EngineDelegate cgo 适配器**（~100 行）：实现 4 个回调，payload 均 `*C.char`。字符串生命周期照 tvoslib `TVEventFn + ctx` 纪律：Go 侧 `C.CString` → 同步调 C 函数 → 返回后 `C.free`，Swift 侧只在回调内同步消费、不存指针。
- **零拷贝纪律**：`CoreSendPacketBatch` 用 `unsafe.Slice` 包住调用方指针直接进 `Engine.SendPacketBatch`——该方法同步消费（循环解析帧写入 wireguard-go，无异步保留，已核对 engine.go:202-232），指针不持有。Swift→Go 单向零拷贝安全。
- **GC root 表**：SendPacketBatch / 回调均无跨 FFI 对象移交，不需要 tvoslib 的 media handle 式注册表；单例指针 `mu` 保护即可。
- **回调并发**：OnPeerStates 2s 高频、可能在任意 engine goroutine 触发；桥接结构体 RWMutex 保护 fn 指针与 ctx，set 与 call 互斥。

### D4：Swift 侧——共享桥接 shim，两个 PTP 换芯

- 新增 `Shared/CoreEngineBridge.swift`（~100 行，lattice-apple 仓）：包装 C ABI，暴露 `start(cfgJSON:callbacks:)/stop()/peers()/publicKey()/setSplitRouting()/setUpstreamDNS()/sendPacketBatch()/publishCastCommand()`，回调转 Swift 闭包。
- `LatticeTunnel/PacketTunnelProvider.swift` 与 `LatticeTunnelMac/PacketTunnelProvider.swift`：gomobile 构造段 / 查询段 / delegate extension（各 ~150 行触点）替换为 bridge 调用；**其余全部原样保留**（socketpair 循环、路由数学、DNS、TunnelLog、bindInterface、enableIPForwarding、provider message IPC）。
- `Shared/TunnelManager.swift` 的 App↔扩展 provider-message 协议：**零改动**（纯 Swift）。
- 两个 PTP target 加 `SWIFT_OBJC_BRIDGING_HEADER`（新 `Shared/CoreLibBridge.h`，`#include "LatticeCore.h"`——构建脚本把 c-archive 产出的头重命名放置）。
- 顺手清理：App 主 target（Lattice/LatticeMac）解绑 LatticeCore framework（零符号引用，纯死重）；Mac App 的 `Restore LatticeCore engine binary` postBuild 删除（static 无 Xcode 26 stub 问题）；`ENABLE_DEBUG_DYLIB: NO` 保守保留。

### D5：构建与指纹机制

- lattice 仓：`apple/Scripts/build_core_libs.sh`（照 `build_tvos_lib.sh` 骨架；xcframework 拼装用 `xcodebuild -create-xcframework` 的 static library 形态）+ Makefile `core-libs` target。
- 关键参数：
  - `GOTOOLCHAIN=go1.26.8` 钉死——多 slice 必须同版本，防 Go runtime 符号冲突；
  - iOS：`CC="clang -isysroot <iphoneos SDK> -target arm64-apple-ios17.0"`；
  - macOS：`CC="clang -isysroot <macosx SDK> -target arm64-apple-macos26.0"`（随 project.yml deploymentTarget）;
  - 显式 `-target` 必须带：只给 `-isysroot` 时 clang driver 会把 sysroot 和推断 target 配错对（tvoslib 脚本注释的 "using sysroot for 'AppleTVOS' but targeting 'iPhone'" 教训）。
- 产物布局（与现 gomobile 布局同路径）：

```
apple/Frameworks/iOS/LatticeCore.xcframework/
  Info.plist
  ios-arm64/
    LatticeCore.a          ← c-archive 静态归档
    Headers/LatticeCore.h  ← c-archive 产头重命名
apple/Frameworks/MacOS/LatticeCore.xcframework/
  Info.plist
  macos-arm64/
    LatticeCore.a
    Headers/LatticeCore.h
```

- lattice-apple 仓：`engine_env.sh` 的 build 分支从 `gomobile bind` 换成调 lattice 仓 `make core-libs`；engine fingerprint（引擎源 git tree hash）机制**原样保留**（与绑定方式无关），stamp 仍写 xcframework 旁；`ensure_engine_framework.sh` check 逻辑不变；scheme preAction 照旧自动重建。

## 3. 实施阶段

### Phase A — Go 侧 corelib + 构建（lattice 仓）

1. 新增 `apple/engine/corelib/corelib.go`：D3 的 12 个 export + EngineDelegate cgo 适配器。
2. `apple/Scripts/build_core_libs.sh`：ios-device + macos-arm64 两个 slice → static xcframework；首次运行把旧 `apple/Frameworks/` 备份为 `apple/Frameworks-gomobile-backup/`；写 stamp。
3. Makefile 加 `core-libs` target。
4. 本地验证：产物 `file` 显示 ar archive、`nm` 有 Core* 符号。

### Phase B — Mac 先行切换（lattice-apple 仓）

5. project.yml：LatticeTunnelMac 加桥接头；LatticeMac embed→false、删 Restore postBuild、解绑 framework；Verify engine phase 保持。
6. `Shared/CoreEngineBridge.swift` + `Shared/CoreLibBridge.h`；`LatticeTunnelMac/PacketTunnelProvider.swift` 换芯。
7. engine_env.sh 适配 build 命令。
8. **真机验证（Mac）**：xcodegen → 构建 → 装 LatticeMac → 入网（peer 状态、overlay IP）→ 国直分流开关 → 出口节点 → 发起投屏到电视 → 退隧道重连。

### Phase C — iOS 切换（手机在手边）

9. `LatticeTunnel/PacketTunnelProvider.swift` 同样换芯；project.yml LatticeTunnel 加桥接头、Lattice iOS 解绑。
10. **真机验证（iPhone）**：装 Lattice → 入网 → peer 列表/分流 → cast 面板 → 出口。
11. tvOS 回归冒烟：电视不动，Mac 发投屏确认链路仍通（corelib 与 tvoslib 编译隔离，理论零影响）。

### Phase D — 清理与收尾

12. lattice 仓：go.mod 删 `golang.org/x/mobile`；`apple/engine/engine.go` 去掉 `_ "golang.org/x/mobile/bind"` import 与相关注释；确认 tvoslib/embedded 不受影响。
13. lattice-apple 仓：删 `apple/EmbeddedKit/`（死线：无消费、测试编不过）及 `embedded` 相关构建 target；`try_latest_engine.sh` 适配新指纹流程。
14. docs：`docs/guide/build-apple-apps.md` 更新构建说明（core-libs 替代 gomobile 叙述）。

## 4. 验证清单（真机集成测试）

| # | 项 | 判据 |
|---|---|---|
| 1 | core-libs 产物 | ar archive、Core* 符号、双 slice stamp 一致 |
| 2 | Mac 入网 | overlay IP 到手、peer 状态 2s 刷新（OnPeerStates 高频回调无泄漏/卡顿） |
| 3 | Mac 分流/出口 | 国直开关生效、出口节点可用 |
| 4 | Mac→电视投屏 | cast 命令发布成功、电视收到（cast 主案结案验证一并覆盖） |
| 5 | iPhone 入网/面板 | 同 2/3 |
| 6 | 数据面 | SendPacketBatch 帧格式不变（ping 走隧道、吞吐与切换前持平） |
| 7 | tvOS 冒烟 | 电视 cast 接收不受影响 |
| 8 | 回退演练 | 换回 backup 目录 → Mac 构建通过（保底逃生门验证一次） |

## 5. 风险与对策

| 风险 | 对策 |
|------|------|
| 回调跨 goroutine 的 C ABI（OnPeerStates 2s 高频） | 照抄 tvoslib TVEventFn 模式；字符串同步消费即 free；Phase B 先在 Mac 上长时间挂机观察 |
| SendPacketBatch 零拷贝破坏（TunnelCore.swift 有 5% 丢包事故教训） | 帧格式与 unsafe.Slice 边界不拷贝写死；验证 #6 对比切换前吞吐 |
| 错误传播语义变化（NSError → *C.char） | bridge 层统一转 String?，PTP 调用点语义一一对应 |
| 静态库符号冲突/链接失败 | GOTOOLCHAIN 钉死；先单 slice（macOS arm64）打通再扩 |
| Mac 无 x86_64 slice | 用户 Mac 为 arm64；需要时脚本加编一条 |
| 期间 cast 主案被阻塞 | 无阻塞：电视侧（tvoslib/embedded）零改动；Mac 发送端验证借 Phase B 一起做 |

## 6. 提交

- lattice 仓：`feat(engine): corelib — Core 引擎的 c-archive C ABI 与多 slice 构建`（Phase A）+ `chore(engine): drop gomobile dependency`（Phase D）。
- lattice-apple 仓：`feat(tunnel): engine bridge — gomobile binding 换 c-archive C ABI`（Phase B+C 一并）+ `chore: remove EmbeddedKit dead line`（Phase D）。
- 各仓 conventional commit + `-s`、不加 Co-Authored-By、提交即 push；lattice 仓提交前 `GOTOOLCHAIN=go1.26.8 make lint`。
