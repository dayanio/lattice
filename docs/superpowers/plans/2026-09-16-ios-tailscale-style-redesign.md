# iOS Tailscale-Style UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-09-16):** Tasks 1-6 implemented, reviewed, and committed (3908a28b…3d696dfe + Task 7 verification). Device-only connection-state checks remain for a real iPhone (NetworkExtension cannot run in the iOS Simulator).

**Goal:** Restyle the Lattice iOS app to reference Tailscale's iOS design — hero connection card with live timer and breathing animation, searchable favorites-grouped device list with platform icons, long-press actions, upgraded peer detail, Tailscale-style grouped settings with a theme picker — per `docs/superpowers/specs/2026-09-16-ios-tailscale-style-redesign-design.md`.

**Architecture:** All changes are app-side only. Shared components (`DesignComponents.swift`, `TunnelManager.swift`) get additive-only changes so the macOS target keeps compiling; new iOS-only code (`OverviewView`, `FavoritesStore`, `PeerActions`) lives under `apple/Lattice/`. No backend calls change — `listPeers`/`renamePeer`/`setPeerDisabled` and the NE tunnel status are the only data sources.

**Tech Stack:** SwiftUI (iOS 17+), NetworkExtension status, XcodeGen (`apple/project.yml` — new files under `Lattice/` or `Shared/` are picked up by `xcodegen generate`), XCTest is NOT set up in this repo — verification = `xcodebuild` on both platforms + SwiftUI `#Preview` + simulator AX walkthrough (same model as `2026-09-16-ios-app-implementation.md`).

## Global Constraints

- 中文文案保持不变；现有 join 流程（`JoinView`/`QRScannerView`）与 `ExitNodeView` 一律不动。
- `apple/Shared/` 改动只增不删；每个任务结束跑 macOS 回归构建，必须 `** BUILD SUCCEEDED **`。
- 不做 Ping 入口（后端无 API）；不做删除 peer / 路由编辑；后端零改动。
- 新 UI 一律用语义色（`.primary`/`.secondary`）+ `LatticePalette` 令牌（`apple/Shared/DesignComponents.swift:16`）；hero 卡背景用 `.regularMaterial`；不写硬编码色值。
- 构建命令（本计划统一验证步）：
  - iOS：`cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -5`
  - macOS：`cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -5`
  - 新增/删除文件后必须：`cd apple && xcodegen generate`
- 提交信息用 `feat(apple):` / `refactor(apple):` / `chore(apple):` 前缀，与近史一致。

---

### Task 1: Shared 设计组件 — `ConnectionState` / `ConnectionHero` / `PlatformIcon` / `FavoriteStar`

**Files:**
- Modify: `apple/Shared/DesignComponents.swift`（文件末尾追加；只增不删）

**Interfaces:**
- Produces（后续任务依赖的精确签名）:
  - `enum ConnectionState { case disconnected, connecting, connected }`
  - `struct ConnectionHero: View` — `init(state: ConnectionState, connectedSince: Date?, aggregateText: String, selfAddress: String, errorText: String, onToggle: () -> Void)`
  - `struct PlatformIcon: View` — `init(os: String, size: CGFloat = 30)`
  - `struct FavoriteStar: View` — `init(isOn: Bool, action: () -> Void)`

- [x] **Step 1: 追加组件实现**

在 `apple/Shared/DesignComponents.swift` 末尾追加：

