# macOS 面板与窗口 UI 重设计 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 macOS 客户端改成"首屏只放状态 + 设备 + 导航栏，其余进二级页面"，并让所有弹窗都有关闭入口。

**Architecture:** 面板与主窗口共用 `ContentView`。二级页面沿用现有的"同一容器内原地替换 + 返回"机制，用一个枚举状态 `subPage: PanelPage?` 取代 `showingNetworkSettings` / `showingShare`。可测试的纯逻辑（页面、返回关系、面板内是否需转主窗口）放进 Foundation-only 的 `PanelRoute.swift`；界面组件（`PageHeader`、`SheetScaffold`、`StateView`、`PanelNavBar`）放进新文件 `PanelComponents.swift`。

**Tech Stack:** Swift / SwiftUI（macOS 14+）、XcodeGen（`apple/project.yml`）、Foundation-only 逻辑测试脚本（`apple/Scripts/test_apple_logic.sh`）。

**Spec:** `docs/superpowers/specs/2026-09-22-mac-panel-redesign-design.md`

## Global Constraints

- **只改 macOS 端**：不修改 `apple/Shared/DesignComponents.swift`、`apple/Lattice/`（iOS）、`frontend/`、Go 代码。
- **单次提交**：按 `CLAUDE.md`，一个功能只做一个 commit。任务 1–7 只构建和验证，**不要 `git commit`**，只在任务 8 统一提交。提交用 `git commit -s`，**不加** `Co-Authored-By`（`CLAUDE.md` 优先于工具默认的署名提示）。
- **每个 Swift 文件以 Apache 2.0 许可头开头**，与现有文件完全相同（`// Copyright 2026 The Lattice Authors, Inc.` 起共 13 行）。
- **尺寸**：面板保持宽 340、高 380–560；主窗口宽 360、高 480–900；所有 sheet 宽 340、内边距 20；二级页面顶栏高 44。
- **界面文案用中文**。
- **构建命令**（下文简称 `BUILD`），在 `/Users/francis/workspc/lattice/apple` 下执行：

```bash
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug \
  -destination 'platform=macOS' -derivedDataPath build/mac-dd \
  CODE_SIGNING_ALLOWED=NO build 2>&1 | grep -E "error:|BUILD (SUCCEEDED|FAILED)"
```

  期望输出只有一行 `** BUILD SUCCEEDED **`。基线（改动前）已验证可通过。
- **逻辑测试命令**（下文简称 `LOGIC`）：`cd /Users/francis/workspc/lattice/apple && bash Scripts/test_apple_logic.sh`，期望最后一行 `apple logic: all checks passed`。基线已验证通过。

## 与设计文档的偏差（执行前需知会用户）

设计文档没有覆盖、但实现中必须处理的细节，已在本计划中做了如下决定：

1. `⋯` 菜单里**额外**有"刷新设备列表"：被移除的 `footer` 里原有"刷新"按钮，不能丢。
2. 被移除的 `footer` 里还显示 `opError`（重命名 / 删除等操作失败提示）。改为设备列表与导航栏之间的**可关闭红色提示条**。
3. 页面枚举做成顶层类型 `PanelPage`（Foundation-only，便于测试），`UIState.Page` 变成它的 `typealias`，效果等同于设计文档说的"UIState.Page 新增 cast、castPairing"。
4. 三个 `.sheet`（登录 / 设置 / 加入）和跨窗口请求的 `.onAppear` / `.onChange` **上移**到 `ContentView.body` 的容器上：否则主窗口停在二级页面时，这些请求没人消费。
5. 面板里"连接开关（未配置时）""尚未加入"按钮、"登录以查看和管理设备"按钮原本会在面板内弹 sheet，一并改为转主窗口（设计文档目标 4：面板内不再弹任何 sheet）。
6. 扫码弹窗去掉底部"取消"按钮，由右上角 `✕` 和 `Esc` 取代；`ManageLoginView` 保留底部"取消"（它和"登录"成对）。
7. 主窗口 `Window` 增加 `.defaultSize(width: 360, height: 640)`，否则放宽高度范围后初始高度可能落在最小值。

## File Structure

| 文件 | 职责 | 操作 |
|---|---|---|
| `apple/LatticeMac/PanelRoute.swift` | `PanelPage` 枚举：标题、返回关系、面板内是否需转主窗口。Foundation-only | 新增 |
| `apple/LatticeMac/PanelComponents.swift` | `PageHeader`、`SheetScaffold`、`StateView`、`PanelNavItem` / `PanelNavBar`、`WindowHiddenObserver` | 新增 |
| `apple/LatticeMac/CastReceiver.swift` | 新增 `CastPage`、`CastPairingView`；删除 `CastSectionView`、`CastPairingSheet` | 修改 |
| `apple/LatticeMac/NetworkPages.swift` | 顶栏改 `PageHeader`；退出节点选择改页内列表 | 修改 |
| `apple/LatticeMac/ContentView.swift` | 首屏重构、`⋯` 菜单、`subPage`、4 个弹窗套 `SheetScaffold` | 修改 |
| `apple/LatticeMac/LatticeMacApp.swift` | `UIState.Page` 别名、`MenuBarPanel` 去页脚、主窗口尺寸 | 修改 |
| `apple/Tests/AppleLogicTests.swift` | `PanelPage` 逻辑检查 | 修改 |
| `apple/Scripts/test_apple_logic.sh` | 编译列表加入 `PanelRoute.swift` | 修改 |
| `apple/LatticeApple.xcodeproj/project.pbxproj` | `xcodegen generate` 重新生成 | 修改 |

---

### Task 1: `PanelPage` 纯逻辑（TDD）

**Files:**
- Create: `apple/LatticeMac/PanelRoute.swift`
- Modify: `apple/Tests/AppleLogicTests.swift`（在 `var coordinatorDone = false` 之前插入）
- Modify: `apple/Scripts/test_apple_logic.sh`（编译列表）

**Interfaces:**
- Produces（后续任务依赖，名称与签名必须一致）：
  - `enum PanelPage: String, CaseIterable, Equatable`，case：`networkSettings`、`share`、`cast`、`castPairing`
  - `var title: String`
  - `var parent: PanelPage?`：返回目标，`nil` 表示回首屏
  - `var needsTextInput: Bool`
  - `func destination(inPanel: Bool) -> PanelDestination`
  - `enum PanelDestination: Equatable { case inPlace, mainWindow }`

- [ ] **Step 0（可选，人工，改动前）：复现设计文档里的疑似缺陷**

先用基线构建产物验证"面板里点配对信息会怎样"。这一步需要有人操作界面，无法自动化；跳过也不影响后续任务。

```bash
cd /Users/francis/workspc/lattice/apple && \
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug \
  -destination 'platform=macOS' -derivedDataPath build/mac-dd CODE_SIGNING_ALLOWED=NO build 2>&1 | grep -E "BUILD (SUCCEEDED|FAILED)" \
&& open build/mac-dd/Build/Products/Debug/LatticeMac.app
```

点菜单栏图标 → 点"配对信息…" → 观察：弹窗是否出现、有没有关闭按钮、点输入框会不会失焦、点面板外面板是否消失。把观察结果记下来，放进任务 8 的 commit 说明。

