# Lattice iOS · Tailscale 风格界面改版设计

> 日期: 2026-09-16
> 性质: 实现规范（iOS App UI 改版，深度 = 全面模仿）
> 关联: `docs/superpowers/specs/2026-09-16-ios-app-design.md`（核心连接范围，本 spec 的基线）、
>       `docs/superpowers/plans/2026-09-16-ios-app-implementation.md`（已完成的 7 任务实现）
> 参考: Tailscale iOS 客户端（官方博客 "Reimagining Tailscale for iOS"）——参考布局与交互，不做像素级克隆
> 状态: 待评审

---

## 一、目标与原则

把现有 iOS App（双 Tab：状态/设置 + 两步加入流程）的界面升级为 Tailscale 风格：hero 连接卡、设备列表（平台图标/搜索/收藏分组）、Tailscale 式分组设置页、升级版详情页，外加连接计时、连接中呼吸动效与深色模式精修。

原则：

- **参考而非克隆**：吸收 Tailscale 的信息架构与交互模式，视觉细节（配色/圆角/字重）沿用 Lattice 现有 DesignComponents 体系；
- **零后端改动**：全部数据来自现有 `LatticeAPI` 与 `TunnelManager`；
- **长按菜单不造假**：Tailscale 的 Ping 我们没有对应 API，不放假入口；用真实存在的 `renamePeer` / `setPeerDisabled` 替代；
- **中文文案不变**；JoinView 两步加入流程不动；macOS 端不动但 Shared 层改动必须保持向后兼容（macOS 回归构建必须通过）。

## 二、现状基线（改什么、不改什么）

| 现有文件 | 处置 |
|---|---|
| `apple/Lattice/RootView.swift` | 保留双 Tab 结构不动 |
| `apple/Lattice/StatusView.swift` | 重写为本 spec 的首页（文件更名 `OverviewView.swift`，RootView 引用同步改） |
| `apple/Lattice/PeerDetailView.swift` | 升级（§四） |
| `apple/Lattice/SettingsView.swift` | 重组（§五），`leaveNetwork()` 清理逻辑原样保留 |
| `apple/Lattice/ExitNodeView.swift` / `JoinView.swift` / `QRScannerView.swift` | 不动 |
| `apple/Shared/TunnelManager.swift` | 增加 `connectedSince`（§六） |
| `apple/Shared/DesignComponents.swift` | 只增不删：新增 `PlatformIcon`、`ConnectionHero`、`FavoriteStar` |
| `apple/Shared/TunnelCore.swift` | `PeerNode` 不改（字段已够用：`os`/`online`/`lastHandshake`/`disabled`/`displayName`） |
| 新文件 `apple/Lattice/FavoritesStore.swift` | 收藏持久化（§七） |

## 三、首页 OverviewView

**1. Hero 连接卡**（新组件 `ConnectionHero`，放 Shared 以便 macOS 复用）：

- 大号椭圆开关（参考 Tailscale 的椭圆形态），三态：
  - 未连接：灰色，`未连接`
  - 连接中（status ∈ .connecting）：绿色描边 + **呼吸光环动画**（外圈 scale 1.0→1.15 + opacity 0.6→0.1，1.2s 循环）
  - 已连接（.connected）：实心绿 + **实时计时**（`HH:mm:ss` 或不足 1 小时 `mm:ss`）
- 计时数据源：`TunnelManager.connectedSince`（§六），`TimelineView(.periodic(every: 1s))` 驱动刷新；
- 卡下方一行聚合状态：任一 peer `ice-ready` → `直连`；否则存在 `lrp-ready` → `经中继`；否则空。错误红字（`lastStartError`）显示在卡内底部。

**2. 搜索**：`PanelSearchField`（现成），按 `shownName` 与 `address` 不区分大小写过滤，作用于下面两组。

**3. 分组列表**：

- `⭐ 收藏` 组：FavoritesStore 中的 peer，按名称排序；空则整组不渲染；
- `全部设备` 组：其余 peer，在线在前、名称次之排序；
- 行布局：`PlatformIcon`（左）+ 名称（`shownName`）+ IP（monospaced, secondary）+ `HaloDot`（online）+ `QualityPill`（现状逻辑不变）+ 尾部 `FavoriteStar`。

**4. `PlatformIcon` 映射表**（新组件，`os` 字符串 → SF Symbol，集中一处）：

| os（前缀匹配，不区分大小写） | SF Symbol |
|---|---|
| ios / iphone / ipad | `iphone` |
| macos / darwin / mac | `laptopcomputer` |
| windows | `pc` |
| linux / android | `desktopcomputer` |
| 其它/空 | `questionmark.circle` |

**5. 长按上下文菜单**（每行）：

