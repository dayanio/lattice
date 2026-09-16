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

/// Home tab: connection toggle, own overlay IP, and the peer list with live
/// connection quality. Peer list requires an admin-auth session
/// (LatticeAPI.shared.isLoggedIn) — the join flow (JoinView) establishes
/// this before RootView ever shows this tab, so `loadPeers()` here assumes
/// it's already true and just surfaces the error if the session expired.
struct StatusView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var peers: [PeerNode] = []
    @State private var isLoading = false
    @State private var errorMsg = ""
    @Environment(\.scenePhase) private var scenePhase

    private var selfName: String { UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "" }

    private var localPeer: PeerNode? {
        peers.first { $0.name == selfName }
    }

    var body: some View {
        NavigationStack {
            List {
                Section("连接") {
                    HStack {
                        Text(tunnel.statusText)
                            .foregroundColor(tunnel.status == .connected ? .green : .primary)
                        Spacer()
                        Toggle("", isOn: tunnel.connectedBinding)
                            .labelsHidden()
                    }
                    if !tunnel.lastStartError.isEmpty {
                        Text(tunnel.lastStartError)
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                    if let local = localPeer {
                        LabeledContent("本机地址", value: local.address)
                            .font(.system(.body, design: .monospaced))
                    }
                }

                Section("节点") {
                    if isLoading && peers.isEmpty {
                        HStack {
                            Spacer()
                            ProgressView()
                            Spacer()
                        }
                    } else if !errorMsg.isEmpty {
                        Text(errorMsg).font(.caption).foregroundColor(.red)
                    } else if peers.isEmpty {
                        Text("暂无节点").font(.caption).foregroundColor(.secondary)
                    } else {
                        ForEach(peers) { peer in
                            NavigationLink {
                                PeerDetailView(peer: peer, quality: tunnel.peerStates[peer.name])
                            } label: {
                                peerRow(peer)
                            }
                        }
                    }
                }
            }
            .navigationTitle("Lattice")
            .refreshable { await loadPeers() }
            .task { await loadPeers() }
            .onChange(of: scenePhase) { _, newPhase in
                if newPhase == .active {
                    Task { await loadPeers() }
                }
            }
        }
    }

    private func peerRow(_ peer: PeerNode) -> some View {
        HStack(spacing: 10) {
            HaloDot(color: peer.disabled ? .secondary : .green)
            VStack(alignment: .leading, spacing: 2) {
                Text(peer.shownName)
                    .font(.system(.body))
                Text(peer.address)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.secondary)
            }
            Spacer()
            if let quality = tunnel.peerStates[peer.name],
               let pill = qualityPill(quality) {
                QualityPill(text: pill.text, color: pill.color)
            }
        }
        .padding(.vertical, 2)
    }

    private func qualityPill(_ state: String) -> (text: String, color: Color)? {
        switch state {
        case "ice-ready": return ("直连", .green)
        case "lrp-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
        default: return nil
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