- [ ] **Step 1: 写失败的测试**

在 `apple/Tests/AppleLogicTests.swift` 里，找到 `var coordinatorDone = false` 这一行，在它**前面**插入：

```swift
// MARK: PanelPage

do {
    eq(PanelPage.castPairing.parent, PanelPage.cast, "the pairing form goes back to the cast page")
    eq(PanelPage.cast.parent, nil, "the cast page goes back to the first screen")
    eq(PanelPage.networkSettings.parent, nil, "network settings goes back to the first screen")
    eq(PanelPage.share.parent, nil, "share goes back to the first screen")
}
do {
    eq(PanelPage.castPairing.destination(inPanel: true), PanelDestination.mainWindow,
       "a typing page opened from the panel goes to the main window")
    eq(PanelPage.castPairing.destination(inPanel: false), PanelDestination.inPlace,
       "a typing page opened in the main window stays in place")
    for page in PanelPage.allCases where page != .castPairing {
        eq(page.destination(inPanel: true), PanelDestination.inPlace,
           "\(page.rawValue) opens in place inside the panel")
        eq(page.destination(inPanel: false), PanelDestination.inPlace,
           "\(page.rawValue) opens in place in the main window")
    }
}
do {
    eq(PanelPage(rawValue: "networkSettings"), PanelPage.networkSettings, "raw values are stable")
    eq(PanelPage(rawValue: "share"), PanelPage.share, "raw values are stable")
    eq(PanelPage(rawValue: "cast"), PanelPage.cast, "raw values are stable")
    eq(PanelPage(rawValue: "castPairing"), PanelPage.castPairing, "raw values are stable")
    check(PanelPage(rawValue: "nope") == nil, "an unknown raw value is rejected")
    for page in PanelPage.allCases {
        check(!page.title.isEmpty, "\(page.rawValue) has a title")
    }
    eq(Set(PanelPage.allCases.map(\.title)).count, PanelPage.allCases.count, "titles are unique")
}

```

- [ ] **Step 2: 让测试脚本编译新文件，并确认它失败**

编辑 `apple/Scripts/test_apple_logic.sh`，把 `swiftc` 那一行改为：

```bash
swiftc -o "$TMP/apple_logic_tests" Shared/JoinPayload.swift Shared/TunnelCore.swift LatticeMac/PanelRoute.swift "$TMP/main.swift"
```

同时把脚本第 2 行注释末尾的检查清单补上"面板页面路由"。

Run: `cd /Users/francis/workspc/lattice/apple && bash Scripts/test_apple_logic.sh`
Expected: FAIL，报错含 `no such file or directory: 'LatticeMac/PanelRoute.swift'`。

- [ ] **Step 3: 写最小实现**

创建 `apple/LatticeMac/PanelRoute.swift`（文件头用与其他 Swift 文件相同的 Apache 2.0 许可头）：

```swift
import Foundation

/// A secondary page of the main panel / main window. Pure Foundation so the
/// routing rules can be unit-checked without SwiftUI.
enum PanelPage: String, CaseIterable, Equatable {
    case networkSettings
    case share
    case cast
    case castPairing

    var title: String {
        switch self {
        case .networkSettings: return "网络设置"
        case .share: return "共享本地服务"
        case .cast: return "投屏接收"
        case .castPairing: return "配对信息"
        }
    }

    /// Where "back" goes; nil means the first screen.
    var parent: PanelPage? {
        switch self {
        case .castPairing: return .cast
        case .networkSettings, .share, .cast: return nil
        }
    }

    /// Pages with text fields cannot live in the menu-bar panel: it is not a
    /// key window, so fields lose focus and a click outside dismisses it.
    var needsTextInput: Bool { self == .castPairing }

    func destination(inPanel: Bool) -> PanelDestination {
        inPanel && needsTextInput ? .mainWindow : .inPlace
    }
}

enum PanelDestination: Equatable {
    case inPlace
    case mainWindow
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `LOGIC`
Expected: `apple logic: all checks passed`

---

### Task 2: 界面组件 `PanelComponents.swift`

**Files:**
- Create: `apple/LatticeMac/PanelComponents.swift`
- Modify: `apple/LatticeApple.xcodeproj/project.pbxproj`（`xcodegen generate` 生成）

**Interfaces:**
- Consumes: `LatticePalette.online`（`apple/Shared/DesignComponents.swift` 已有）
- Produces（后续任务依赖）：
  - `PageHeader<Accessory: View>(title: String, onBack: @escaping () -> Void, @ViewBuilder accessory: () -> Accessory)`，以及 `Accessory == EmptyView` 时的 `PageHeader(title:onBack:)`
  - `SheetScaffold<Content: View>(title: String, onClose: @escaping () -> Void, @ViewBuilder content: () -> Content)`
  - `StateView(icon: String? = nil, iconColor: Color = .secondary, title: String, message: String? = nil, actionTitle: String? = nil, prominent: Bool = false, isLoading: Bool = false, action: (() -> Void)? = nil)`（参数顺序即声明顺序，调用时必须按此顺序）
  - `PanelNavItem(id: String, icon: String, title: String, showsDot: Bool = false, action: @escaping () -> Void)`
  - `PanelNavBar(items: [PanelNavItem])`

- [ ] **Step 1: 创建组件文件**

创建 `apple/LatticeMac/PanelComponents.swift`（文件头用 Apache 2.0 许可头）：

```swift
import SwiftUI

// MARK: - Page header (secondary pages)

/// Top bar of a secondary page: "‹ 返回" + title (+ optional accessory such as
/// a 即将推出 badge). Replaces the hand-rolled "‹ 返回主面板" headers.
struct PageHeader<Accessory: View>: View {
    let title: String
    let onBack: () -> Void
    @ViewBuilder var accessory: () -> Accessory

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Button(action: onBack) {
                    HStack(spacing: 2) {
                        Image(systemName: "chevron.left")
                            .font(.system(size: 11, weight: .semibold))
                        Text("返回").font(.system(size: 12))
                    }
                    .foregroundColor(.accentColor)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                Text(title)
                    .font(.system(.headline, design: .rounded))
                    .lineLimit(1)
                accessory()
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 16)
            .frame(height: 44)
            Divider()
        }
    }
}

extension PageHeader where Accessory == EmptyView {
    init(title: String, onBack: @escaping () -> Void) {
        self.init(title: title, onBack: onBack) { EmptyView() }
    }
}

// MARK: - Sheet scaffold

/// Shared frame of every sheet: title + a ✕ close button on the top row
/// (Esc closes too), fixed 340pt width, 20pt padding.
struct SheetScaffold<Content: View>: View {
    let title: String
    let onClose: () -> Void
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text(title)
                    .font(.system(.headline, design: .rounded))
                Spacer()
                Button(action: onClose) {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 15))
                        .foregroundColor(.secondary)
                }
                .buttonStyle(.plain)
                .keyboardShortcut(.cancelAction)
                .help("关闭")
            }
            content()
        }
        .padding(20)
        .frame(width: 340)
    }
}

// MARK: - State view (loading / error / empty)

/// One layout for the loading, error and empty states of the first screen.
struct StateView: View {
    var icon: String? = nil
    var iconColor: Color = .secondary
    let title: String
    var message: String? = nil
    var actionTitle: String? = nil
    var prominent = false
    var isLoading = false
    var action: (() -> Void)? = nil