```swift
// MARK: - Connection hero (Tailscale-style)

/// Hero 连接状态，从 NetworkExtension 解耦，便于预览与复用。
enum ConnectionState { case disconnected, connecting, connected }

/// 大号椭圆连接开关：未连接灰 / 连接中呼吸光环 / 已连接实心绿 + 实时计时。
struct ConnectionHero: View {
    let state: ConnectionState
    let connectedSince: Date?
    let aggregateText: String
    var selfAddress: String = ""
    var errorText: String = ""
    let onToggle: () -> Void

    @State private var breathe = false

    private var heroColor: Color {
        switch state {
        case .connected: return LatticePalette.online
        case .connecting: return LatticePalette.online.opacity(0.55)
        case .disconnected: return LatticePalette.neutral
        }
    }

    var body: some View {
        VStack(spacing: 10) {
            ZStack {
                if state == .connecting {
                    Capsule()
                        .stroke(heroColor, lineWidth: 2)
                        .frame(width: 158, height: 82)
                        .scaleEffect(breathe ? 1.12 : 1.0)
                        .opacity(breathe ? 0.15 : 0.55)
                }
                Capsule()
                    .fill(state == .connected ? heroColor.opacity(0.14) : Color.primary.opacity(0.06))
                    .frame(width: 150, height: 74)
                    .overlay(Capsule().stroke(heroColor, lineWidth: state == .disconnected ? 1.5 : 2.5))
                HStack(spacing: 12) {
                    HaloDot(color: heroColor, size: 14)
                    Text(headline)
                        .font(.system(size: 17, weight: .bold))
                        .foregroundColor(state == .disconnected ? .primary : heroColor)
                }
            }
            .frame(width: 150, height: 74)
            .contentShape(Capsule())
            .onTapGesture { onToggle() }
            .onChange(of: state) { _, newState in
                breathe = false
                if newState == .connecting {
                    withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                        breathe = true
                    }
                }
            }
            .onAppear {
                if state == .connecting {
                    withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                        breathe = true
                    }
                }
            }

            TimelineView(.periodic(from: .now, by: 1)) { context in
                Text(timerText(at: context.date))
                    .font(.system(size: 13, weight: .semibold, design: .monospaced))
                    .foregroundColor(.secondary)
            }

            VStack(spacing: 2) {
                if !aggregateText.isEmpty {
                    Text(aggregateText)
                        .font(.system(size: 12, weight: .bold))
                        .foregroundColor(aggregateText == "直连" ? LatticePalette.online : LatticePalette.relay)
                }
                if !selfAddress.isEmpty {
                    Text("本机 \(selfAddress)")
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundColor(.secondary)
                }
                if !errorText.isEmpty {
                    Text(errorText)
                        .font(.caption)
                        .foregroundColor(LatticePalette.blocked)
                        .multilineTextAlignment(.center)
                }
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 18)
        .background(RoundedRectangle(cornerRadius: 16).fill(.regularMaterial))
        .padding(.horizontal, 15)
    }

    private var headline: String {
        switch state {
        case .connected: return "已连接"
        case .connecting: return "连接中…"
        case .disconnected: return "未连接"
        }
    }

    /// 计时语义（见 spec §六）：connectedSince 是本 App 会话内发现连接的时刻。
    private func timerText(at now: Date) -> String {
        guard state == .connected, let since = connectedSince else { return "" }
        let secs = max(0, Int(now.timeIntervalSince(since)))
        let h = secs / 3600, m = (secs % 3600) / 60, s = secs % 60
        return h > 0 ? String(format: "%02d:%02d:%02d", h, m, s) : String(format: "%02d:%02d", m, s)
    }
}

/// peer 平台图标：os 字符串（前缀、不区分大小写）→ SF Symbol。
struct PlatformIcon: View {
    let os: String
    var size: CGFloat = 30

    private var symbol: String {
        let o = os.lowercased()
        if o.hasPrefix("ios") || o.hasPrefix("iphone") || o.hasPrefix("ipad") { return "iphone" }
        if o.hasPrefix("macos") || o.hasPrefix("darwin") || o.hasPrefix("mac") { return "laptopcomputer" }
        if o.hasPrefix("windows") { return "pc" }
        if o.hasPrefix("linux") || o.hasPrefix("android") { return "desktopcomputer" }
        return "questionmark.circle"
    }

    var body: some View {
        RoundedRectangle(cornerRadius: size * 0.24)
            .fill(LatticePalette.accent.opacity(0.14))
            .frame(width: size, height: size)
            .overlay(
                Image(systemName: symbol)
                    .font(.system(size: size * 0.48, weight: .semibold))
                    .foregroundColor(LatticePalette.accent)
            )
    }
}

/// 行尾/菜单共用的收藏星标。
struct FavoriteStar: View {
    let isOn: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: isOn ? "star.fill" : "star")
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(isOn ? LatticePalette.relay : .secondary)
        }
        .buttonStyle(.plain)
    }
}
```

- [x] **Step 2: 双平台构建验证**

