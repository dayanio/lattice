// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 只读 peer 详情 + 复制/收藏/重命名/停用动作（spec §四）。
struct PeerDetailView: View {
    let peer: PeerNode
    let quality: String?

    @ObservedObject private var favorites = FavoritesStore.shared
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
                        do {
                            try await PeerActions.setDisabled(peer, !currentDisabled)
                            currentDisabled.toggle()
                        } catch {
                            // 服务器未变更时保持原状态；与全 App 静默错误约定一致，不弹窗
                        }
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