    var body: some View {
        VStack(spacing: 10) {
            if isLoading {
                ProgressView().controlSize(.small)
            } else if let icon {
                Image(systemName: icon)
                    .font(.system(size: 30))
                    .foregroundColor(iconColor)
            }
            Text(title).foregroundColor(.secondary)
            if let message {
                Text(message)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let actionTitle, let action {
                if prominent {
                    Button(actionTitle, action: action).buttonStyle(.borderedProminent)
                } else {
                    Button(actionTitle, action: action).buttonStyle(.bordered)
                }
            }
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

// MARK: - Bottom nav bar

struct PanelNavItem: Identifiable {
    let id: String
    let icon: String
    let title: String
    var showsDot = false
    let action: () -> Void
}

/// The single icon bar at the bottom of the first screen.
struct PanelNavBar: View {
    let items: [PanelNavItem]

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 0) {
                ForEach(items) { item in
                    PanelNavButton(item: item)
                }
            }
        }
    }
}

private struct PanelNavButton: View {
    let item: PanelNavItem
    @State private var hovered = false

    var body: some View {
        Button(action: item.action) {
            VStack(spacing: 3) {
                Image(systemName: item.icon)
                    .font(.system(size: 15, weight: .medium))
                    .overlay(alignment: .topTrailing) {
                        if item.showsDot {
                            Circle()
                                .fill(LatticePalette.online)
                                .frame(width: 7, height: 7)
                                .offset(x: 4, y: -2)
                        }
                    }
                Text(item.title).font(.system(size: 10.5))
            }
            .foregroundColor(Color.primary.opacity(0.8))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 7)
            .contentShape(Rectangle())
            .background(hovered ? Color.primary.opacity(0.045) : Color.clear)
        }
        .buttonStyle(.plain)
        .onHover { hovered = $0 }
    }
}
```

- [ ] **Step 2: 确认构建产物目录不会被 git 跟踪**

Run: `cd /Users/francis/workspc/lattice && git check-ignore -v apple/build/mac-dd`
Expected: 输出一条命中规则（说明被忽略）。若没有输出，改用 scratchpad 目录作为 `-derivedDataPath`，不要把构建产物加进仓库。

- [ ] **Step 3: 重新生成 Xcode 工程（新增了 `PanelRoute.swift` 和 `PanelComponents.swift`）**

Run: `cd /Users/francis/workspc/lattice/apple && xcodegen generate && git -C .. diff --stat -- apple/LatticeApple.xcodeproj`
Expected: `project.pbxproj` 有改动，且改动只包含新增的两个文件相关行。若出现与这两个文件无关的大段改动（例如 xcodegen 版本差异导致的重排），停下来告诉用户，不要继续。

- [ ] **Step 4: 构建**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`

---

### Task 3: 投屏二级页 `CastPage` 与页内配对表单 `CastPairingView`

本任务**只新增**，不删除旧的 `CastSectionView` / `CastPairingSheet`（`ContentView` 还在用它们，任务 6 再一起删除）。

**Files:**
- Modify: `apple/LatticeMac/CastReceiver.swift`（在文件末尾追加）

**Interfaces:**
- Consumes: `PanelPage.cast.title`、`PanelPage.castPairing.title`（任务 1）；`PageHeader`（任务 2）；`CastReceiverManager.shared`（`isEnabled`、`isRunning`、`startError`、`config`、`setEnabled(_:)`、`save(config:enabled:)`，均已存在）
- Produces:
  - `CastPage(onBack: () -> Void, onEditPairing: () -> Void)`
  - `CastPairingView(onDone: () -> Void)`：保存成功后和点 `‹ 返回` 都调用 `onDone`

- [ ] **Step 1: 在 `CastReceiver.swift` 末尾追加**

```swift

// MARK: - Cast page (二级页)

/// 投屏接收二级页：开关、状态、配对信息只读摘要（令牌不显示）。
struct CastPage: View {
    var onBack: () -> Void
    var onEditPairing: () -> Void

    @ObservedObject private var receiver = CastReceiverManager.shared

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.cast.title, onBack: onBack)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    HStack {
                        Text("接收投屏").font(.system(size: 13))
                        Spacer()
                        Toggle("", isOn: Binding(
                            get: { receiver.isEnabled },
                            set: { receiver.setEnabled($0) }
                        ))
                        .toggleStyle(.switch)
                        .controlSize(.small)
                        .labelsHidden()
                    }
                    statusLine

                    if let config = receiver.config {
                        VStack(alignment: .leading, spacing: 6) {
                            summaryRow("接收端名称", config.name)
                            summaryRow("房间", config.room)
                            summaryRow("端口", String(config.port))
                        }
                        .padding(10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(
                            RoundedRectangle(cornerRadius: 10)
                                .fill(Color.primary.opacity(0.04))
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: 10)
                                .strokeBorder(Color.primary.opacity(0.08))
                        )
                    }

                    Button(receiver.config == nil ? "配对…" : "编辑配对信息…") { onEditPairing() }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
                .padding(16)
            }
        }
    }

    @ViewBuilder
    private var statusLine: some View {
        if receiver.isRunning, let config = receiver.config {
            Text("接收中 · \(config.name) · 房间 \(config.room)")
                .font(.caption)
                .foregroundColor(.secondary)
        } else if let err = receiver.startError {
            Text(err).font(.caption).foregroundColor(.red)
        } else {
            Text("未配对 — 保存配对后即可接收").font(.caption).foregroundColor(.secondary)
        }
    }

    private func summaryRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(.caption).foregroundColor(.secondary)
            Spacer()
            Text(value).font(.system(.caption, design: .monospaced))
        }
    }
}

// MARK: - Pairing form (页内表单，仅主窗口)

/// 配对信息编辑（渲染端身份：name/room/token/port）。在主窗口里作为
/// 投屏页下的页内表单出现；面板里编辑配对会转到主窗口（需要文字输入）。
struct CastPairingView: View {
    var onDone: () -> Void

    @State private var name = Host.current().localizedName ?? "lattice-mac"
    @State private var room = "lattice"
    @State private var token = ""
    @State private var port = "7822"

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.castPairing.title, onBack: onDone)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    LabeledField(label: "接收端名称") {
                        TextField("lattice-mac", text: $name).textFieldStyle(.plain)
                    }
                    LabeledField(label: "房间") {
                        TextField("lattice", text: $room).textFieldStyle(.plain)
                    }
                    LabeledField(label: "配对令牌") {
                        SecureField("cast-agent 签发", text: $token).textFieldStyle(.plain)
                            .font(.system(.caption, design: .monospaced))
                    }
                    LabeledField(label: "端口") {
                        TextField("7822", text: $port).textFieldStyle(.plain)
                            .font(.system(.caption, design: .monospaced))
                    }
                    Text("在 cast-agent 侧登记此名称与令牌后，即可向本机发起投屏。")
                        .font(.caption2).foregroundColor(.secondary)
                    HStack {
                        Spacer()
                        Button("保存") { save() }
                            .buttonStyle(.borderedProminent)
                            .disabled(name.isEmpty || room.isEmpty || token.isEmpty)
                    }
                }
                .padding(16)
            }
        }
        .onAppear {
            if let existing = CastReceiverManager.shared.config {
                name = existing.name
                room = existing.room
                token = existing.token
                port = String(existing.port)
            }
        }
    }

    private func save() {
        let config = LatticeCastConfig(
            name: name, room: room, token: token,
            port: Int(port) ?? 7822
        )
        CastReceiverManager.shared.save(config: config, enabled: true)
        onDone()
    }
}
```