Run: `cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`

Run: `xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`

- [x] **Step 3: Commit**

```bash
git add apple/Shared/DesignComponents.swift
git commit -m "feat(apple): connection hero, platform icon, favorite star components"
```

---

### Task 2: TunnelManager 增加 `connectedSince` 与 `connectionState`

**Files:**
- Modify: `apple/Shared/TunnelManager.swift`（`@Published` 区约 35-40 行、`refreshStatus()` 约 157-167 行）

**Interfaces:**
- Consumes: 现有 `private func refreshStatus()`。
- Produces:
  - `@Published private(set) var connectedSince: Date?`
  - `var connectionState: ConnectionState`（计算属性，Task 4 首页消费）

- [x] **Step 1: 加属性**

在 `@Published private(set) var peerStates: [String: String] = [:]`（约 40 行）之后加：

```swift
    /// 本 App 会话内连接建立的时刻（spec §六：冷启动无法取回系统真实起点，
    /// 用"发现连接的时刻"作为计时起点，离开 connected 即清空）。
    @Published private(set) var connectedSince: Date?
```

- [x] **Step 2: 在 refreshStatus 中维护**

`refreshStatus()` 改为：

```swift
    private func refreshStatus() {
        status = manager?.connection.status ?? .invalid
        if status == .connected {
            if connectedSince == nil { connectedSince = Date() }
            startStatePoller()
        } else {
            connectedSince = nil
            stopStatePoller()
            if peerStates.isEmpty == false {
                peerStates = [:]
            }
        }
    }
```

并在 `var connectedBinding: Binding<Bool>`（约 58 行）之后加计算属性：

```swift
    /// UI 侧连接态（ConnectionHero 消费；不暴露 NEVPNStatus 给组件层）。
    var connectionState: ConnectionState {
        switch status {
        case .connected: return .connected
        case .connecting, .disconnecting, .reasserting: return .connecting
        default: return .disconnected
        }
    }
```

- [x] **Step 3: 双平台构建验证**

同 Task 1 的两条构建命令。Expected: 均 `** BUILD SUCCEEDED **`

- [x] **Step 4: Commit**

```bash
git add apple/Shared/TunnelManager.swift
git commit -m "feat(apple): expose connectedSince and connectionState on TunnelManager"
```

---

### Task 3: FavoritesStore（iOS-only 收藏持久化）

**Files:**
- Create: `apple/Lattice/FavoritesStore.swift`

**Interfaces:**
- Produces: `final class FavoritesStore: ObservableObject` — `@Published private(set) var names: Set<String>`；`init()`；`func isFavorite(_ name: String) -> Bool`；`func toggle(_ name: String)`。UserDefaults key 固定 `lattice.favoritePeers`。

- [x] **Step 1: 实现**

```swift
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 收藏的 peer（按 name 持久化到 UserDefaults）。已注销网络的残留收藏
/// 无需清理：首页渲染按当前 peers 过滤，孤儿项自然不可见（spec §七）。
final class FavoritesStore: ObservableObject {
    @Published private(set) var names: Set<String> = []

    private static let key = "lattice.favoritePeers"

    init() { load() }

    func isFavorite(_ name: String) -> Bool { names.contains(name) }

    func toggle(_ name: String) {
        if names.contains(name) {
            names.remove(name)
        } else {
            names.insert(name)
        }
        save()
    }

    private func load() {
        guard let data = UserDefaults.standard.data(forKey: Self.key),
              let list = try? JSONDecoder().decode([String].self, from: data) else { return }
        names = Set(list)
    }

    private func save() {
        if let data = try? JSONEncoder().encode(names.sorted()) {
            UserDefaults.standard.set(data, forKey: Self.key)
        }
    }
}
```

- [x] **Step 2: 注册进 Xcode 工程并构建**

Run: `cd apple && xcodegen generate && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`（`sources: [Lattice, Shared]` 自动纳入新文件；macOS target 不含 `Lattice/` 目录，无需回归——但仍跑一次 macOS 构建确认无意外：Expected 同上）

- [x] **Step 3: Commit**

```bash
git add apple/Lattice/FavoritesStore.swift apple/LatticeApple.xcodeproj/project.pbxproj
git commit -m "feat(apple): favorites store with UserDefaults persistence"
```

