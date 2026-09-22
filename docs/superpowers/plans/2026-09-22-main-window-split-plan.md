# 主窗口左右分栏重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 主窗口尺寸与面板解耦（默认 760×640，最小宽 680），宽窗口下左栏（状态+设备列表+导航 320 固定）+ 右侧内容区（概览页 / 节点详情宽版 / 二级页）左右分栏；窄窗口或面板自动回退现有单列形态。

**Architecture:** `ContentView.body` 外套 `GeometryReader`，`!inPanel && width >= 680` 时走 `HStack{ leftColumn; Divider(); rightPane }`，否则走现有单列 VStack。两种形态复用同一套子视图（homeScreen / subPageView / PeerDetailView），`PeerDetailView` 增加 `wide` 参数控制指标卡横排与折线加高。数据层零改动。

**Tech Stack:** Swift/SwiftUI（GeometryReader）。

**Spec:** 本文即设计（用户已确认草图）。关联：`2026-09-22-mac-panel-redesign-design.md`、`2026-09-22-mac-client-capabilities-design.md`。

## Global Constraints

面板（340）行为完全不变；单 commit、`-s`、无 Co-Authored-By；中文文案；`BUILD` 不带 CODE_SIGNING_ALLOWED=NO；无 Go 改动（不重 bind、lint 照跑）。

## File Structure

| 文件 | 职责 | 操作 |
|---|---|---|
| `apple/LatticeMac/LatticeMacApp.swift` | 主窗口尺寸解耦（minWidth 680 / defaultSize 760×640） | 修改 |
| `apple/LatticeMac/ContentView.swift` | GeometryReader 分栏 + leftColumn/rightPane/switchArea/overviewPane | 修改 |
| `apple/LatticeMac/PeerDetailView.swift` | `wide` 参数：四张指标卡横排、折线加高 | 修改 |

## Tasks

### Task 1: 窗口尺寸解耦（LatticeMacApp）

```swift
        Window("Lattice", id: "main") {
            ContentView()
                .frame(minWidth: 680, minHeight: 480, maxHeight: 1000)
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 760, height: 640)
        .windowResizability(.contentMinSize)
```

### Task 2: ContentView 分栏

1. `body` 改为 `GeometryReader { geo in content(split: !inPanel && geo.size.width >= 680) }`，全部现有修饰符（background/onAppear/onChange/onReceive/sheet/alert）挂在 GeometryReader 外层不变。
2. `content(split:)`：split → `HStack { leftColumn; Divider(); rightPane }`；否则现有 `VStack { switchArea; banner; nav }`。
3. `leftColumn = VStack { homeScreen; banner; PanelNavBar }.frame(width: 320)`。
4. `switchArea`：detail → PeerDetailView(wide: false)；subPage → subPageView；nil → homeScreen（现状）。
5. `rightPane`：detail → PeerDetailView(wide: true)；subPage → `subPageView(page).frame(maxWidth: 560, alignment: .leading)`；nil → `overviewPane`。
6. `overviewPane`（新增，刻意小）：三张卡片（设备在线数 / 直连数 / 隧道状态）+ 本机 overlay 地址 + 待审批提示；数据全部来自现有 `tunnel` / `pendingPeers`。
7. `onBack` 闭包不改：split 模式下 detailPeer=nil / subPage=nil 自然回到概览页。

### Task 3: PeerDetailView 宽版

- 加 `var wide: Bool = false`。
- `connectionSection`：wide 时四个指标改为等宽卡片（`metricCard`：圆角底 + 大号数值），非 wide 保持现有紧凑两行；折线高 wide 36 / 紧凑 26。
- 其余区块（策略/AI/操作）不动，天然吃满右栏宽度。

### Task 4: 验证与提交

- [ ] `BUILD` + `bash Scripts/test_apple_logic.sh` 全绿
- [ ] 重启 App；人工确认：① 默认 760 宽左右分栏；② 拉窄 <680 回退单列；③ 点节点右栏出宽版详情、列表常驻；④ 面板 340 行为不变
- [ ] `git add … && git commit -s -m "feat(apple): adaptive two-pane main window …"`

## Self-Review

- 设计草图五点全覆盖（尺寸解耦/左栏导航/右栏三态/自适应回退/详情宽版）；面板零改动。
- 人工验证项：真实拖拽手感与概览页观感（代理无法操作 UI，如实交用户）。
