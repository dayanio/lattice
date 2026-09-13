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

// MARK: - Model

struct PeerNode: Identifiable {
    let id = UUID()
    let name: String
    let address: String
    let online: Bool
}

// MARK: - API Response

struct PeerListResponse: Codable {
    let code: Int
    let data: PeerListData?
    struct PeerListData: Codable {
        let total: Int
        let list: [PeerItem]?
    }
    struct PeerItem: Codable {
        let name: String
        let address: String
        let status: String
    }
}

// MARK: - ContentView

struct ContentView: View {
    @State private var peers: [PeerNode] = []
    @State private var isLoading = true
    @State private var errorMsg = ""
    @State private var copiedIP = ""

    private let baseURL = "http://127.0.0.1:8080"

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()

            if isLoading {
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
                ScrollView {
                    VStack(spacing: 0) {
                        ForEach(peers) { peer in
                            PeerRow(peer: peer)
                            Divider().padding(.leading, 44)
                        }
                    }
                }
            }

            Divider()
            footer
        }
        .frame(width: 320)
        .frame(minHeight: 300, maxHeight: 480)
        .task { await loadPeers() }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Circle()
                .fill(peers.isEmpty ? Color.gray : Color.green)
                .frame(width: 10, height: 10)
            Text(peers.isEmpty ? "未连接" : "已连接")
                .font(.system(.headline, design: .rounded))
            Spacer()
            Text("Lattice")
                .font(.system(.caption, design: .rounded))
                .foregroundColor(.secondary)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
    }

    private var footer: some View {
        HStack {
            Text("Lattice standalone").font(.caption2).foregroundColor(.secondary)
            Spacer()
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
            guard let url = URL(string: baseURL + "/api/v1/peers/list?page=1&pageSize=50") else {
                throw URLError(.badURL)
            }
            var req = URLRequest(url: url, timeoutInterval: 10)
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            let (data, _) = try await URLSession.shared.data(for: req)
            let decoded = try JSONDecoder().decode(PeerListResponse.self, from: data)
            guard let list = decoded.data?.list else { return }
            peers = list.map { p in
                PeerNode(name: p.name, address: p.address, online: p.status == "online")
            }
        } catch {
            errorMsg = "加载失败: \(error.localizedDescription)"
        }
    }
}

// MARK: - Peer Row

struct PeerRow: View {
    let peer: PeerNode
    @State private var copied = false

    var body: some View {
        HStack(spacing: 10) {
            Circle()
                .fill(peer.online ? Color.green : Color.gray.opacity(0.4))
                .frame(width: 7, height: 7)

            VStack(alignment: .leading, spacing: 1) {
                Text(peer.name).font(.system(.body, design: .default))
                Text(peer.address)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.secondary)
            }

            Spacer()

            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(peer.address, forType: .string)
                copied = true
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .font(.caption2)
                    .foregroundColor(copied ? .green : .secondary)
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 6)
        .contentShape(Rectangle())
        .onTapGesture {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(peer.address, forType: .string)
            copied = true
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
        }
    }

    @State private var copied = false
}