---

### Task 4: 首页重写 OverviewView + 共享动作 PeerActions

**Files:**
- Create: `apple/Lattice/OverviewView.swift`（`git mv apple/Lattice/StatusView.swift apple/Lattice/OverviewView.swift` 后重写）
- Create: `apple/Lattice/PeerActions.swift`
- Modify: `apple/Lattice/RootView.swift`（`StatusView()` → `OverviewView()`；DEBUG 跳过加入开关）

**Interfaces:**
- Consumes: Task 1 组件、Task 2 `tunnel.connectionState`/`connectedSince`、Task 3 `FavoritesStore`、`LatticeAPI.listPeers/renamePeer/setPeerDisabled`、`PeerNode`（`TunnelCore.swift:25`）。
- Produces: `struct OverviewView: View`；`enum PeerActions`（Task 5 详情页复用）：`static func copyToClipboard(_ text: String)`、`static func rename(_ peer: PeerNode, to newName: String) async throws`、`static func setDisabled(_ peer: PeerNode, _ disabled: Bool) async throws`、`static func qualityPill(_ state: String) -> (text: String, color: Color)?`（从现 StatusView 迁移）。

- [x] **Step 1: git mv 并写 PeerActions**

```bash
git mv apple/Lattice/StatusView.swift apple/Lattice/OverviewView.swift
```

Create `apple/Lattice/PeerActions.swift`:

```swift
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import UIKit

/// 首页长按菜单与详情页共用的 peer 动作（spec §四：动作函数只写一处）。
enum PeerActions {
    static func copyToClipboard(_ text: String) {
        UIPasteboard.general.string = text
    }

    static func rename(_ peer: PeerNode, to newName: String) async throws {
        try await LatticeAPI.shared.renamePeer(peer.name, displayName: newName)
    }

    static func setDisabled(_ peer: PeerNode, _ disabled: Bool) async throws {
        try await LatticeAPI.shared.setPeerDisabled(peer.name, disabled)
    }

    /// 质量态 → pill 文案与颜色（自原 StatusView.qualityPill 迁移）。
    static func qualityPill(_ state: String) -> (text: String, color: Color)? {
        switch state {
        case "ice-ready": return ("直连", LatticePalette.online)
        case "lrp-ready": return ("经中继", LatticePalette.relay)
        case "probing", "created": return ("连接中", LatticePalette.neutral)
        case "failed": return ("失败", LatticePalette.blocked)
        default: return nil
        }
    }
}
```

- [x] **Step 2: 重写 OverviewView**

`apple/Lattice/OverviewView.swift` 全文替换为：