- [ ] **Step 2: 构建**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`（此时两个新视图还没有被引用，只需通过编译）。

---

### Task 4: 网络设置页与共享页改用 `PageHeader`，退出节点选择改为页内列表

**Files:**
- Modify: `apple/LatticeMac/NetworkPages.swift`

**Interfaces:**
- Consumes: `PageHeader`（任务 2）、`PanelPage.networkSettings.title` / `PanelPage.share.title`（任务 1）、`SoonBadge`（`DesignComponents.swift` 已有）
- Produces: `NetworkSettingsView(onBack:)` 与 `ShareView(onBack:)` 签名**不变**；行为变化：退出节点选择不再弹 sheet。

- [ ] **Step 1: 改 `NetworkSettingsView.body`：容器化、去掉 sheet**

把：

```swift
    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()

            settingsRow(
                title: "使用退出节点",
```

替换为：

```swift
    var body: some View {
        VStack(spacing: 0) {
            if showingPicker {
                exitNodePicker
            } else {
                settingsList
            }
        }
        .task { await load() }
    }

    private var settingsList: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.networkSettings.title, onBack: onBack)

            settingsRow(
                title: "使用退出节点",
```

再把：

```swift
            Spacer(minLength: 0)
            Divider()
            footerBar
        }
        .task { await load() }
        .sheet(isPresented: $showingPicker) {
            exitNodePicker
        }
    }
```

替换为：

```swift
            Spacer(minLength: 0)
            Divider()
            footerBar
        }
    }
