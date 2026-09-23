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

/// 传文件（LatticeDrop）：把文件经 overlay 直发其他节点的 9530 端口，
/// 同时本机 9530 监听接收。收到的文件在「文件」App → LatticeDrop。
struct FileDropView: View {
    @StateObject private var drop = FileDropService.shared
    @State private var peers: [PeerNode] = []
    @State private var selectedPeer = ""
    @State private var showingImporter = false
    @State private var pendingFile: URL?

    var body: some View {
        List {
            Section {
                HStack {
                    Label(drop.isListening ? "监听中（:9530）" : "监听未启动",
                          systemImage: drop.isListening ? "tray.and.arrow.down.fill" : "tray")
                        .font(.subheadline)
                    Spacer()
                    if drop.isListening {
                        Button("停止") { drop.stopListening() }
                            .font(.caption)
                    } else {
                        Button("启动监听") { drop.startListening() }
                            .font(.caption)
                    }
                }
                Text("收到的文件保存在「文件」App → 我的 iPhone → LatticeDrop。")
                    .font(.caption2)
                    .foregroundColor(.secondary)
            } header: {
                Text("接收")
            }

            Section {
                Picker("目标节点", selection: $selectedPeer) {
                    ForEach(peers) { peer in
                        Text(peer.displayName.isEmpty ? peer.name : peer.displayName)
                            .tag(peer.address)
                    }
                }
                Button {
                    showingImporter = true
                } label: {
                    Label(pendingFile?.lastPathComponent ?? "选择文件", systemImage: "folder")
                        .lineLimit(1)
                }
                Button {
                    if let file = pendingFile {
                        drop.send(fileURL: file, to: selectedPeer)
                        pendingFile = nil
                    }
                } label: {
                    Label("发送", systemImage: "paperplane.fill")
                }
                .disabled(selectedPeer.isEmpty || pendingFile == nil)
            } header: {
                Text("发送到节点")
            } footer: {
                Text("发送走 overlay 直连（TCP :9530），对端需安装 Lattice 客户端。")
            }

            if !drop.events.isEmpty {
                Section("传输记录") {
                    ForEach(drop.events) { event in
                        VStack(alignment: .leading, spacing: 2) {
                            if event.direction == "log" {
                                Text(event.name)
                                    .font(.caption)
                                    .foregroundColor(event.success ? .secondary : .red)
                            } else {
                                HStack {
                                    Image(systemName: event.direction == "in" ? "arrow.down.circle" : "arrow.up.circle")
                                        .foregroundColor(event.success ? LatticePalette.online : .red)
                                    Text(event.name).font(.subheadline).lineLimit(1)
                                    Spacer()
                                    Text(event.direction == "in" ? "接收" : "发送")
                                        .font(.caption2).foregroundColor(.secondary)
                                }
                                if !event.detail.isEmpty {
                                    Text(event.detail)
                                        .font(.caption2)
                                        .foregroundColor(.secondary)
                                        .lineLimit(1)
                                }
                            }
                        }
                    }
                }
            }
        }
        .navigationTitle("传文件")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            drop.startListening()
            Task {
                peers = ((try? await LatticeAPI.shared.listPeers()) ?? [])
                    .filter { $0.approvalStatus != "pending" && !$0.address.isEmpty }
                selectedPeer = peers.first?.address ?? ""
            }
        }
        .fileImporter(isPresented: $showingImporter, allowedContentTypes: [.data]) { result in
            if case .success(let url) = result {
                let accessed = url.startAccessingSecurityScopedResource()
                pendingFile = url
                if !accessed { url.stopAccessingSecurityScopedResource() }
            }
        }
    }
}