```swift
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 首页：hero 连接卡 + 搜索 + ⭐收藏/全部设备两组（spec §三）。
struct OverviewView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @StateObject private var favorites = FavoritesStore()
    @State private var peers: [PeerNode] = []
    @State private var searchText = ""
    @State private var isLoading = false
    @State private var errorMsg = ""
    @State private var renamingPeer: PeerNode?
    @State private var renameText = ""
    @State private var disablingPeer: PeerNode?
    @Environment(\.scenePhase) private var scenePhase

    private var selfName: String { UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "" }
    private var localPeer: PeerNode? { peers.first { $0.name == selfName } }

    private var aggregateText: String {
        let states = peers.compactMap { tunnel.peerStates[$0.name] }
        if states.contains("ice-ready") { return "直连" }
        if states.contains("lrp-ready") { return "经中继" }
        return ""
    }

    private var filtered: [PeerNode] {
        let kw = searchText.trimmingCharacters(in: .whitespaces).lowercased()
        guard !kw.isEmpty else { return peers }
        return peers.filter { $0.shownName.lowercased().contains(kw) || $0.address.lowercased().contains(kw) }
    }
    private var favoritePeers: [PeerNode] {
        filtered.filter { favorites.isFavorite($0.name) }.sorted { $0.shownName < $1.shownName }
    }
    private var otherPeers: [PeerNode] {
        filtered.filter { !favorites.isFavorite($0.name) }
            .sorted { ($0.online ? 0 : 1, $0.shownName) < ($1.online ? 0 : 1, $1.shownName) }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 4) {
                    ConnectionHero(
                        state: tunnel.connectionState,
                        connectedSince: tunnel.connectedSince,
                        aggregateText: aggregateText,
                        selfAddress: localPeer?.address ?? "",
                        errorText: tunnel.lastStartError,
                        onToggle: { tunnel.connectedBinding.wrappedValue.toggle() }
                    )
                    PanelSearchField(text: $searchText)

                    if isLoading && peers.isEmpty {
                        ProgressView().padding(.top, 30)
                    } else if !errorMsg.isEmpty {
                        Text(errorMsg)
                            .font(.caption)
                            .foregroundColor(LatticePalette.blocked)
                            .padding(.top, 30)
                    } else if filtered.isEmpty {
                        Text(searchText.isEmpty ? "暂无节点" : "无匹配设备")
                            .font(.caption)
                            .foregroundColor(.secondary)
                            .padding(.top, 30)
                    } else {
                        if !favoritePeers.isEmpty {
                            SectionHead(title: "⭐ 收藏")
                            ForEach(favoritePeers) { peerRow($0) }
                        }
                        SectionHead(title: "全部设备")
                        ForEach(otherPeers) { peerRow($0) }
                    }
                }
                .padding(.bottom, 12)
            }
            .navigationTitle("Lattice")
            .refreshable { await loadPeers() }
            .task { await loadPeers() }
            .onChange(of: scenePhase) { _, newPhase in
                if newPhase == .active { Task { await loadPeers() } }
            }
            .alert("重命名设备", isPresented: .init(
                get: { renamingPeer != nil },
                set: { if !$0 { renamingPeer = nil } }
            )) {
                TextField("新名称", text: $renameText)
                Button("确定") {
                    if let peer = renamingPeer {
                        Task {
                            try? await PeerActions.rename(peer, to: renameText)
                            await loadPeers()
                        }
                    }
                }
                Button("取消", role: .cancel) {}
            }
            .confirmationDialog(
                "停用 \"\(disablingPeer?.shownName ?? "")\"？",
                isPresented: .init(
                    get: { disablingPeer != nil },
                    set: { if !$0 { disablingPeer = nil } }
                ),
                titleVisibility: .visible
            ) {
                Button("停用", role: .destructive) {
                    if let peer = disablingPeer {
                        Task {
                            try? await PeerActions.setDisabled(peer, true)
                            await loadPeers()
                        }
                    }
                }
                Button("取消", role: .cancel) {}
            }
        }
    }

    private func peerRow(_ peer: PeerNode) -> some View {
        NavigationLink {
            PeerDetailView(peer: peer, quality: tunnel.peerStates[peer.name])
        } label: {
            HStack(spacing: 10) {
                PlatformIcon(os: peer.os)
                VStack(alignment: .leading, spacing: 2) {
                    Text(peer.shownName).font(.system(.body))
                    Text(peer.address)
                        .font(.system(.caption, design: .monospaced))
                        .foregroundColor(.secondary)
                }
                Spacer()
                if let quality = tunnel.peerStates[peer.name],
                   let pill = PeerActions.qualityPill(quality) {
                    QualityPill(text: pill.text, color: pill.color)
                }
                HaloDot(color: peer.online ? LatticePalette.online : .secondary)
                FavoriteStar(isOn: favorites.isFavorite(peer.name)) {
                    favorites.toggle(peer.name)
                }
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button { PeerActions.copyToClipboard(peer.address) } label: { Label("复制 IP", systemImage: "doc.on.doc") }
            Button { PeerActions.copyToClipboard(peer.shownName) } label: { Label("复制名称", systemImage: "doc.on.doc") }
            Button { favorites.toggle(peer.name) } label: {
                Label(favorites.isFavorite(peer.name) ? "取消收藏" : "收藏",
                      systemImage: favorites.isFavorite(peer.name) ? "star.slash" : "star")
            }
            Button { renamingPeer = peer; renameText = peer.shownName } label: { Label("重命名", systemImage: "pencil") }
            Button(role: .destructive) { disablingPeer = peer } label: {
                Label("停用", systemImage: "nosign")
            }
        }
    }

    private func loadPeers() async {
        isLoading = true
        errorMsg = ""
        defer { isLoading = false }
        do {
            peers = try await LatticeAPI.shared.listPeers()
        } catch {
            errorMsg = "加载失败: \(error.localizedDescription)"
        }
    }
}
```

