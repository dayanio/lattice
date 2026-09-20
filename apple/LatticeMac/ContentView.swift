// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import SwiftUI
import NetworkExtension

// MARK: - ContentView

struct ContentView: View {
    /// Panel mode (menu-bar popover): read-mostly. Text input and
    /// presentations route to the main window — the popover is not a key
    /// window, so TextFields lose focus and click-outs dismiss it.
    var inPanel: Bool = false
    /// Opens the main window (used only in panel mode).
    var openMain: (() -> Void)? = nil
    /// Opens the AI assistant window (used only in panel mode).
    var openAI: (() -> Void)? = nil

    @State private var peers: [PeerNode] = []
    @State private var isLoading = true
    @State private var errorMsg = ""
    @State private var showingSettings = false
    @State private var showingJoin = false
    @State private var joined = UserDefaults.standard.bool(forKey: "lattice.joined")
    @StateObject private var tunnel = TunnelManager.shared
    @State private var renameTarget: PeerNode?
    @State private var renameText = ""
    @State private var endpointTarget: PeerNode?
    @State private var endpointText = ""
    @State private var deleteTarget: PeerNode?
    @State private var opError = ""
    @State private var detailPeer: PeerNode?
    @State private var showingNetworkSettings = false
    @State private var showingShare = false
    /// The management API is unavailable (not logged in, or the login expired);
    /// the device list still shows what the tunnel knows.
    @State private var needsLogin = false
    @ObservedObject private var loginCoordinator = LoginCoordinator.shared
    @ObservedObject private var auth = AuthSession.shared
    @State private var searchQuery = ""
    @ObservedObject private var ui = UIState.shared
    @Environment(\.openWindow) private var openAIWindow

