# macOS 客户端面板与窗口 UI 重设计

**日期**：2026-09-22
**状态**：Draft（待评审）
**范围**：`apple/LatticeMac`（菜单栏面板 + 主窗口）。不含 Vue 网页控制台、iOS 端、AI 助手窗口
**关联文档**：[Apple 客户端设计](./2026-09-13-apple-clients-design.md)、[iOS Tailscale 风格重设计](./2026-09-16-ios-tailscale-style-redesign-design.md)、[PlayerCore 共享播放内核设计](./2026-09-20-player-core-design.md)

---

## 一、背景与动机

macOS 客户端有两个入口，共用同一个 `ContentView`：

| 入口 | 尺寸 | 定义位置 |
|---|---|---|
| 菜单栏弹出面板（**主要入口**） | 宽 340，高 380–560 | `LatticeMacApp.swift` 的 `MenuBarPanel` |
| 主窗口 | 宽 360，高 420–640 | `LatticeMacApp.swift` 的 `Window("Lattice", id: "main")` |

实际使用中暴露出三类问题：

1. **弹窗没有关闭按钮。** `CastPairingSheet`（投屏配对）只有"保存"；`JoinView`（加入网络）和 `SettingsView`（连接设置）同样没有取消或关闭入口。三个弹窗宽度分别为 320 / 340 / 300，风格不一。
2. **设备（节点）区域太小。** 设备列表是夹在固定内容之间的 `ScrollView`：头部、投屏卡片（约 120pt）、底部导航（4 行）、以及面板外层 `MenuBarPanel` 自己加的一条页脚（约 30pt）。粗估固定内容约 300pt，面板最小高度下列表只剩约 80pt，扣掉"设备"标题和"退出节点"行后只能看到约 1 个节点。（以上为读代码的估算，实现阶段以截图为准。）
3. **一级界面塞得太满，且入口重复。** 投屏、共享、AI、网络设置、加入网络、退出全部堆在首屏；"加入网络"在 `MenuBarPanel` 页脚和 `ContentView.footer` 各有一个。

此外还有一个疑似缺陷：`ContentView` 有注释写明"面板不是 key window，输入框会失焦，点击外部会让面板消失，所以需要输入的流程都转到主窗口"，登录弹窗也据此加了 `!inPanel` 限制。但 `CastSectionView` 没有这个限制，在面板里也会直接弹出 `CastPairingSheet`。这是从代码推断的，**尚未运行验证**，实现的第一步先复现它。

## 二、目标 / 非目标

**目标**
1. 面板首屏只保留"连接状态 + 设备列表 + 一条导航栏"，设备列表拿到剩余全部高度
2. 投屏、共享、网络设置改为二级页面，在面板内原地打开
3. 所有弹窗和二级页面都有明确的关闭 / 返回入口，`Esc` 可关闭弹窗
4. 面板内不再弹出任何 sheet；需要文字输入的流程统一转主窗口
5. 弹窗、二级页面顶栏、加载 / 出错 / 空列表三种状态各只有一套组件

**非目标**
- 不做主窗口双栏布局（后续需要再评估）
- 不改 Vue 网页控制台、iOS 端、AI 助手窗口（`ChatWindow`）
- 不改隧道、投屏协议、API 等任何非 UI 逻辑
- 不调整面板尺寸（保持 340 宽、高 380–560）；只把外层页脚移除，让 `ContentView` 拿到完整高度

## 三、信息架构

```
菜单栏面板 / 主窗口（共用 ContentView）
│
├─ 一级：首屏（状态 + 设备 + 导航栏）
│    ├─ 头部 ⋯ 菜单：加入/重新入网 · 连接设置 · 退出 Lattice
│    ├─ 设备列表
│    └─ 导航栏：网络 · 共享 · 投屏 · AI
│
├─ 二级页面（原地替换首屏，顶栏 ‹ 返回 + 标题）
│    ├─ 网络：使用退出节点 · 广播子网路由 · MagicDNS
│    ├─ 共享：本地服务共享（现有内容）
│    ├─ 投屏：接收开关 · 状态 · 配对信息摘要（只读）
│    └─ 节点详情（有输入框 → 面板内转主窗口）
│
└─ 三级 / 弹窗（仅主窗口）
     ├─ 投屏配对编辑（页内表单，不是 sheet）
     └─ 加入网络 · 连接设置 · 管理登录（sheet，统一脚手架）
```

