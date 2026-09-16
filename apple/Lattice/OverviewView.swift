// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 首页 = 连接页，永远是启动后的落地页。三态：
/// 未加入网络 → 引导卡；已加入未登录 → 登录提示；已加入已登录 → 设备列表。
struct OverviewView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @ObservedObject private var favorites = FavoritesStore.shared
    @State private var peers: [PeerNode] = []
    @State private var searchText = ""
    @State private var isLoading = false
    @State private var errorMsg = ""
    @State private var renamingPeer: PeerNode?
    @State private var renameText = ""
    @State private var disablingPeer: PeerNode?
    @State private var showingJoin = false
    @State private var showingLogin = false
    @AppStorage("lattice.authToken") private var authToken = ""
    @Environment(\.scenePhase) private var scenePhase

    private var selfName: String { UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "" }
    private var localPeer: PeerNode? { peers.first { $0.name == selfName } }
    /// 服务器地址为空 = 未加入（退出网络会清掉它），此时只应引导加入。
    private var joined: Bool { tunnel.isConfigured && !serverAddress.isEmpty }

    private var serverAddress: String { UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "" }

    private var aggregateText: String {
        let states = peers.compactMap { tunnel.peerStates[$0.appID] }
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

                    if !joined {
                        joinPrompt
                    } else if authToken.isEmpty {
                        loginPrompt
                    } else {
                        PanelSearchField(text: $searchText)

                        if isLoading && peers.isEmpty {
                            ProgressView().padding(.top, 30)
                        } else if !errorMsg.isEmpty {
                            Text(errorMsg)
                                .font(.caption)
                                .foregroundColor(LatticePalette.blocked)
                                .padding(.top, 30)
                        } else if filtered.isEmpty {
                            VStack(spacing: 8) {
                                Image(systemName: "personalhotspot")
                                    .font(.system(size: 34, weight: .light))
                                    .foregroundColor(.secondary.opacity(0.6))
                                Text(searchText.isEmpty ? "暂无节点" : "无匹配设备")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                            }
                            .padding(.top, 30)
                        } else {
                            deviceGroup(title: "⭐ 收藏", peers: favoritePeers)
                            deviceGroup(title: "全部设备", peers: otherPeers)
                        }
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
            .sheet(isPresented: $showingJoin) {
                JoinView(onFinished: {
                    showingJoin = false
                    Task { await loadPeers() }
                })
            }
            .sheet(isPresented: $showingLogin) {
                LoginView(onFinished: { Task { await loadPeers() } })
            }
            .alert("重命名设备", isPresented: .init(
                get: { renamingPeer != nil },
                set: { if !$0 { renamingPeer = nil } }
            )) {
                if let peer = renamingPeer {
                    TextField("新名称", text: $renameText)
                    Button("确定") {
                        Task {
                            try? await PeerActions.rename(peer, to: renameText)
                            await loadPeers()
                        }
                    }
                    Button("取消", role: .cancel) {}
                }
            }
            .confirmationDialog(
                "停用 \"\(disablingPeer?.shownName ?? "")\"？",
                isPresented: .init(
                    get: { disablingPeer != nil },
                    set: { if !$0 { disablingPeer = nil } }
                ),
                titleVisibility: .visible
            ) {
                if let peer = disablingPeer {
                    Button("停用", role: .destructive) {
                        Task {
                            try? await PeerActions.setDisabled(peer, true)
                            await loadPeers()
                        }
                    }
                    Button("取消", role: .cancel) {}
                }
            }
        }
    }

    /// 未加入网络时的引导卡：扫码或手动输入，通往加入流程。
    private var joinPrompt: some View {
        VStack(spacing: 12) {
            Image(systemName: "qrcode.viewfinder")
                .font(.system(size: 42, weight: .light))
                .foregroundColor(LatticePalette.accent)
            VStack(spacing: 4) {
                Text("尚未加入网络")
                    .font(.system(.headline, weight: .semibold))
                Text("扫码或手动输入服务器信息，一键连回家。")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
            }
            VStack(spacing: 8) {
                Button { showingJoin = true } label: {
                    Label("扫描二维码", systemImage: "qrcode.viewfinder")
                        .font(.system(size: 14, weight: .semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 6)
                }
                .buttonStyle(.borderedProminent)
                Button { showingJoin = true } label: {
                    Label("手动输入", systemImage: "keyboard")
                        .font(.system(size: 14, weight: .semibold))
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 6)
                }
                .buttonStyle(.bordered)
            }
        }
        .padding(18)
        .background(
            RoundedRectangle(cornerRadius: 20, style: .continuous)
                .fill(.regularMaterial)
                .shadow(color: .black.opacity(0.06), radius: 12, y: 4)
        )
        .padding(.horizontal, 15)
        .padding(.top, 6)
    }

    /// 已加入但未登录管理后台：隧道可用，仅设备列表/管理功能需要登录。
    private var loginPrompt: some View {
        Button { showingLogin = true } label: {
            HStack(spacing: 10) {
                Image(systemName: "person.crop.circle.badge.exclamationmark")
                    .font(.system(size: 22))
                    .foregroundColor(LatticePalette.accent)
                VStack(alignment: .leading, spacing: 2) {
                    Text("登录后可查看与管理设备")
                        .font(.system(.body, weight: .medium))
                    Text("隧道连接不受影响，点击登录管理后台")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .padding(14)
            .background(RoundedRectangle(cornerRadius: 12).fill(Color.primary.opacity(0.05)))
            .padding(.horizontal, 15)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .padding(.top, 6)
    }

    /// 圆角容器卡：一组设备行共用一张卡，与 hero 卡同一设计语言。
    private func deviceGroup(title: String, peers: [PeerNode]) -> some View {
        VStack(spacing: 0) {
            SectionHead(title: title)
                .padding(.horizontal, 4)
            ForEach(peers) { peerRow($0) }
        }
        .padding(.vertical, 6)
        .background(
            RoundedRectangle(cornerRadius: 16, style: .continuous)
                .fill(Color.primary.opacity(0.04))
        )
        .padding(.horizontal, 15)
    }

    private func peerRow(_ peer: PeerNode) -> some View {
        NavigationLink {
            PeerDetailView(peer: peer, quality: tunnel.peerStates[peer.appID])
        } label: {
            HStack(spacing: 12) {
                Group {
                    if peer.os.isEmpty {
                        MonogramAvatar(name: peer.shownName)
                    } else {
                        PlatformIcon(os: peer.os)
                    }
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text(peer.shownName).font(.system(.body, weight: .medium))
                    HStack(spacing: 6) {
                        Text(peer.address)
                            .font(.system(.caption, design: .monospaced))
                            .foregroundColor(.secondary)
                        if let quality = tunnel.peerStates[peer.appID],
                           let pill = PeerActions.qualityPill(quality) {
                            QualityPill(text: pill.text, color: pill.color)
                        }
                    }
                }
                Spacer()
                if peer.name == selfName {
                    TagBadge(text: "本机", color: LatticePalette.ai)
                }
                HaloDot(color: peer.disabled ? .secondary : (peer.online ? LatticePalette.online : .secondary), size: 8)
                FavoriteStar(isOn: favorites.isFavorite(peer.name)) {
                    favorites.toggle(peer.name)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 9)
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
