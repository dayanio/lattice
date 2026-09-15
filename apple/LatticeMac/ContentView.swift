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
    @State private var deleteTarget: PeerNode?
    @State private var opError = ""
    @State private var detailPeer: PeerNode?
    @State private var showingNetworkSettings = false
    @State private var showingShare = false
    @State private var searchQuery = ""
    @ObservedObject private var ui = UIState.shared
    @Environment(\.openWindow) private var openAIWindow

    var body: some View {
        VStack(spacing: 0) {
            if let detail = detailPeer {
                PeerDetailView(
                    peer: detail,
                    quality: tunnel.peerStates[detail.name],
                    onBack: { detailPeer = nil },
                    onRename: { name in
                        renameText = peers.first { $0.name == name }?.displayName ?? ""
                        renameTarget = detailPeer
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
            TextField("显示名称", text: $renameText)
            Button("保存") { Task { await renamePeer() } }
            Button("取消", role: .cancel) { renameTarget = nil }
        } message: {
            Text("只改显示名称，不影响节点的网络身份。")
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
            } else if isLoading {
                Spacer()
                ProgressView("加载中…")
                Spacer()
            } else if !errorMsg.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "exclamationmark.triangle")
                        .font(.title2).foregroundColor(.orange)
                    Text(errorMsg).font(.caption).foregroundColor(.secondary)
                    Button("重试") { Task { await loadPeers() } }
                        .buttonStyle(.bordered)
                }
                Spacer()
            } else if peers.isEmpty {
                Spacer()
                VStack(spacing: 8) {
                    Image(systemName: "personalhotspot")
                        .font(.system(size: 32))
                        .foregroundColor(.secondary)
                    Text("没有已连接的节点").foregroundColor(.secondary)
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

    private var deviceList: some View {
        ScrollView {
            VStack(spacing: 0) {
                SectionHead(title: "设备", trailing: "\(filteredPeers.count) 台在线")
                if !inPanel, peers.count >= 4 {
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
            quality: tunnel.peerStates[peer.name],
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
                if let err = tunnel.lastStartError, !err.isEmpty {
                    Text(err).font(.caption2).foregroundColor(.red)
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
        if states.contains("lrp-ready") { return ("经中继", LatticePalette.relay) }
        return nil
    }

    private var filteredPeers: [PeerNode] {
        let q = searchQuery.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return peers }
        return peers.filter {
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
                errorMsg = "登录已过期"
                showingSettings = true
            } else {
                errorMsg = "加载失败: \(description)"
            }
        }
    }

    private func renamePeer() async {
        guard let target = renameTarget else { return }
        renameTarget = nil
        do {
            try await LatticeAPI.shared.renamePeer(target.name, displayName: renameText)
            await loadPeers()
        } catch {
            opError = "重命名失败: \(error.localizedDescription)"
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
    /// ("ice-ready" = direct, "lrp-ready" = relayed). Nil when the local
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
        case "lrp-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
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

/// First-run join sheet: collects the control-plane URL and enrollment token,
/// installs the VPN profile, and connects the tunnel.
struct JoinView: View {
    var onDone: () -> Void

    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://127.0.0.1:8080"
    @State private var token = ""
    @State private var deviceName = Host.current().localizedName ?? "lattice-mac"
    @State private var isSaving = false
    @State private var errorText = ""
    @State private var showingScanner = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("加入 Lattice 网络")
                .font(.system(.headline, design: .rounded))

            LabeledField(label: "服务器地址") {
                TextField("http://127.0.0.1:8080", text: $serverURL)
                    .textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }

            LabeledField(label: "入网令牌") {
                SecureField("控制台签发的入网令牌", text: $token)
                    .textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }

            LabeledField(label: "节点名称") {
                TextField("lattice-mac", text: $deviceName)
                    .textFieldStyle(.plain)
            }

            Text("加入后系统会请求授权创建 VPN 配置，本机即可访问网络内的节点。")
                .font(.caption2)
                .foregroundColor(.secondary)

            if !errorText.isEmpty {
                Text(errorText).font(.caption).foregroundColor(.red)
            }

            HStack(spacing: 8) {
                Button {
                    showingScanner = true
                } label: {
                    Label("扫码入网", systemImage: "qrcode.viewfinder")
                        .font(.caption)
                }
                .buttonStyle(.bordered)

                Button {
                    pastePayload()
                } label: {
                    Label("粘贴", systemImage: "doc.on.clipboard")
                        .font(.caption)
                }
                .buttonStyle(.bordered)

                Spacer()

                if isSaving {
                    ProgressView().controlSize(.small)
                } else {
                    Button("加入网络") { saveAndConnect() }
                        .buttonStyle(.borderedProminent)
                        .disabled(serverURL.isEmpty || token.isEmpty)
                }
            }
        }
        .padding(20)
        .frame(width: 320)
        .sheet(isPresented: $showingScanner) {
            JoinScannerView { payload in
                showingScanner = false
                applyPayload(payload)
            } onCancel: {
                showingScanner = false
            }
        }
    }

    /// Fills server/token from a scanned or pasted lattice://join payload.
    private func applyPayload(_ payload: JoinPayload) {
        if let server = payload.serverURL, !server.isEmpty {
            serverURL = server
        }
        if let t = payload.token, !t.isEmpty {
            token = t
        }
        errorText = ""
    }

    private func pastePayload() {
        guard let raw = NSPasteboard.general.string(forType: .string) else {
            errorText = "剪贴板为空"
            return
        }
        guard let payload = JoinPayload(raw) else {
            errorText = "剪贴板内容不是有效的入网信息"
            return
        }
        applyPayload(payload)
    }

    private func saveAndConnect() {
        isSaving = true
        errorText = ""
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        UserDefaults.standard.set(deviceName, forKey: "lattice.nodeName")
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: token, name: deviceName) { err in
            isSaving = false
            if let err {
                errorText = "保存失败: \(err)"
            } else {
                onDone()
            }
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