"AI"不属于二级页面，仍然打开独立的 AI 助手窗口，行为与现在一致。

## 四、一级首屏

```
┌──────────────────────────┐
│ ● 已连接 [直连]   [开关] ⋯│
├──────────────────────────┤
│ 设备                 5 台 │
│ ● mac-mini  本机   直连  │
│ ● nas              中继  │
│ ● pi-4             直连  │
│ …（列表占满剩余高度）      │
├──────────────────────────┤
│  网络    共享   投屏●  AI │
└──────────────────────────┘
```

- **头部**：保留状态点、状态文字、质量标签、连接开关；右侧新增 `⋯` 菜单，包含"加入网络 / 重新入网"、"连接设置"、"退出 Lattice"。
- **设备列表**：移除"退出节点"行（该功能已在二级页"网络"的"使用退出节点"里）。设备行本身（`PeerRow`）不改。
- **导航栏**：一条图标加文字的横栏，四项：网络、共享、投屏、AI。投屏在接收中时图标右上角显示绿点。
- **移除**：首屏的投屏卡片（`CastSectionView`）、4 行底部导航（`bottomNav`）、`ContentView.footer` 的 `plus.circle`、`MenuBarPanel` 外层页脚。"退出 Lattice"和"加入网络"只在 `⋯` 菜单里保留一份。
- **搜索框**：保持现状，仅在主窗口且设备数 ≥ 4 时显示。面板里输入框会失焦，不加。
- 首屏固定占用估算约 95pt（头部约 50 + 导航栏约 44），面板最小高度下列表约 285pt。

## 五、二级页面

沿用 `ContentView.body` 里已有的"在同一容器内原地替换 + 返回按钮"机制（现有的 `detailPeer`、`showingNetworkSettings`、`showingShare` 就是这个模式），不引入 `NavigationStack`。三个布尔状态收敛为一个枚举状态 `subPage`（网络 / 共享 / 投屏 / 投屏配对），具体形态由实现计划决定。

**统一顶栏 `PageHeader`**：`‹ 返回` + 标题，高度固定。取代现有 `NetworkSettingsView` / `ShareView` 里各自手写的"‹ 返回主面板"。

| 页面 | 面板内 | 主窗口 | 说明 |
|---|---|---|---|
| 网络 | 原地打开 | 原地打开 | 无文字输入。退出节点选择由 sheet 改为页内列表（面板承载不了 sheet） |
| 共享 | 原地打开 | 原地打开 | 无文字输入，现有内容仅换顶栏 |
| 投屏 | 原地打开 | 原地打开 | 接收开关、状态、配对信息**只读摘要** |
| 投屏配对编辑 | 跳转主窗口 | 页内表单 | 有输入框。表单页有 `‹ 返回`，保存后返回投屏页 |
| 节点详情 | 跳转主窗口 | 原地打开 | 有意图输入框，维持现状 |

**跨窗口路由**：`UIState.Page` 新增 `cast` 和 `castPairing` 两个 case（现有 `networkSettings`、`share`）。面板里"编辑配对"设置 `UIState.shared.page = .castPairing` 并打开主窗口，由 `syncUIStateRequests()` 消费，与现有机制一致。网络、共享、投屏三页在面板内改为直接原地打开，不再走 `UIState` 转主窗口；`UIState` 里对应的 case 仍保留，供其他入口（如需要）使用。

**面板重开回到首屏**：面板关闭后再次打开，应回到首屏而不是停在上次的二级页面。具体机制（`onDisappear` 或其他）在实现中验证，因为 `MenuBarExtra` 的视图生命周期可能与普通窗口不同。

## 六、弹窗统一

**`SheetScaffold`**：所有 sheet 共用的脚手架。

- 顶部：标题（左）+ 关闭按钮 `xmark`（右）。
- 关闭按钮绑定 `.keyboardShortcut(.cancelAction)`，`Esc` 可关闭。
- 宽度统一 340，内边距统一 20。
- 主操作按钮固定在底部右侧。

适用：`JoinView`（加入网络）、`SettingsView`（连接设置）、`ManageLoginView`（管理登录，已有"取消"，改为统一样式）。`JoinScannerView`（扫码）已有"取消"，仅统一宽度和顶栏风格。