- [x] **Step 3: RootView 改引用 + DEBUG 跳过加入**

`apple/Lattice/RootView.swift`：`StatusView()` 改为 `OverviewView()`（含 tabItem 不变）；`evaluateJoinState()` 改为：

```swift
    private func evaluateJoinState() {
        #if DEBUG
        if ProcessInfo.processInfo.environment["LATTICE_DEBUG_SKIP_JOIN"] == "1" {
            needsJoin = false
            return
        }
        #endif
        needsJoin = !tunnel.isConfigured || !LatticeAPI.shared.isLoggedIn
    }
```

- [x] **Step 4: 构建验证 + 模拟器冒烟**

Run: `cd apple && xcodegen generate && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`

Run: `xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -3`
Expected: `** BUILD SUCCEEDED **`

模拟器（验证首页空态/错误态渲染与 hero 卡）：

```bash
xcrun simctl boot A4BA97DA-DB8D-4D46-89E2-E229904CAED9 2>/dev/null; true
APP=$(find ~/Library/Developer/Xcode/DerivedData/LatticeApple-*/Build/Products/Debug-iphonesimulator -name "Lattice.app" -print -quit)
xcrun simctl install A4BA97DA-DB8D-4D46-89E2-E229904CAED9 "$APP"
SIMCTL_CHILD_LATTICE_DEBUG_SKIP_JOIN=1 xcrun simctl launch A4BA97DA-DB8D-4D46-89E2-E229904CAED9 io.lattice.ios
xcrun simctl io A4BA97DA-DB8D-4D46-89E2-E229904CAED9 screenshot /tmp/lattice-overview.png
```

Read `/tmp/lattice-overview.png`：应看到 hero 卡（未连接/灰）+ 搜索框 + 节点区（"暂无节点"或"加载失败"——模拟器未登录属预期），无崩溃。

- [x] **Step 5: Commit**

```bash
git add apple/Lattice/OverviewView.swift apple/Lattice/PeerActions.swift apple/Lattice/RootView.swift apple/LatticeApple.xcodeproj/project.pbxproj
git commit -m "feat(apple): Tailscale-style overview with hero card, search, favorites groups"
```

---

### Task 5: PeerDetailView 升级

**Files:**
- Modify: `apple/Lattice/PeerDetailView.swift`（86 行，全文替换）

**Interfaces:**
- Consumes: `PlatformIcon`/`FavoriteStar`（Task 1）、`FavoritesStore`（Task 3）、`PeerActions`（Task 4）、`LatticePalette`。

- [x] **Step 1: 全文替换**