    var body: some View {
        VStack(spacing: 0) {
            if let detail = detailPeer {
                PeerDetailView(
                    peer: detail,
                    quality: tunnel.peerStates[detail.appID],
                    onBack: { detailPeer = nil },
                    onRename: { name in
                        renameText = peers.first { $0.name == name }?.displayName ?? ""
                        renameTarget = detailPeer
                    },
                    onSetEndpoint: { _ in
                        endpointText = ""
                        endpointTarget = detailPeer
                    },
                    onToggleDisabled: {
                        Task {
                            await toggleDisabled(detail)
                            detailPeer = peers.first { $0.name == detail.name }
                        }
                    },
                    onDelete: { deleteTarget = detail }
                )
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
        .alert("重命名节点", isPresented: Binding(
            get: { renameTarget != nil },
            set: { if !$0 { renameTarget = nil } }
        )) {
            if let target = renameTarget {
                TextField("显示名称", text: $renameText)
                Button("保存") { Task { await renamePeer(target) } }
                Button("取消", role: .cancel) { renameTarget = nil }
            }
        } message: {
            Text("只改显示名称，不影响节点的网络身份。")
        }
        .alert("设置静态地址", isPresented: Binding(
            get: { endpointTarget != nil },
            set: { if !$0 { endpointTarget = nil } }
        )) {
            if let target = endpointTarget {
                TextField("IP:端口，如 203.0.113.5:51820", text: $endpointText)
                Button("保存") { Task { await setEndpoint(target) } }
                Button("取消", role: .cancel) { endpointTarget = nil }
            }
        } message: {
            Text("手动指定该节点的真实可达地址，跳过自动打洞。留空清除，恢复自动探测。")
        }
        .confirmationDialog(
            "删除节点 \(deleteTarget?.shownName ?? "")？",
            isPresented: Binding(
                get: { deleteTarget != nil },
                set: { if !$0 { deleteTarget = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("删除", role: .destructive) { Task { await deletePeer() } }
            Button("取消", role: .cancel) { deleteTarget = nil }
        } message: {
            Text("该节点将被移出网络，需重新入网才能恢复。")
        }
    }

    /// Consumes pending cross-window requests (from the menu-bar panel).
    /// Called from onAppear/onChange only — never mid-view-update.
    private func syncUIStateRequests() {
        guard !inPanel else { return }
        if ui.showJoin {
            ui.showJoin = false
            showingJoin = true
        }
        if ui.showSettings {
            ui.showSettings = false
            showingSettings = true
        }
        if let name = ui.detailPeerName {
            ui.detailPeerName = nil
            if let target = peers.first(where: { $0.name == name }) {
                detailPeer = target
            } else {
                // Peers not loaded yet in a freshly opened window: load, then show.
                Task {
                    await loadPeers()
                    detailPeer = peers.first { $0.name == name }
                }
            }
        }
        if let page = ui.page {
            ui.page = nil
            switch page {
            case .networkSettings: showingNetworkSettings = true
            case .share: showingShare = true
            }
        }
    }

    private var mainPanel: some View {
        VStack(spacing: 0) {
            header
            Divider()

            if !joined {
                Spacer()
                VStack(spacing: 10) {
                    Image(systemName: "personalhotspot")
                        .font(.system(size: 32))
                        .foregroundColor(.secondary)
                    Text("尚未加入 Lattice 网络")
                        .foregroundColor(.secondary)
                    Button("加入网络") { showingJoin = true }
                        .buttonStyle(.borderedProminent)
                }
                Spacer()
            } else if isLoading && displayPeers.isEmpty {
                Spacer()
                ProgressView("加载中…")
                Spacer()
            } else if !errorMsg.isEmpty && displayPeers.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.title2).foregroundColor(.orange)
                    Text(errorMsg).font(.caption).foregroundColor(.secondary)
                    Button("重试") { Task { await loadPeers() } }
                        .buttonStyle(.bordered)
                }
                Spacer()
            } else if displayPeers.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "personalhotspot")
                        .font(.system(size: 32))
                        .foregroundColor(.secondary)
                    Text("没有已连接的节点").foregroundColor(.secondary)
                    if needsLogin {
                        Button("登录以查看和管理设备") { requestManageLogin() }
                            .buttonStyle(.bordered)
                    }
                }
                Spacer()
            } else {
                deviceList
            }

            bottomNav
        }
        .task {
            tunnel.load()
            await loadPeers()
        }
        .onChange(of: auth.isLoggedIn) { _ in
            Task { await loadPeers() }
        }
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
            SettingsView {
                showingSettings = false
                Task { await loadPeers() }
            } onJoin: {
                showingSettings = false
                showingJoin = true
            }
        }
        .onAppear { syncUIStateRequests() }
        // onChange fires after the update — safe to mutate state here
        // (onReceive could land mid-update and crash SwiftUI).
        .onChange(of: ui.showJoin) { _ in syncUIStateRequests() }
        .onChange(of: ui.showSettings) { _ in syncUIStateRequests() }
        .onChange(of: ui.detailPeerName) { _ in syncUIStateRequests() }
        .onChange(of: ui.page) { _ in syncUIStateRequests() }
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
    }

    /// Shown when the management API is unavailable: the list above still works,
    /// only rename / disable / delete and the extra details need a login.
    private var loginHint: some View {
        Button { requestManageLogin() } label: {
            HStack(spacing: 8) {
                Image(systemName: "person.crop.circle.badge.exclamationmark")
                    .foregroundColor(.accentColor)
                VStack(alignment: .leading, spacing: 1) {
                    Text("登录后可管理设备").font(.caption.weight(.medium))
                    Text("列表与连接不受影响").font(.caption2).foregroundColor(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right").font(.caption2).foregroundColor(.secondary)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func requestManageLogin() {
        Task { _ = await LoginCoordinator.shared.requestLogin() }
    }

    private var deviceList: some View {
        ScrollView {
            VStack(spacing: 0) {
                if needsLogin {
                    loginHint
                }
                SectionHead(title: "设备", trailing: "\(filteredPeers.count) 台在线")
                if !inPanel, displayPeers.count >= 4 {
                    PanelSearchField(text: $searchQuery)
                }
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
                ForEach(filteredPeers) { peer in
                    peerRow(peer)
                    Divider().padding(.leading, 44)
                }
            }
        }
    }

    private func peerRow(_ peer: PeerNode) -> some View {
        PeerRow(
            peer: peer,
            quality: tunnel.peerStates[peer.appID],
            onRename: { name in
                if inPanel {
                    UIState.shared.detailPeerName = peer.name
                    openMain?()
                } else {
                    renameText = peers.first { $0.name == name }?.displayName ?? ""
                    renameTarget = peer
                }
            },
            onToggleDisabled: { Task { await toggleDisabled(peer) } },
            onDelete: {
                if inPanel {
                    UIState.shared.detailPeerName = peer.name
                    openMain?()
                } else {
                    deleteTarget = peer
                }
            },
            onOpenDetail: {
                if inPanel {
                    UIState.shared.detailPeerName = peer.name
                    openMain?()
                } else {
                    detailPeer = peer
                }
            }
        )
    }

    /// Bottom quick-nav stack: AI assistant, sharing, network settings, footer.
    private var bottomNav: some View {
        VStack(spacing: 0) {
            Divider()
            NavRow(
                icon: "sparkles",
                iconColor: Color(red: 0.49, green: 0.48, blue: 1.0),
                title: "AI 助手",
                showsChevron: true
            ) {
                openAIWindow(id: "ai")
                NSApp.activate(ignoringOtherApps: true)
            }
            Divider()
            NavRow(
                icon: "arrow.up.forward",
                iconColor: Color(red: 0.49, green: 0.48, blue: 1.0),
                title: "共享本地服务",
                showsChevron: true
            ) {
                if inPanel {
                    UIState.shared.page = .share
                    openMain?()
                } else {
                    showingShare = true
                }
            }
            Divider()
            NavRow(
                icon: "gearshape",
                iconColor: .accentColor,
                title: "网络设置",
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
            footer
        }
    }

    private var connected: Binding<Bool> {
        Binding(
            get: { tunnel.status == .connected },
            set: { on in
                if on {
                    if tunnel.isConfigured {
                        tunnel.connect()
                    } else {
                        showingJoin = true
                    }
                } else {
                    tunnel.disconnect()
                }
            }
        )
    }

    private var header: some View {
        HStack(spacing: 9) {
            HaloDot(color: statusColor, size: 9)
            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 6) {
                    Text(statusText)
                        .font(.system(size: 13.5, weight: .semibold, design: .rounded))
                    if let summary = aggregateQuality {
                        QualityPill(text: summary.text, color: summary.color)
                    }
                }
                if let failure = tunnel.lastFailure {
                    Text(failure.title).font(.caption2.weight(.semibold))
                        .foregroundColor(failure.isNotice ? .orange : .red)
                    if !failure.advice.isEmpty {
                        Text(failure.advice)
                            .font(.caption2)
                            .foregroundColor(.secondary)
                            .lineLimit(3)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                } else if let host = URL(string: tunnel.serverURL ?? ""), let hostHeader = host.host {
                    Text(hostHeader)
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
            }
            Spacer()
            Toggle("", isOn: connected)
                .toggleStyle(.switch)
                .controlSize(.small)
                .labelsHidden()
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 12)
    }

    /// Aggregate quality for the header pill: direct wins over relay.
    private var aggregateQuality: (text: String, color: Color)? {
        guard tunnel.status == .connected else { return nil }
        let states = Set(tunnel.peerStates.values)
        if states.contains("ice-ready") { return ("直连", LatticePalette.online) }
        if states.contains("relay-ready") { return ("经中继", LatticePalette.relay) }
        return nil
    }

    /// Management-API peers merged with the tunnel's own list, so the list works
    /// without a login.
    private var displayPeers: [PeerNode] {
        PeerListMerge.merged(api: peers, tunnel: tunnel.tunnelPeers)
    }

    private var filteredPeers: [PeerNode] {
        let q = searchQuery.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return displayPeers }
        return displayPeers.filter {
            $0.shownName.localizedCaseInsensitiveContains(q)
                || $0.name.localizedCaseInsensitiveContains(q)
                || $0.address.contains(q)
        }
    }

    private var statusColor: Color {
        switch tunnel.status {
        case .connected: return LatticePalette.online
        case .connecting, .reasserting, .disconnecting: return LatticePalette.relay
        default: return .secondary
        }
    }

    private var statusText: String {
        switch tunnel.status {
        case .connected: return "已连接"
        case .connecting, .reasserting: return "连接中…"
        case .disconnecting: return "断开中…"
        default: return "未连接"
        }
    }

    private var footer: some View {
        HStack {
            if !opError.isEmpty {
                Text(opError)
                    .font(.caption2)
                    .foregroundColor(.red)
                    .lineLimit(1)
                    .help(opError)
                    .onTapGesture { opError = "" }
            } else {
                Text("Lattice standalone").font(.caption2).foregroundColor(.secondary)
            }
            Spacer()
            Button {
                if inPanel {
                    UIState.shared.showJoin = true
                    openMain?()
                } else {
                    showingJoin = true
                }
            } label: {
                Image(systemName: "plus.circle")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("加入网络 / 重新入网")

            Button {
                if inPanel {
                    UIState.shared.showSettings = true
                    openMain?()
                } else {
                    showingSettings = true
                }
            } label: {
                Image(systemName: "gearshape")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("登录管理面板")

            Button("刷新") { Task { await loadPeers() } }
                .font(.caption)
                .buttonStyle(.plain)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
    }

    private func loadPeers() async {
        isLoading = true
        errorMsg = ""
        defer { isLoading = false }
        // Not logged in: skip the management API; the list comes from the tunnel.
        guard LatticeAPI.shared.isLoggedIn else {
            needsLogin = true
            peers = []
            return
        }
        needsLogin = false
        do {
            var loaded = try await LatticeAPI.shared.listPeers()
            // Badge AI agents: an AgentIdentity referencing the peer makes it
            // an agent node, not a human device (UI mockup §04).
            if let identities = try? await LatticeAPI.shared.listAgentIdentities() {
                for i in identities.indices {
                    let identity = identities[i]
                    let ref = identity.peerRef ?? identity.name ?? ""
                    guard !ref.isEmpty else { continue }
                    if let idx = loaded.firstIndex(where: { $0.name == ref || $0.appID == ref }) {
                        loaded[idx].isAgent = true
                        loaded[idx].sandbox = identity.sandbox
                    }
                }
            }
            peers = loaded
        } catch {
            let description = error.localizedDescription
            if description.contains("Invalid token") || description.contains("log in first")
                || description.contains("token has been revoked") {
                needsLogin = true
                if tunnel.tunnelPeers.isEmpty {
                    errorMsg = "登录已过期"
                    showingSettings = true
                }
            } else {
                errorMsg = "加载失败: \(description)"
            }
        }
    }

    private func renamePeer(_ target: PeerNode) async {
        do {
            try await LatticeAPI.shared.renamePeer(target.name, displayName: renameText)
            await loadPeers()
        } catch {
            opError = "重命名失败: \(error.localizedDescription)"
        }
    }

    private func setEndpoint(_ target: PeerNode) async {
        do {
            try await LatticeAPI.shared.setPeerEndpoint(target.name, endpoint: endpointText)
            await loadPeers()
        } catch {
            opError = "设置静态地址失败: \(error.localizedDescription)"
        }
    }

    private func toggleDisabled(_ peer: PeerNode) async {
        do {
            try await LatticeAPI.shared.setPeerDisabled(peer.name, !peer.disabled)
            await loadPeers()
        } catch {
            opError = "操作失败: \(error.localizedDescription)"
        }
    }

    private func deletePeer() async {
        guard let target = deleteTarget else { return }
        deleteTarget = nil
        do {
            try await LatticeAPI.shared.deletePeer(target.name)
            await loadPeers()
        } catch {
            opError = "删除失败: \(error.localizedDescription)"
        }
    }
}

// MARK: - Peer Row

struct PeerRow: View {
    let peer: PeerNode
    /// Connection quality from this machine's tunnel engine
    /// ("ice-ready" = direct, "relay-ready" = relayed). Nil when the local
    /// tunnel is down or this peer isn't in the engine's netmap.
    var quality: String? = nil
    var onRename: ((String) -> Void)? = nil
    var onToggleDisabled: (() -> Void)? = nil
    var onDelete: (() -> Void)? = nil
    var onOpenDetail: (() -> Void)? = nil
    @State private var copied = false
    @State private var hovered = false

    private var qualityLabel: (text: String, color: Color)? {
        switch quality {
        case "ice-ready": return ("直连", .green)
        case "relay-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
        case "closed": return ("不可达", .secondary)
        default: return nil
        }
    }

    /// True for this machine's own peer (matched by join name or hostname).
    private var isSelf: Bool {
        let joined = UserDefaults.standard.string(forKey: "lattice.nodeName") ?? ""
        let host = Host.current().localizedName ?? ""
        return !joined.isEmpty && joined == peer.name
            || !host.isEmpty && host == peer.name
    }

    var body: some View {
        HStack(spacing: 10) {
            HaloDot(color: peer.disabled ? .orange : (peer.online ? .green : Color.secondary.opacity(0.6)))

            VStack(alignment: .leading, spacing: 1) {
                HStack(spacing: 5) {
                    Text(peer.shownName).font(.system(size: 13))
                    if isSelf {
                        Text("本机")
                            .font(.system(size: 10, weight: .bold))
                            .foregroundColor(.accentColor)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Color.accentColor.opacity(0.12))
                            .cornerRadius(4)
                    }
                    if peer.isAgent {
                        Text("AI")
                            .font(.system(size: 10, weight: .heavy))
                            .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Color(red: 0.49, green: 0.48, blue: 1.0).opacity(0.16))
                            .cornerRadius(4)
                    }
                    if peer.disabled {
                        Text("已下线")
                            .font(.system(size: 10, weight: .bold))
                            .foregroundColor(.orange)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Color.orange.opacity(0.14))
                            .cornerRadius(4)
                    }
                }
                Text(peer.address)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundColor(.secondary)
            }

            Spacer()

            if let q = qualityLabel {
                QualityPill(text: q.text, color: q.color)
            }

            Button {
                copyAddress()
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .font(.caption2)
                    .foregroundColor(copied ? .green : .secondary)
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 7)
        .rowHover(hovered)
        .opacity(peer.disabled ? 0.55 : 1)
        .contentShape(Rectangle())
        .onHover { hovered = $0 }
        .onTapGesture {
            onOpenDetail?()
        }
        .contextMenu {
            Button("查看 ACL") { onOpenDetail?() }
            Button("复制 IP 地址") { copyAddress() }
            if let onRename {
                Button("重命名…") { onRename(peer.name) }
            }
            if let onToggleDisabled {
                Button(peer.disabled ? "上线" : "下线") { onToggleDisabled() }
            }
            Divider()
            if let onDelete {
                Button("删除节点", role: .destructive) { onDelete() }
            }
        }
    }

    private func copyAddress() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(peer.address, forType: .string)
        copied = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
    }
}

// MARK: - Join (network enrollment)

/// First-run join sheet. One input takes an invite link
/// (lattice://join?server=…&token=…[&name=…]) or a bare enrollment token; the
/// server address and device name live under "高级" and are only asked for when
/// the input does not carry them. Saving installs the VPN profile; the caller
/// connects the tunnel.
struct JoinView: View {
    var onDone: () -> Void

    @State private var input = ""
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var deviceName = Host.current().localizedName ?? "lattice-mac"
    @State private var showAdvanced = false
    @State private var fromClipboard = false
    /// "Log in and join": an account issues this device's token, instead of an
    /// invite link or token being pasted.
    @State private var accountMode = false
    @State private var username = UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin"
    @State private var password = ""
    @State private var isSaving = false
    @State private var failure: JoinFailure?
    @State private var showingScanner = false

    private var payload: JoinPayload? { JoinPayload(input) }
    private var token: String { payload?.token ?? "" }
    private var effectiveServer: String {
        (payload?.serverURL ?? serverURL).trimmingCharacters(in: .whitespacesAndNewlines)
    }
    private var effectiveName: String { payload?.name ?? deviceName }
    private var canJoin: Bool {
        if accountMode {
            return !serverURL.trimmingCharacters(in: .whitespaces).isEmpty && !username.isEmpty && !password.isEmpty && !isSaving
        }
        return !token.isEmpty && !effectiveServer.isEmpty && !isSaving
    }
    private var needsServer: Bool { payload != nil && payload?.serverURL == nil && serverURL.isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("加入 Lattice 网络")
                .font(.system(.headline, design: .rounded))

            Picker("", selection: $accountMode) {
                Text("邀请链接 / 令牌").tag(false)
                Text("账号登录").tag(true)
            }
            .pickerStyle(.segmented)
            .labelsHidden()

            if accountMode {
                LabeledField(label: "服务器地址") {
                    TextField("http://服务器地址:18090", text: $serverURL)
                        .textFieldStyle(.plain)
                        .font(.system(.caption, design: .monospaced))
                }
                LabeledField(label: "用户名") {
                    TextField("admin", text: $username)
                        .textFieldStyle(.plain)
                }
                LabeledField(label: "密码") {
                    SecureField("••••••••", text: $password)
                        .textFieldStyle(.plain)
                }
                Text("用账号为这台设备签发入网令牌，登录状态会保留，之后的管理操作不必再登录。")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                LabeledField(label: "邀请链接或入网令牌") {
                    TextField("粘贴 lattice://join?… 链接，或入网令牌", text: $input)
                        .textFieldStyle(.plain)
                        .font(.system(.caption, design: .monospaced))
                }
            }

            if accountMode {
                EmptyView()
            } else if fromClipboard {
                Text("已从剪贴板读取邀请信息")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            } else if let payload, payload.serverURL != nil {
                Text("服务器：\(payload.serverURL ?? "")")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }

            if needsServer && !accountMode {
                Text("这个令牌不含服务器地址，请在下面填写。")
                    .font(.caption2)
                    .foregroundColor(.orange)
            }

            DisclosureGroup(accountMode ? "高级（设备名）" : "高级（服务器地址、设备名）", isExpanded: $showAdvanced) {
                VStack(alignment: .leading, spacing: 10) {
                    if !accountMode {
                        LabeledField(label: "服务器地址") {
                            TextField("http://服务器地址:18090", text: $serverURL)
                                .textFieldStyle(.plain)
                                .font(.system(.caption, design: .monospaced))
                        }
                    }
                    LabeledField(label: "设备名") {
                        TextField("lattice-mac", text: $deviceName)
                            .textFieldStyle(.plain)
                    }
                    if let stored = DeviceName.preview(effectiveName) {
                        Text("将保存为 \(stored)")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                }
                .padding(.top, 6)
            }
            .font(.caption)

            Text("加入后系统会请求授权创建 VPN 配置，请在弹窗里点“允许”。")
                .font(.caption2)
                .foregroundColor(.secondary)

            if let failure {
                VStack(alignment: .leading, spacing: 2) {
                    Text(failure.title).font(.caption.weight(.semibold)).foregroundColor(.red)
                    if !failure.advice.isEmpty {
                        Text(failure.advice).font(.caption2).foregroundColor(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }

            HStack(spacing: 8) {
                if !accountMode {
                    Button {
                        showingScanner = true
                    } label: {
                        Label("扫码入网", systemImage: "qrcode.viewfinder")
                            .font(.caption)
                    }
                    .buttonStyle(.bordered)

                    Button {
                        pasteFromClipboard()
                    } label: {
                        Label("粘贴", systemImage: "doc.on.clipboard")
                            .font(.caption)
                    }
                    .buttonStyle(.bordered)
                }

                Spacer()

                if isSaving {
                    ProgressView().controlSize(.small)
                    Text("正在保存配置…").font(.caption2).foregroundColor(.secondary)
                } else {
                    Button(accountMode ? "登录并加入" : "加入网络") { join() }
                        .buttonStyle(.borderedProminent)
                        .disabled(!canJoin)
                }
            }
        }
        .padding(20)
        .frame(width: 340)
        .onAppear { detectClipboardInvite() }
        .onChange(of: needsServer) { needed in
            if needed { showAdvanced = true }
        }
        .sheet(isPresented: $showingScanner) {
            JoinScannerView { payload in
                showingScanner = false
                applyPayload(payload, raw: nil)
            } onCancel: {
                showingScanner = false
            }
        }
    }

    /// Only a complete invite link is picked up from the clipboard on its own; a
    /// bare word (any copied password, say) would otherwise land in this field.
    private func detectClipboardInvite() {
        guard input.isEmpty,
              let raw = NSPasteboard.general.string(forType: .string),
              raw.lowercased().hasPrefix("lattice://join"),
              let payload = JoinPayload(raw), payload.token != nil else { return }
        applyPayload(payload, raw: raw)
        fromClipboard = true
    }

    private func applyPayload(_ payload: JoinPayload, raw: String?) {
        if let raw {
            input = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        } else if let t = payload.token {
            var link = URLComponents()
            link.scheme = "lattice"
            link.host = "join"
            var items = [URLQueryItem(name: "token", value: t)]
            if let server = payload.serverURL { items.append(URLQueryItem(name: "server", value: server)) }
            if let name = payload.name { items.append(URLQueryItem(name: "name", value: name)) }
            link.queryItems = items
            input = link.string ?? t
        }
        fromClipboard = false
        failure = nil
    }

    private func pasteFromClipboard() {
        guard let raw = NSPasteboard.general.string(forType: .string) else {
            failure = JoinFailure(title: "剪贴板是空的", advice: "先复制邀请链接或入网令牌。")
            return
        }
        guard let payload = JoinPayload(raw) else {
            failure = JoinFailure(title: "剪贴板里不是有效的入网信息", advice: "需要 lattice://join?… 链接，或不含空格的入网令牌。")
            return
        }
        applyPayload(payload, raw: raw)
    }

    private func join() {
        if accountMode {
            joinWithAccount()
        } else {
            saveAndConnect(server: effectiveServer, token: token, name: effectiveName)
        }
    }

    /// Logs in, has the account issue this device's token, then joins with it.
    private func joinWithAccount() {
        isSaving = true
        failure = nil
        let server = serverURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmed = server.hasSuffix("/") ? String(server.dropLast()) : server
        Task {
            do {
                let issued = try await LatticeAPI.shared.loginAndCreateDeviceToken(server: trimmed, user: username, pass: password)
                password = ""
                saveAndConnect(server: trimmed, token: issued, name: deviceName)
            } catch let error as AccountJoinError {
                isSaving = false
                failure = error.failure
            } catch {
                isSaving = false
                failure = .tokenNotIssued(error.localizedDescription)
            }
        }
    }

    private func saveAndConnect(server rawServer: String, token: String, name: String) {
        isSaving = true
        failure = nil
        let server = rawServer.hasSuffix("/") ? String(rawServer.dropLast()) : rawServer
        UserDefaults.standard.set(server, forKey: "lattice.serverURL")
        // Peers appear under the server's normalized name; keep the same form so
        // "this device" is recognised in the list.
        UserDefaults.standard.set(DeviceName.normalized(name), forKey: "lattice.nodeName")
        TunnelManager.shared.saveJoin(serverURL: server, token: token, name: name) { err in
            isSaving = false
            if let err {
                failure = JoinFailure(title: "保存 VPN 配置失败", advice: "\(err)。请在系统弹窗里点“允许”后重试。")
            } else {
                onDone()
            }
        }
    }
}

// MARK: - Manage login (on demand)

/// Compact login for a management action that needs one. The server is the one
/// the device joined; only the account is asked for.
struct ManageLoginView: View {
    var onFinished: (Bool) -> Void

    @State private var username = UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""

    private var serverURL: String { UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "" }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("登录以管理设备")
                .font(.system(.headline, design: .rounded))
            Text("改名、下线、删除等管理操作需要账号；设备列表和连接不受影响。")
                .font(.caption2)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if serverURL.isEmpty {
                Text("尚未加入网络，请先加入。")
                    .font(.caption)
                    .foregroundColor(.orange)
            } else {
                Text(serverURL)
                    .font(.system(.caption2, design: .monospaced))
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }

            LabeledField(label: "用户名") {
                TextField("admin", text: $username)
                    .textFieldStyle(.plain)
            }
            LabeledField(label: "密码") {
                SecureField("••••••••", text: $password)
                    .textFieldStyle(.plain)
            }

            if !loginError.isEmpty {
                Text(loginError).font(.caption).foregroundColor(.red)
            }

            HStack {
                Button("取消") { onFinished(false) }
                    .buttonStyle(.bordered)
                Spacer()
                if isLoggingIn {
                    ProgressView().controlSize(.small)
                } else {
                    Button("登录") { Task { await login() } }
                        .buttonStyle(.borderedProminent)
                        .keyboardShortcut(.defaultAction)
                        .disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)
                }
            }
        }
        .padding(20)
        .frame(width: 300)
    }

    private func login() async {
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            onFinished(true)
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}

// MARK: - Settings (management-plane login)

struct SettingsView: View {
    var onDone: () -> Void
    var onJoin: (() -> Void)? = nil

    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://127.0.0.1:8080"
    @State private var username = UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("连接到 Lattice")
                .font(.system(.headline, design: .rounded))

            LabeledField(label: "服务器地址") {
                TextField("http://127.0.0.1:8080", text: $serverURL)
                    .textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }

            LabeledField(label: "用户名") {
                TextField("admin", text: $username)
                    .textFieldStyle(.plain)
            }

            LabeledField(label: "密码") {
                SecureField("••••••••", text: $password)
                    .textFieldStyle(.plain)
            }

            if !loginError.isEmpty {
                Text(loginError)
                    .font(.caption)
                    .foregroundColor(.red)
            }

            HStack {
                Spacer()
                if isLoggingIn {
                    ProgressView().controlSize(.small)
                } else {
                    Button("登录") { Task { await login() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)
                }
            }

            Button {
                onJoin?()
            } label: {
                Label("加入新网络 / 重新入网", systemImage: "plus.circle")
                    .font(.caption)
            }
            .buttonStyle(.bordered)

            Divider().padding(.vertical, 2)

            // SSO 按既有决定暂缓（Phase 5）：入口保留但明确标注，不假装可用。
            HStack(spacing: 7) {
                Text("使用单点登录（SSO）")
                    .font(.caption)
                    .foregroundColor(.secondary)
                SoonBadge()
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 7)
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .strokeBorder(Color.secondary.opacity(0.3))
            )
        }
        .padding(20)
        .frame(width: 300)
    }

    private func login() async {
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            onDone()
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}

// MARK: - Join QR scanner sheet

/// Camera sheet: scans a lattice://join QR code. Also shows the expected
/// payload format so a person can type it from another screen if no camera
/// is available.
struct JoinScannerView: View {
    var onCode: (JoinPayload) -> Void
    var onCancel: () -> Void

    @State private var errorText = ""

    var body: some View {
        VStack(spacing: 12) {
            Text("扫描入网二维码")
                .font(.system(.headline, design: .rounded))
                .padding(.top, 14)

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

            Text("二维码内容格式：lattice://join?server=…&token=…")
                .font(.caption2)
                .foregroundColor(.secondary)

            if !errorText.isEmpty {
                Text(errorText)
                    .font(.caption)
                    .foregroundColor(.red)
                    .padding(.horizontal, 14)
            }

            Button("取消") { onCancel() }
                .buttonStyle(.bordered)
                .padding(.bottom, 14)
        }
        .frame(width: 320)
    }
}