- 复制 IP（`UIPasteboard.general.string = peer.address`）
- 复制名称
- ⭐ 收藏 / 取消收藏
- 重命名（alert + TextField，调 `LatticeAPI.renamePeer`，成功后刷新列表）
- 停用 / 启用（调 `setPeerDisabled`，带确认）

## 四、PeerDetailView 升级

- 头部：大号 `PlatformIcon` + `shownName` + `HaloDot`（online）；
- IP 行：monospaced 展示 + 尾部复制按钮；
- 信息组（`LabeledField`/`LabeledContent`）：平台（os）、在线状态、连接质量（QualityPill 同款语义）、最近握手（`lastHandshake`，已有字段）；
- 操作组：收藏 toggle、重命名、停用/启用（与首页长按菜单同一套动作函数，抽到共用 helper 避免两处实现）；
- 保持只读基线：不做删除 peer、不做路由编辑。

## 五、设置页重组

结构（自上而下）：

1. **账户头**：`lattice.adminUser`（UserDefaults 已有）+ 服务器 URL（`tunnel.serverURL`）；
2. **网络组**：退出节点（`NavigationLink → ExitNodeView()`，现状保留）；
3. **偏好组**：主题 —— `Picker`（跟随系统 / 浅色 / 深色），`@AppStorage("lattice.theme")`，根视图用 `.preferredColorScheme` 应用；
4. **服务器信息组**：服务器 URL、本机节点名（`lattice.nodeName`）、版本（现状逻辑）；
5. **退出网络**：现状 `leaveNetwork()` 逻辑原样保留（含确认弹窗与全部本地清理）。

`lattice.theme` 的应用位置：`LatticeApp.swift` 根部统一 `.preferredColorScheme(themeScheme)`，三值映射 `nil / .light / .dark`。

## 六、TunnelManager 增量

- 新增 `@Published private(set) var connectedSince: Date?`；
- `refreshStatus()`/`observeStatus()` 中：status 变为 `.connected` 且 `connectedSince == nil` 时置为 `Date()`；status 离开 `.connected` 时置 `nil`；
- App 冷启动时连接已在跑的场景：`connectedSince` 无法从系统取回真实起点，用**本次会话发现连接的时刻**作为起点（计时显示的是"本App会话内已连接时长"），该语义在 UI 无需解释；
- 其余（saveJoin/connect/disconnect/pollPeerStates）一律不动。

## 七、FavoritesStore（新文件，iOS-only）

- `final class FavoritesStore: ObservableObject`，`@Published private(set) var names: Set<String>`；
- 持久化：UserDefaults key `lattice.favoritePeers`，JSON 数组（仅存 peer name）；
- API：`toggle(_ name: String)`、`isFavorite(_ name: String) -> Bool`、`containsAny` 辅助；
- 已注销网络的残留收藏无需清理：列表渲染按当前 peers 过滤，孤儿项自然不可见；
- 放 iOS target（macOS 不引入）。

## 八、深色模式

- 所有新 UI 使用语义色（`.primary` / `.secondary` / `.background`）与现有 DesignComponents 令牌；hero 卡背景 `.regularMaterial`；
- 不引入硬编码色值（绿色态可用 `.green` 系统语义色）；
- 验收：模拟器分别以浅色/深色 + 主题 Picker 三种取值截图核对首页/设置/详情三屏。

## 九、非目标

- Ping/SSH 等网络操作入口（无 API）；
- 删除 peer、路由编辑（超出现有只读详情基线）；
- macOS 端界面改版（仅要求 Shared 改动不破坏其构建）；
- JoinView / QRScanner / ExitNodeView 的任何改动；
- 后端任何改动。

## 十、验收标准

1. `xcodebuild` iOS（模拟器 destination）与 macOS（LatticeMac scheme）双构建 `** BUILD SUCCEEDED **`；
2. AX 自动化模拟器走查：加入流程后进入首页，hero 卡状态正确；搜索过滤生效；收藏 toggle 后分组出现且重启 App 仍在（UserDefaults 持久化）；重命名/停用走通；深浅色三屏核对；
3. 真机人工清单：连接/断开切换、计时准确性、连接中呼吸动画、直连/经中继聚合显示；
4. 现有回归：macOS 面板功能不受 Shared 改动影响。

## 十一、风险

| 风险 | 应对 |
|---|---|
| Shared 层改动破坏 macOS 构建 | 新组件全部为纯新增；每次改动跑 LatticeMac 构建 |
| `connectedSince` 冷启动起点不准 | 已定义语义为"本会话内时长"（§六），不做系统级回溯 |
| AX 自动化对 SwiftUI 自绘动画验证有限 | 动画验收以人工/截图为准，自动化只验证状态与数据 |