```swift
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 只读 peer 详情 + 复制/收藏/重命名/停用动作（spec §四）。
struct PeerDetailView: View {
    let peer: PeerNode
    let quality: String?

    @StateObject private var favorites = FavoritesStore()
    @State private var currentDisabled: Bool
    @State private var showingRename = false
    @State private var renameText = ""
    @State private var copied = false

    init(peer: PeerNode, quality: String?) {
        self.peer = peer
        self.quality = quality
        _currentDisabled = State(initialValue: peer.disabled)
    }

    var body: some View {
        List {
            Section {
                VStack(spacing: 8) {
                    PlatformIcon(os: peer.os, size: 56)
                    Text(peer.shownName).font(.system(.title3, design: .rounded)).bold()
                    HStack(spacing: 6) {
                        HaloDot(color: peer.online ? LatticePalette.online : .secondary)
                        Text(peer.online ? "在线" : "离线")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
            }

            Section {
                HStack {
                    Text("IP 地址")
                    Spacer()
                    Text(peer.address)
                        .font(.system(.body, design: .monospaced))
                    Button {
                        PeerActions.copyToClipboard(peer.address)
                        copied = true
                        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
                    } label: {
                        Image(systemName: copied ? "checkmark" : "doc.on.doc")
                            .foregroundColor(LatticePalette.accent)
                    }
                    .buttonStyle(.plain)
                }
                LabeledContent("平台", value: peer.os.isEmpty ? "未知" : peer.os)
                LabeledContent("连接质量") {
                    if let quality, let pill = PeerActions.qualityPill(quality) {
                        QualityPill(text: pill.text, color: pill.color)
                    } else {
                        Text("—").foregroundColor(.secondary)
                    }
                }
                LabeledContent("最近握手", value: peer.lastHandshake)
            }

            Section {
                Button {
                    PeerActions.copyToClipboard(peer.address)
                    copied = true
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
                } label: {
                    Label(copied ? "已复制" : "复制 IP 地址", systemImage: copied ? "checkmark" : "doc.on.doc")
                }
                Button { favorites.toggle(peer.name) } label: {
                    Label(favorites.isFavorite(peer.name) ? "取消收藏" : "收藏",
                          systemImage: favorites.isFavorite(peer.name) ? "star.fill" : "star")
                }
                Button { showingRename = true; renameText = peer.shownName } label: {
                    Label("重命名", systemImage: "pencil")
                }
                Button(role: .destructive) {
                    Task {
                        try? await PeerActions.setDisabled(peer, !currentDisabled)
                        currentDisabled.toggle()
                    }
                } label: {
                    Label(currentDisabled ? "启用" : "停用", systemImage: currentDisabled ? "checkmark.circle" : "nosign")
                }
            }
        }
        .navigationTitle(peer.shownName)
        .navigationBarTitleDisplayMode(.inline)
        .alert("重命名设备", isPresented: $showingRename) {
            TextField("新名称", text: $renameText)
            Button("确定") {
                Task { try? await PeerActions.rename(peer, to: renameText) }
            }
            Button("取消", role: .cancel) {}
        }
    }
}
```

**实现注意**：上面 `FavoriteStar-likeCopyButton` 是占位记号——落码时该处写 `Button { PeerActions.copyToClipboard(peer.address); copied = true; DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false } } label: { Image(systemName: copied ? "checkmark" : "doc.on.doc").foregroundColor(LatticePalette.accent) }.buttonStyle(.plain)`；同时删除下方操作组里重复的"复制 IP"行内状态提示可自行取舍，两处都用 `copied` 态即可。若 `LabeledContent` 尾闭包形式编译器类型推断报错，改用 `HStack { Text("IP 地址"); Spacer(); Text(peer.address)...; copyButton }` 的手写布局。

- [x] **Step 2: 双平台构建验证**

同 Task 1 的两条构建命令。Expected: 均 `** BUILD SUCCEEDED **`

- [x] **Step 3: Commit**

```bash
git add apple/Lattice/PeerDetailView.swift
git commit -m "feat(apple): Tailscale-style peer detail with copy and actions"
```

---

### Task 6: 设置页重组 + 主题切换

**Files:**
- Modify: `apple/Lattice/SettingsView.swift`（重组 body；`leaveNetwork()` 原样保留）
- Modify: `apple/Lattice/LatticeApp.swift`（应用 `preferredColorScheme`）

**Interfaces:**
- Produces: `enum LatticeTheme: String, CaseIterable`（`system`/`light`/`dark`，UserDefaults key `lattice.theme` 经 `@AppStorage` 读写）。

- [x] **Step 1: 主题枚举 + 设置页 body 重组**

`apple/Lattice/SettingsView.swift`：在 import 后加：

```swift
/// 主题三选（spec §五），@AppStorage 持久化，键 lattice.theme。
enum LatticeTheme: String, CaseIterable, Identifiable {
    case system, light, dark
    var id: String { rawValue }
    var label: String {
        switch self {
        case .system: return "跟随系统"
        case .light: return "浅色"
        case .dark: return "深色"
        }
    }
    var colorScheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}
```

`SettingsView` 加属性 `@AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue`，body 的 `List` 改为：