`CastPairingSheet` 不再是 sheet：它在主窗口里变成二级页面下的页内表单（`CastPairingView`），用 `PageHeader` 的 `‹ 返回` 退出，因此不套 `SheetScaffold`。

## 七、状态组件

**`StateView`**：加载中、出错、空列表三种状态统一为一个组件（图标 + 标题 + 可选说明 + 可选按钮）。取代 `ContentView.mainPanel` 里四段手写的 `Spacer + VStack + Spacer`：未加入网络、加载中、出错、无节点。

## 八、主窗口

- 与面板共用 `ContentView`，自动获得上述全部改动。
- 尺寸：宽 360 不变，高度范围由 420–640 放宽到 480–900。
- 保留 `.hiddenTitleBar` 与 `.windowResizability(.contentMinSize)`。

## 九、文件改动

| 文件 | 改动 |
|---|---|
| `apple/LatticeMac/PanelComponents.swift`（新增） | `PageHeader`、`SheetScaffold`、`StateView`、`PanelNavBar` |
| `apple/LatticeMac/ContentView.swift` | 重构 `mainPanel`；头部加 `⋯` 菜单；移除设备列表里的"退出节点"行、`bottomNav`、`footer`；`subPage` 状态；`JoinView`、`SettingsView`、`ManageLoginView`、`JoinScannerView` 套 `SheetScaffold` |
| `apple/LatticeMac/LatticeMacApp.swift` | `MenuBarPanel` 移除外层页脚；主窗口高度 480–900；`UIState.Page` 新增 `cast`、`castPairing` |
| `apple/LatticeMac/CastReceiver.swift` | `CastSectionView` 改为二级页 `CastPage`；`CastPairingSheet` 改为页内 `CastPairingView` |
| `apple/LatticeMac/NetworkPages.swift` | 套 `PageHeader`；退出节点选择由 sheet 改为页内列表 |
| `apple/LatticeApple.xcodeproj/project.pbxproj` | 新增文件后运行 `xcodegen generate` 重新生成并提交 |

`apple/Shared/DesignComponents.swift` 由 iOS 和 macOS 共用，**不修改**；新组件全部放在 macOS 端，避免影响 iOS。

## 十、错误处理

- 二级页面加载失败沿用现有 `errorMsg` / `opError` 通路，展示改用 `StateView`，不引入新的错误模型。
- 投屏页展示 `CastReceiverManager.startError`（现有），保持红色文字，位置在状态行下方。
- 面板里触发需要输入的流程（编辑配对、节点详情、重命名、静态地址）时，主窗口打开失败没有特殊处理，与现有 `openMain` 行为一致。

## 十一、测试与验证

SwiftUI 界面无法靠单元测试覆盖，验证以构建和实际运行为主：

1. **构建**：`xcodegen generate` 后 macOS target 构建通过。
2. **面板与主窗口截图对照**（各截一张最小高度和一张最大高度）：
   - 弹窗：`JoinView`、`SettingsView`、`ManageLoginView` 有关闭按钮，`Esc` 可关闭
   - 设备列表在最小高度下的可见行数明显多于改动前
   - 网络 / 共享 / 投屏三个二级页面能进入、返回
   - 面板里点"编辑配对"能跳到主窗口并直接显示表单
   - 面板关闭再打开回到首屏
3. **疑似缺陷复现**：改动前先在面板里点"配对信息…"，确认第一节所述的失焦 / 面板消失现象是否属实，结果记入实现计划。
4. **逻辑测试**：若实现中改动到纯逻辑（例如 `subPage` 与 `UIState.Page` 的映射），补到 `apple/Tests/AppleLogicTests.swift`。

## 十二、风险

| 风险 | 缓解 |
|---|---|
| `MenuBarExtra` 里原地替换的二级页面，面板重开后状态不复位 | 验收项之一，实现中验证并处理 |
| 退出节点选择由 sheet 改页内列表，行为与旧版不一致 | 保留原选项与选择逻辑，仅改承载方式 |
| 新增文件未进入 Xcode 工程 | 工程由 `project.yml` 按目录收集，必须重新 `xcodegen generate` 并提交 `pbxproj` |
| 移除首屏投屏卡片后，用户找不到投屏开关 | 导航栏投屏图标带接收状态绿点，进入即是开关 |
