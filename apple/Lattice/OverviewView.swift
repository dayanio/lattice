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