```swift
            List {
                Section {
                    HStack(spacing: 12) {
                        Image(systemName: "person.crop.circle.fill")
                            .font(.system(size: 40))
                            .foregroundColor(LatticePalette.accent)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin")
                                .font(.system(.body, weight: .semibold))
                            Text(tunnel.serverURL ?? "—")
                                .font(.system(.caption, design: .monospaced))
                                .foregroundColor(.secondary)
                        }
                    }
                    .padding(.vertical, 4)
                }

                Section("网络") {
                    NavigationLink("退出节点") { ExitNodeView() }
                    LabeledContent("本机节点", value: UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "—")
                }

                Section("偏好") {
                    Picker("主题", selection: $theme) {
                        ForEach(LatticeTheme.allCases) { t in
                            Text(t.label).tag(t.rawValue)
                        }
                    }
                }

                Section {
                    LabeledContent("版本", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                }

                Section {
                    Button("退出网络", role: .destructive) { showingLeaveConfirm = true }
                }
            }
```

（`leaveNetwork()`、`showingLeaveConfirm`、confirmationDialog 原样不动。）

- [x] **Step 2: LatticeApp 应用主题**

`apple/Lattice/LatticeApp.swift` 全文替换为：

```swift
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

@main
struct LatticeApp: App {
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue

    var body: some Scene {
        WindowGroup {
            RootView()
                .preferredColorScheme(LatticeTheme(rawValue: theme)?.colorScheme)
        }
    }
}
```

- [x] **Step 3: 双平台构建验证**

同 Task 1 的两条构建命令。Expected: 均 `** BUILD SUCCEEDED **`

- [x] **Step 4: Commit**

```bash
git add apple/Lattice/SettingsView.swift apple/Lattice/LatticeApp.swift
git commit -m "feat(apple): grouped settings with account header and theme picker"
```

---

### Task 7: 终验 — 双构建 + 深浅色与交互走查

**Files:**
- Modify: `docs/superpowers/plans/2026-09-16-ios-tailscale-style-redesign.md`（回填 checkbox）

- [x] **Step 1: 干净全量双构建**

```bash
cd apple && xcodegen generate
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -3
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -3
```
Expected: 两条均 `** BUILD SUCCEEDED **`

- [x] **Step 2: 模拟器走查（DEBUG 跳过加入）**

```bash
xcrun simctl boot A4BA97DA-DB8D-4D46-89E2-E229904CAED9 2>/dev/null; true
APP=$(find ~/Library/Developer/Xcode/DerivedData/LatticeApple-*/Build/Products/Debug-iphonesimulator -name "Lattice.app" -print -quit)
xcrun simctl install A4BA97DA-DB8D-4D46-89E2-E229904CAED9 "$APP"
SIMCTL_CHILD_LATTICE_DEBUG_SKIP_JOIN=1 xcrun simctl launch A4BA97DA-DB8D-4D46-89E2-E229904CAED9 io.lattice.ios
xcrun simctl io A4BA97DA-DB8D-4D46-89E2-E229904CAED9 screenshot /tmp/lattice-redesign-light.png
```

检查 `/tmp/lattice-redesign-light.png`：hero 卡（未连接/灰/椭圆）、搜索框、设备区空态文案，布局与 spec §三一致、无崩溃。再验证设置页与深色：用 System Events（`tell process "Simulator" to click ...`，同上午冒烟的 AX 驱动方式）切到设置 Tab，确认账户头/偏好/主题 Picker 存在；把主题切到"深色"后再截一张 `dark` 图，确认全屏变深。

- [x] **Step 3: 深浅色核对标准**

light/dark 两图并排对照：hero 卡材质、文字对比度、pill 可读性；任何硬编码白底/黑字即为不合规（回到对应组件改语义色后重跑 Step 1）。

- [x] **Step 4: 回填 checkbox 并提交**

勾掉本计划已完成步骤，未覆盖项（真机连接态：计时/呼吸动画/直连中继聚合）保持未勾并在文件头加一行 Status 注明原因（同上午 iOS 计划的格式）。

```bash
git add docs/superpowers/plans/2026-09-16-ios-tailscale-style-redesign.md
git commit -m "docs(plans): mark iOS redesign plan status"
```

---

## 遗留到真机的事项（不阻塞本计划）

- 连接态三要素的人工验证：连接/断开切换、计时准确性、呼吸动画、直连/经中继聚合（模拟器 NE 限制无法进连接态，同上午冒烟结论）。
- 数据丰满状态（多设备 + 收藏 + 质量标签）的最终视觉确认：真机登录 live latticed 后检查。