```

- [ ] **Step 2: 抽出 `exitCandidates` / `currentExitName`**

把整个 `exitNodeDesc`：

```swift
    private var exitNodeDesc: String {
        if let picked = selectedProviders.first(where: { provider in candidates.first(where: { c in c.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true }) {
            return "当前：\(picked)"
        }
        return "全部流量经由所选节点转发 · 当前：无"
    }
```

替换为：

```swift
    private var exitCandidates: [PeerNode] {
        candidates.filter { $0.advertisedRoutes.contains("0.0.0.0/0") }
    }

    private var currentExitName: String? {
        selectedProviders.first { provider in exitCandidates.contains { $0.name == provider } }
    }

    private var exitNodeDesc: String {
        if let picked = currentExitName { return "当前：\(picked)" }
        return "全部流量经由所选节点转发 · 当前：无"
    }
```

- [ ] **Step 3: 把 `exitNodePicker` 改成页内列表**

把整个 `exitNodePicker`（含 `NavigationStack`、`List`、`.frame(width: 280, height: 320)`）替换为：

```swift
    private var exitNodePicker: some View {
        VStack(spacing: 0) {
            PageHeader(title: "选择退出节点", onBack: { showingPicker = false })
            ScrollView {
                VStack(spacing: 0) {
                    pickerRow(title: "无（关闭）", selected: currentExitName == nil) {
                        Task { await selectExitNode(nil) }
                    }
                    ForEach(exitCandidates) { peer in
                        Divider().padding(.leading, 15)
                        pickerRow(title: peer.name, selected: currentExitName == peer.name) {
                            Task { await selectExitNode(peer.name) }
                        }
                    }
                }
            }
            if !errorText.isEmpty {
                Text(errorText).font(.caption2).foregroundColor(.red)
                    .padding(.horizontal, 15).padding(.vertical, 6)
            }
        }
    }

    private func pickerRow(title: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Text(title).font(.system(size: 13))
                Spacer()
                if selected {
                    Image(systemName: "checkmark")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundColor(.accentColor)
                }
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
```

`selectExitNode(_:)` 保持不变（它成功后会 `showingPicker = false` 并重新 `load()`，正好回到设置列表）。

- [ ] **Step 4: 删除旧的 `header`**

删除 `NetworkSettingsView` 里整个 `private var header: some View { ... }`（含 "‹ 返回主面板" 和 `Text("网络设置")` 的那段，已被 `PageHeader` 取代）。

- [ ] **Step 5: `ShareView` 改用 `PageHeader`**

把：

```swift
            VStack(alignment: .leading, spacing: 3) {
                Button {
                    onBack()
                } label: {
                    Text("‹ 返回主面板")
                        .font(.caption)
                        .foregroundColor(.accentColor)
                }
                .buttonStyle(.plain)
                HStack(spacing: 6) {
                    Text("共享本地服务")
                        .font(.system(.headline, design: .rounded))
                    SoonBadge()
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            Divider()
```

替换为：

```swift
            PageHeader(title: PanelPage.share.title, onBack: onBack) { SoonBadge() }
```

- [ ] **Step 6: 构建**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`

---

### Task 5: 四个弹窗套 `SheetScaffold`

**Files:**
- Modify: `apple/LatticeMac/ContentView.swift`（`JoinView`、`SettingsView`、`ManageLoginView`、`JoinScannerView`，以及 `mainPanel` 里两处调用点）

**Interfaces:**
- Consumes: `SheetScaffold`（任务 2）
- Produces:
  - `JoinView(onDone: () -> Void, onClose: () -> Void)`
  - `SettingsView(onDone: () -> Void, onJoin: (() -> Void)? = nil, onClose: () -> Void)`（macOS 端的这个 `SettingsView` 在 `ContentView.swift` 里；`apple/Lattice/SettingsView.swift` 是 iOS 的同名类型，**不要动**）
  - `ManageLoginView(onFinished: (Bool) -> Void)`、`JoinScannerView(onCode:onCancel:)` 签名不变

- [ ] **Step 1: `JoinView` 加 `onClose` 并套脚手架**

在 `struct JoinView: View {` 里，`var onDone: () -> Void` 下面加一行：

```swift
    var onClose: () -> Void
```

把 `body` 开头：

```swift
        VStack(alignment: .leading, spacing: 12) {
            Text("加入 Lattice 网络")
                .font(.system(.headline, design: .rounded))

            Picker("", selection: $accountMode) {
```

替换为：

```swift
        SheetScaffold(title: "加入 Lattice 网络", onClose: onClose) {
            Picker("", selection: $accountMode) {
```

把 `body` 结尾：

```swift
        }
        .padding(20)
        .frame(width: 340)
        .onAppear { detectClipboardInvite() }
```

替换为：

```swift
        }
        .onAppear { detectClipboardInvite() }
```

- [ ] **Step 2: `SettingsView` 加 `onClose` 并套脚手架**

在 `struct SettingsView: View {` 里，`var onJoin: (() -> Void)? = nil` 下面加一行：

```swift
    var onClose: () -> Void
```

把 `body` 开头：

```swift
        VStack(alignment: .leading, spacing: 14) {
            Text("连接到 Lattice")
                .font(.system(.headline, design: .rounded))

            LabeledField(label: "服务器地址") {
                TextField("http://127.0.0.1:8080", text: $serverURL)
```

替换为：

```swift
        SheetScaffold(title: "连接到 Lattice", onClose: onClose) {
            LabeledField(label: "服务器地址") {
                TextField("http://127.0.0.1:8080", text: $serverURL)
```

把 `body` 结尾（以 SSO 那块的 `.strokeBorder(Color.secondary.opacity(0.3))` 定位）：

```swift
                    .strokeBorder(Color.secondary.opacity(0.3))
            )
        }
        .padding(20)
        .frame(width: 300)
    }
```

替换为：

```swift
                    .strokeBorder(Color.secondary.opacity(0.3))
            )
        }
    }
```

- [ ] **Step 3: `ManageLoginView` 套脚手架**

把开头：

```swift
        VStack(alignment: .leading, spacing: 12) {
            Text("登录以管理设备")
                .font(.system(.headline, design: .rounded))
            Text("改名、下线、删除等管理操作需要账号；设备列表和连接不受影响。")
```

替换为：

```swift
        SheetScaffold(title: "登录以管理设备", onClose: { onFinished(false) }) {
            Text("改名、下线、删除等管理操作需要账号；设备列表和连接不受影响。")
```

把结尾（以 `.disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)` 定位）：

```swift
                        .disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)
                }
            }
        }
        .padding(20)
        .frame(width: 300)
    }
```

替换为：

```swift
                        .disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)
                }
            }
        }
    }
```

（底部的"取消"按钮保留。）

- [ ] **Step 4: `JoinScannerView` 套脚手架，去掉底部"取消"**

把整个 `JoinScannerView.body`（从 `var body: some View {` 到 struct 结束前的 `}`）替换为：

```swift
    var body: some View {
        SheetScaffold(title: "扫描入网二维码", onClose: onCancel) {
            CameraScannerView(
                onCode: { code in
                    if let payload = JoinPayload(code) {
                        onCode(payload)
                    } else {
                        errorText = "二维码内容无法识别：\(code)"
                    }
                },
                onError: { errorText = $0 }
            )
            .frame(width: 280, height: 280)
            .cornerRadius(12)
            .clipped()
            .frame(maxWidth: .infinity)

            Text("二维码内容格式：lattice://join?server=…&token=…")
                .font(.caption2)
                .foregroundColor(.secondary)

            if !errorText.isEmpty {
                Text(errorText)
                    .font(.caption)
                    .foregroundColor(.red)
            }
        }
    }
```

- [ ] **Step 5: 就地更新 `mainPanel` 里两处调用点（任务 6 会再整体搬走它们）**

把：

```swift
        .sheet(isPresented: $showingSettings) {
            SettingsView {
                showingSettings = false
                Task { await loadPeers() }
            } onJoin: {
                showingSettings = false
                showingJoin = true
            }
        }
```

替换为：

```swift
        .sheet(isPresented: $showingSettings) {
            SettingsView(
                onDone: {
                    showingSettings = false
                    Task { await loadPeers() }
                },
                onJoin: {
                    showingSettings = false
                    showingJoin = true
                },
                onClose: { showingSettings = false }
            )
        }
```

把：

```swift
        .sheet(isPresented: $showingJoin) {
            JoinView {
                showingJoin = false
                joined = true
                UserDefaults.standard.set(true, forKey: "lattice.joined")
                tunnel.load {
                    tunnel.connect()
                }
            }
        }
```

替换为：

```swift
        .sheet(isPresented: $showingJoin) {
            JoinView(
                onDone: {
                    showingJoin = false
                    joined = true
                    UserDefaults.standard.set(true, forKey: "lattice.joined")
                    tunnel.load {
                        tunnel.connect()
                    }
                },
                onClose: { showingJoin = false }
            )
        }
```

- [ ] **Step 6: 构建**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`

---

### Task 6: 首屏重构与页面接线（`ContentView` + `LatticeMacApp`）

这是最大的一个任务。编辑顺序：先 `LatticeMacApp.swift`，再 `ContentView.swift` 自上而下，最后删除 `CastReceiver.swift` 里的旧视图。

**Files:**
- Modify: `apple/LatticeMac/LatticeMacApp.swift`
- Modify: `apple/LatticeMac/ContentView.swift`
- Modify: `apple/LatticeMac/CastReceiver.swift`（删除旧视图）

**Interfaces:**
- Consumes: `PanelPage`（任务 1）；`PageHeader`、`StateView`、`PanelNavItem`、`PanelNavBar`（任务 2）；`CastPage`、`CastPairingView`（任务 3）；`NetworkSettingsView(onBack:)`、`ShareView(onBack:)`（任务 4）；`JoinView(onDone:onClose:)`、`SettingsView(onDone:onJoin:onClose:)`（任务 5）
- Produces: `UIState.Page == PanelPage`；`ContentView` 内部的 `subPage`、`open(_:)`、`presentJoin()`、`presentSettings()`（任务 7 依赖 `subPage` 和 `detailPeer`）

- [ ] **Step 1: `LatticeMacApp.swift`——`UIState.Page` 改为别名**

把：

```swift
    enum Page: String {
        case networkSettings
        case share
    }
```

替换为：

```swift
    typealias Page = PanelPage
```

- [ ] **Step 2: `LatticeMacApp.swift`——主窗口尺寸**

把：

```swift
        Window("Lattice", id: "main") {
            ContentView()
                .frame(width: 360)
                .frame(minHeight: 420, maxHeight: 640)
        }
        .windowStyle(.hiddenTitleBar)
        .windowResizability(.contentMinSize)
```

替换为：

```swift
        Window("Lattice", id: "main") {
            ContentView()
                .frame(width: 360)
                .frame(minHeight: 480, maxHeight: 900)
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 360, height: 640)
        .windowResizability(.contentMinSize)
```

- [ ] **Step 3: `LatticeMacApp.swift`——`MenuBarPanel` 去掉外层页脚**

把整个 `struct MenuBarPanel: View`（从 `/// Popover content:` 注释起，到 `class AppDelegate` 之前）替换为：

```swift
/// Popover content: the shared main panel in panel mode (read-mostly —
/// every flow that needs typing routes to the main window). "加入网络" and
/// "退出 Lattice" live in the header's ⋯ menu.
struct MenuBarPanel: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        ContentView(
            inPanel: true,
            openMain: {
                openWindow(id: "main")
                NSApp.activate(ignoringOtherApps: true)
            },
            openAI: {
                openWindow(id: "ai")
                NSApp.activate(ignoringOtherApps: true)
            }
        )
        .frame(width: 340)
        .frame(minHeight: 380, maxHeight: 560)
    }
}
```

- [ ] **Step 4: `ContentView.swift`——状态变量**

把：

```swift
    @State private var showingNetworkSettings = false
    @State private var showingShare = false
```

替换为：

```swift
    /// The secondary page shown in place of the first screen; nil = first screen.
    @State private var subPage: PanelPage?
    @ObservedObject private var castReceiver = CastReceiverManager.shared
```

- [ ] **Step 5: `ContentView.swift`——`body` 里的页面分支，并把弹窗与跨窗口请求上移到容器**

把：

```swift
            } else if showingNetworkSettings {
                NetworkSettingsView {
                    showingNetworkSettings = false
                }
            } else if showingShare {
                ShareView {
                    showingShare = false
                }
            } else {
                mainPanel
            }
        }
```

替换为：

```swift
            } else if let page = subPage {
                subPageView(page)
            } else {
                mainPanel
            }
        }
        .onAppear { syncUIStateRequests() }
        // onChange fires after the update — safe to mutate state here
        // (onReceive could land mid-update and crash SwiftUI).
        .onChange(of: ui.showJoin) { _ in syncUIStateRequests() }
        .onChange(of: ui.showSettings) { _ in syncUIStateRequests() }
        .onChange(of: ui.detailPeerName) { _ in syncUIStateRequests() }
        .onChange(of: ui.page) { _ in syncUIStateRequests() }
        // A management action (rename, delete, ...) that needs a login asks for
        // one here and carries on once it succeeds. Only the main window presents
        // it; the menu-bar panel cannot host a sheet.
        .sheet(isPresented: Binding(
            get: { loginCoordinator.isPresenting && !inPanel },
            set: { if !$0 { loginCoordinator.finish(success: false) } }
        )) {
            ManageLoginView { loginCoordinator.finish(success: $0) }
        }
        .sheet(isPresented: $showingSettings) {
            SettingsView(
                onDone: {
                    showingSettings = false
                    Task { await loadPeers() }
                },
                onJoin: {
                    showingSettings = false
                    showingJoin = true
                },
                onClose: { showingSettings = false }
            )
        }
        .sheet(isPresented: $showingJoin) {
            JoinView(
                onDone: {
                    showingJoin = false
                    joined = true
                    UserDefaults.standard.set(true, forKey: "lattice.joined")
                    tunnel.load {
                        tunnel.connect()
                    }
                },
                onClose: { showingJoin = false }
            )
        }
```

（后面紧跟的 `.alert("重命名节点"…` 等修饰符保持不动。）

- [ ] **Step 6: `ContentView.swift`——`syncUIStateRequests` 里的页面分支**

把：

```swift
        if let page = ui.page {
            ui.page = nil
            switch page {
            case .networkSettings: showingNetworkSettings = true
            case .share: showingShare = true
            }
        }
```

替换为：

```swift
        if let page = ui.page {
            ui.page = nil
            subPage = page
        }
```

- [ ] **Step 7: `ContentView.swift`——替换整个 `mainPanel`**

定位：从 `    private var mainPanel: some View {` 开始，到紧邻的 `    /// Shown when the management API is unavailable:` 注释**之前**结束。把这一整段替换为下面的内容（其中 `.task` 与 `.onChange(of: auth.isLoggedIn)` 保留，三个 `.sheet`、`.onAppear`、四个 `.onChange(of: ui.…)` 已在步骤 5 上移，这里不再有）：

```swift
    private var mainPanel: some View {
        VStack(spacing: 0) {
            header
            Divider()
            content
            if !opError.isEmpty {
                opErrorBanner
            }
            PanelNavBar(items: navItems)
        }
        .task {
            tunnel.load()
            CastReceiverManager.shared.startIfNeeded()
            await loadPeers()
        }
        .onChange(of: auth.isLoggedIn) { _ in
            Task { await loadPeers() }
        }
    }

    @ViewBuilder
    private var content: some View {
        if !joined {
            StateView(
                icon: "personalhotspot",
                title: "尚未加入 Lattice 网络",
                actionTitle: "加入网络",
                prominent: true
            ) { presentJoin() }
        } else if isLoading && displayPeers.isEmpty {
            StateView(title: "加载中…", isLoading: true)
        } else if !errorMsg.isEmpty && displayPeers.isEmpty {
            StateView(
                icon: "exclamationmark.triangle",
                iconColor: .orange,
                title: "加载失败",
                message: errorMsg,
                actionTitle: "重试"
            ) { Task { await loadPeers() } }
        } else if displayPeers.isEmpty {
            StateView(
                icon: "personalhotspot",
                title: "没有已连接的节点",
                actionTitle: needsLogin ? "登录以查看和管理设备" : nil
            ) { requestManageLogin() }
        } else {
            deviceList
        }
    }

    private var navItems: [PanelNavItem] {
        [
            PanelNavItem(id: "network", icon: "network", title: "网络") { open(.networkSettings) },
            PanelNavItem(id: "share", icon: "arrow.up.forward.app", title: "共享") { open(.share) },
            PanelNavItem(id: "cast", icon: "tv", title: "投屏", showsDot: castReceiver.isRunning) { open(.cast) },
            PanelNavItem(id: "ai", icon: "sparkles", title: "AI") {
                openAIWindow(id: "ai")
                NSApp.activate(ignoringOtherApps: true)
            },
        ]
    }

    /// A management action failed (rename, delete, ...): a dismissible strip
    /// above the nav bar. Replaces the old footer's inline error text.
    private var opErrorBanner: some View {
        HStack(spacing: 6) {
            Image(systemName: "exclamationmark.circle.fill").foregroundColor(.red)
            Text(opError)
                .font(.caption2)
                .foregroundColor(.red)
                .lineLimit(2)
            Spacer()
            Button { opError = "" } label: {
                Image(systemName: "xmark").font(.caption2)
            }
            .buttonStyle(.plain)
            .foregroundColor(.secondary)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 6)
        .background(Color.red.opacity(0.08))
        .help(opError)
    }

    @ViewBuilder
    private func subPageView(_ page: PanelPage) -> some View {
        switch page {
        case .networkSettings:
            NetworkSettingsView { subPage = page.parent }
        case .share:
            ShareView { subPage = page.parent }
        case .cast:
            CastPage(
                onBack: { subPage = page.parent },
                onEditPairing: { open(.castPairing) }
            )
        case .castPairing:
            CastPairingView { subPage = page.parent }
        }
    }

    /// Opens a secondary page: in place, or — for pages with text fields
    /// opened from the panel — in the main window.
    private func open(_ page: PanelPage) {
        switch page.destination(inPanel: inPanel) {
        case .inPlace:
            subPage = page
        case .mainWindow:
            UIState.shared.page = page
            openMain?()
        }
    }

    private func presentJoin() {
        if inPanel {
            UIState.shared.showJoin = true
            openMain?()
        } else {
            showingJoin = true
        }
    }

    private func presentSettings() {
        if inPanel {
            UIState.shared.showSettings = true
            openMain?()
        } else {
            showingSettings = true
        }
    }

```

- [ ] **Step 8: `ContentView.swift`——`requestManageLogin` 在面板里转主窗口**

把：

```swift
    private func requestManageLogin() {
        Task { _ = await LoginCoordinator.shared.requestLogin() }
    }
```

替换为：

```swift
    private func requestManageLogin() {
        // The login sheet is presented by the main window only.
        if inPanel { openMain?() }
        Task { _ = await LoginCoordinator.shared.requestLogin() }
    }
```

- [ ] **Step 9: `ContentView.swift`——设备列表去掉"退出节点"行**

在 `deviceList` 里，删除这一整段（含后面的 `Divider()`）：

```swift
                NavRow(
                    icon: "arrow.left.arrow.right",
                    iconColor: .gray,
                    title: "退出节点",
                    value: "无",
                    showsChevron: true
                ) {
                    if inPanel {
                        UIState.shared.page = .networkSettings
                        openMain?()
                    } else {
                        showingNetworkSettings = true
                    }
                }
                Divider()
```

- [ ] **Step 10: `ContentView.swift`——删除 `bottomNav` 与 `footer`**

1. 删除从 `    /// Bottom quick-nav stack: AI assistant, sharing, network settings, footer.` 起，到 `    private var connected: Binding<Bool> {` **之前**的整段（即整个 `bottomNav`）。
2. 删除从 `    private var footer: some View {` 起，到 `    private func loadPeers() async {` **之前**的整段（即整个 `footer`）。

- [ ] **Step 11: `ContentView.swift`——连接开关未配置时走 `presentJoin()`**

把：

```swift
                    if tunnel.isConfigured {
                        tunnel.connect()
                    } else {
                        showingJoin = true
                    }
```

替换为：

```swift
                    if tunnel.isConfigured {
                        tunnel.connect()
                    } else {
                        presentJoin()
                    }
```

- [ ] **Step 12: `ContentView.swift`——头部加 `⋯` 菜单**

把 `header` 里：

```swift
            Toggle("", isOn: connected)
                .toggleStyle(.switch)
                .controlSize(.small)
                .labelsHidden()
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 12)
```

替换为：

```swift
            Toggle("", isOn: connected)
                .toggleStyle(.switch)
                .controlSize(.small)
                .labelsHidden()
            moreMenu
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 12)
```

并在 `header` 属性**之后**紧接着加入：

```swift
    private var moreMenu: some View {
        Menu {
            Button("加入网络 / 重新入网…") { presentJoin() }
            Button("连接设置…") { presentSettings() }
            Button("刷新设备列表") { Task { await loadPeers() } }
            Divider()
            Button("退出 Lattice") { NSApp.terminate(nil) }
        } label: {
            Image(systemName: "ellipsis.circle")
                .font(.system(size: 14))
                .foregroundColor(.secondary)
        }
        .menuStyle(.borderlessButton)
        .menuIndicator(.hidden)
        .fixedSize()
        .help("更多")
    }
```

- [ ] **Step 13: `CastReceiver.swift`——删除旧视图**

删除从 `// MARK: - Panel section (投屏接收)` 起，到 `// MARK: - Cast page (二级页)` **之前**的全部内容（即 `CastSectionView` 和 `CastPairingSheet`）。

- [ ] **Step 14: 全局确认没有残留引用**

Run: `cd /Users/francis/workspc/lattice/apple && grep -rnE "showingNetworkSettings|showingShare|CastSectionView|CastPairingSheet|bottomNav|private var footer" LatticeMac`
Expected: 无输出。

- [ ] **Step 15: 构建并跑逻辑测试**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`

Run: `LOGIC`
Expected: `apple logic: all checks passed`

---

### Task 7: 面板关闭后重开回到首屏

`MenuBarExtra(.window)` 的内容视图在面板关闭后不会销毁，`@State` 会保留，`onAppear` / `onDisappear` 也不可靠。这里用 `NSWindow.didChangeOcclusionStateNotification` 监听面板变为不可见，仅对面板生效（主窗口被遮挡时不应复位）。

**Files:**
- Modify: `apple/LatticeMac/PanelComponents.swift`（追加 `WindowHiddenObserver`）
- Modify: `apple/LatticeMac/ContentView.swift`（`body` 加一行 `.background`）

**Interfaces:**
- Consumes: `ContentView.subPage`、`ContentView.detailPeer`（任务 6）
- Produces: `WindowHiddenObserver(onHidden: () -> Void)`

- [ ] **Step 1: 在 `PanelComponents.swift` 末尾追加**

```swift

// MARK: - Window visibility

/// Calls `onHidden` whenever the hosting window stops being visible. The
/// menu-bar panel keeps its SwiftUI state across open/close and does not
/// reliably fire onAppear/onDisappear, so window occlusion is the signal.
struct WindowHiddenObserver: NSViewRepresentable {
    var onHidden: () -> Void

    func makeNSView(context: Context) -> NSView {
        ObserverView(onHidden: onHidden)
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        (nsView as? ObserverView)?.onHidden = onHidden
    }

    private final class ObserverView: NSView {
        var onHidden: () -> Void
        private var token: NSObjectProtocol?

        init(onHidden: @escaping () -> Void) {
            self.onHidden = onHidden
            super.init(frame: .zero)
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let token { NotificationCenter.default.removeObserver(token) }
            token = nil
            guard let window else { return }
            token = NotificationCenter.default.addObserver(
                forName: NSWindow.didChangeOcclusionStateNotification,
                object: window,
                queue: .main
            ) { [weak self] note in
                guard let w = note.object as? NSWindow, !w.occlusionState.contains(.visible) else { return }
                self?.onHidden()
            }
        }

        deinit {
            if let token { NotificationCenter.default.removeObserver(token) }
        }
    }
}
```

- [ ] **Step 2: 在 `ContentView.body` 接上**

在 `body` 里 `VStack(spacing: 0) { … }` 之后、`.onAppear { syncUIStateRequests() }` 之前，插入：

```swift
        .background(
            WindowHiddenObserver {
                // Only the menu-bar panel resets; the main window can be occluded
                // by other windows without losing its place.
                guard inPanel else { return }
                subPage = nil
                detailPeer = nil
            }
        )
```

- [ ] **Step 3: 构建**

Run: `BUILD`
Expected: `** BUILD SUCCEEDED **`

- [ ] **Step 4（人工验证，见任务 8 清单第 8 项）：** 打开面板 → 进入"网络" → 点菜单栏图标关闭面板 → 再打开，应回到首屏。若仍停在二级页面，说明面板被关闭时窗口不会触发遮挡状态变化；此时**不要自行换方案**，把现象告诉用户再决定。

---

### Task 8: 最终验证、提交、推送

**Files:** 无新增改动；提交任务 1–7 的全部改动。

- [ ] **Step 1: 全量构建 + 逻辑测试**

Run: `BUILD`，期望 `** BUILD SUCCEEDED **`。
Run: `LOGIC`，期望 `apple logic: all checks passed`。

- [ ] **Step 2: 人工验证清单（需要有人操作界面；无法操作时逐项报告"未验证"，不要声称通过）**

先启动改动后的构建：`open /Users/francis/workspc/lattice/apple/build/mac-dd/Build/Products/Debug/LatticeMac.app`。未签名构建的隧道无法连接，但界面可以看。需要看到设备列表的项目要用已入网的设备（用户自己的环境）。

1. 未入网状态：首屏显示"尚未加入 Lattice 网络"和"加入网络"按钮；点击后弹出加入网络窗口，**右上角有 ✕，按 Esc 能关闭**。
2. 头部右侧有 `⋯` 菜单，含：加入网络 / 重新入网…、连接设置…、刷新设备列表、退出 Lattice。**菜单在面板里能正常展开**（若不能，如实报告，此时需要用户决定改法）。
3. `⋯` → "连接设置…"：面板里会转到主窗口并弹出"连接到 Lattice"，有 ✕，Esc 可关闭。
4. 底部导航栏为四项：网络、共享、投屏、AI；点"网络 / 共享 / 投屏"在**面板内原地**打开二级页，`‹ 返回` 可回首屏。
5. 网络页里点"使用退出节点"：出现**页内列表**（不是 sheet），有 `‹ 返回`，选中项有 ✓。
6. 投屏页：显示开关、状态和只读的配对摘要（不显示令牌）。面板里点"编辑配对信息…"：跳到主窗口并直接显示配对表单；在主窗口里点则原地进入表单；保存后回到投屏页。
7. 已入网时：面板最小高度下设备列表可见行数明显多于改动前（改动前约 1 个）。首屏不再有投屏卡片、4 行底部导航和外层页脚。
8. 面板关闭再打开会回到首屏，而不是停在上次的二级页面。
9. 主窗口：初始高度约 640，可拖到约 900；同样的首屏与二级页面表现。
10. 制造一次操作失败（例如断网后重命名）：设备列表下方出现红色提示条，点 ✕ 可关闭。

- [ ] **Step 3: 确认改动范围**

Run: `cd /Users/francis/workspc/lattice && git status --short`
Expected 只包含：
- `apple/LatticeMac/PanelRoute.swift`（新增）
- `apple/LatticeMac/PanelComponents.swift`（新增）
- `apple/LatticeMac/CastReceiver.swift`
- `apple/LatticeMac/NetworkPages.swift`
- `apple/LatticeMac/ContentView.swift`
- `apple/LatticeMac/LatticeMacApp.swift`
- `apple/Tests/AppleLogicTests.swift`
- `apple/Scripts/test_apple_logic.sh`
- `apple/LatticeApple.xcodeproj/project.pbxproj`
- 以及本来就在的两个未跟踪文件 `docs/superpowers/plans/2026-09-19-latticerun-v1.md`、`docs/superpowers/specs/2026-09-19-latticerun-design.md`（**不要 add**）。
- 本计划文件 `docs/superpowers/plans/2026-09-22-mac-panel-redesign.md` 也要一起提交。

出现其他文件（尤其 `apple/build/`、`apple/lattice.db`）时先弄清来源，不要 add。

- [ ] **Step 4: lint（`CLAUDE.md` 要求提交前先跑）**

本次没改 Go 代码，但按项目规则仍先跑：

Run: `cd /Users/francis/workspc/lattice && GOTOOLCHAIN=go1.26.8 bin/golangci-lint run ./internal/... ./cmd/...`
Expected: `0 issues.`（`make lint` 会被用户本地未跟踪的 `tmp-syntool/` 干扰，所以只 lint 真实代码目录。）

- [ ] **Step 5: 提交（单次提交）**

```bash
cd /Users/francis/workspc/lattice && git add \
  apple/LatticeMac/PanelRoute.swift \
  apple/LatticeMac/PanelComponents.swift \
  apple/LatticeMac/CastReceiver.swift \
  apple/LatticeMac/NetworkPages.swift \
  apple/LatticeMac/ContentView.swift \
  apple/LatticeMac/LatticeMacApp.swift \
  apple/Tests/AppleLogicTests.swift \
  apple/Scripts/test_apple_logic.sh \
  apple/LatticeApple.xcodeproj/project.pbxproj \
  docs/superpowers/plans/2026-09-22-mac-panel-redesign.md
git commit -s -m "feat(apple): redesign mac panel with secondary pages and closable sheets

First screen keeps only status, devices and one nav bar; network, share
and cast move to secondary pages that open in place. All sheets share a
scaffold with a close button (Esc closes). The panel no longer presents
sheets: flows that need typing route to the main window. The panel
resets to the first screen when it is reopened."
```

提交信息末尾**不要**加 `Co-Authored-By`。如果任务 1 Step 0 做了复现，把观察结果补进 commit 说明正文。

- [ ] **Step 6: 推送——先向用户确认再执行**

`CLAUDE.md` 要求提交后立即推送，但当前分支 `feat/lattice-cast-develop` 在任何远程上都还不存在，且 `.github/workflows/release.yml` 会对 `feat/**` 分支的 push 触发（含 `goreleaser` 和 `docker` 两个 job）；仓库还配置了 `origin` / `gitea` / `gitee` / `upstream` 四个远程。因此**执行前必须让用户确认远程和是否接受触发 CI**，确认后：

```bash
git push -u origin feat/lattice-cast-develop
```

---

## Self-Review（对照设计文档）

**Spec coverage**

| 设计文档要求 | 对应任务 |
|---|---|
| 二、目标 1：首屏只留状态 + 设备 + 导航栏 | 任务 6（步骤 7、9、10、12） |
| 二、目标 2：投屏 / 共享 / 网络改二级页面，面板内原地打开 | 任务 3、4、6（步骤 5、7 的 `open` / `subPageView`） |
| 二、目标 3：所有弹窗有关闭入口，Esc 可关 | 任务 2（`SheetScaffold`）、任务 5 |
| 二、目标 4：面板内不再弹 sheet，输入流程转主窗口 | 任务 1（`destination`）、任务 4（退出节点页内化）、任务 6（步骤 7 的 `presentJoin` / 步骤 8 / 步骤 11） |
| 二、目标 5：组件统一 | 任务 2、4、5、6 |
| 三、信息架构 | 任务 6 |
| 四、一级首屏（`⋯` 菜单、导航栏、投屏绿点、移除项） | 任务 6（步骤 7、9、10、12）；投屏绿点在 `navItems` |
| 四、搜索框仅主窗口 | 未改动，保持现状 |
| 五、二级页面统一顶栏、`UIState.Page` 新增 cast / castPairing | 任务 1、2、6（步骤 1） |
| 五、面板重开回到首屏 | 任务 7 |
| 六、`SheetScaffold`（340 宽、20 内边距、✕ + Esc） | 任务 2、5 |
| 六、投屏配对改为页内表单 | 任务 3、6 |
| 七、`StateView` | 任务 2、6 |
| 八、主窗口 480–900 | 任务 6（步骤 2） |
| 九、`xcodegen generate` 并提交 pbxproj | 任务 2（步骤 3）、任务 8 |
| 十一、构建、截图对照、疑似缺陷复现、逻辑测试 | 任务 1（Step 0 与 TDD）、任务 8（步骤 2） |

**Placeholder scan:** 未发现 TBD / "适当处理" 类占位；所有代码步骤均给出完整代码。

**Type consistency:** `PanelPage`（`title` / `parent` / `destination(inPanel:)`）、`PanelDestination`、`PageHeader(title:onBack:accessory:)`、`SheetScaffold(title:onClose:content:)`、`StateView` 的参数顺序、`PanelNavItem` / `PanelNavBar`、`CastPage(onBack:onEditPairing:)`、`CastPairingView(onDone:)`、`JoinView(onDone:onClose:)`、`SettingsView(onDone:onJoin:onClose:)`、`WindowHiddenObserver(onHidden:)`、`subPage` / `detailPeer` 在各任务间名称一致。

**已知不确定项（已在任务中设置验证点，不是占位）:** `⋯` 菜单在面板（非 key window）里能否正常展开（任务 8 清单第 2 项）；`didChangeOcclusionStateNotification` 是否在面板关闭时触发（任务 7 Step 4、任务 8 清单第 8 项）。任一不成立都要如实报告，由用户决定改法。
