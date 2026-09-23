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

/// 共享发布：把网内某节点的本地端口经发布网关（:8090）以 URL 形式对外提供。
/// v1 能力边界：HTTP、路径前缀路由、无 TLS/访问令牌。
struct ShareView: View {
    @State private var publishes: [PublishItem] = []
    @State private var isLoading = false
    @State private var errorText = ""
    @State private var showingNewSheet = false

    /// 发布网关地址：控制面主机 + 固定网关端口（v1 约定 :8090）。
    private var gatewayBase: String {
        guard let url = URL(string: LatticeAPI.shared.serverURL),
              let host = url.host else { return "http://<网关>:8090" }
        return "http://\(host):8090"
    }

    var body: some View {
        Group {
            if isLoading && publishes.isEmpty {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if publishes.isEmpty {
                ContentUnavailableView(
                    "还没有发布",
                    systemImage: "square.and.arrow.up.outside",
                    description: Text("把网内节点的服务发布成 URL，供网关外部的设备访问。")
                )
            } else {
                publishList
            }
        }
        .navigationTitle("共享发布")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            Button { showingNewSheet = true } label: { Image(systemName: "plus") }
        }
        .sheet(isPresented: $showingNewSheet) {
            NewPublishSheet(onDone: { Task { await load() } })
        }
        .task { await load() }
        .refreshable { await load() }
    }

    private var publishList: some View {
        List {
            Section {
                ForEach(publishes) { item in
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Text(item.name)
                                .font(.system(.body, weight: .semibold))
                            Spacer()
                            Button {
                                PeerActions.copyToClipboard("\(gatewayBase)/\(item.name)/")
                            } label: {
                                Image(systemName: "doc.on.doc")
                                    .font(.caption)
                            }
                            .buttonStyle(.borderless)
                        }
                        Text("\(item.peerName):\(item.port)")
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Text("\(gatewayBase)/\(item.name)/")
                            .font(.system(.caption2, design: .monospaced))
                            .foregroundColor(LatticePalette.accent)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
                    .padding(.vertical, 2)
                }
                .onDelete { offsets in
                    let names = offsets.map { publishes[$0].name }
                    Task {
                        for name in names {
                            try? await LatticeAPI.shared.deletePublish(name)
                        }
                        await load()
                    }
                }
            } footer: {
                Text("网关：\(gatewayBase)。发布经网关以 HTTP 反代访问目标节点；v1 暂无 TLS 与访问令牌，请勿发布敏感服务。")
            }

            if !errorText.isEmpty {
                Section {
                    Text(errorText).font(.caption).foregroundColor(.red)
                }
            }
        }
    }

    private func load() async {
        isLoading = true
        errorText = ""
        defer { isLoading = false }
        do {
            publishes = try await LatticeAPI.shared.listPublishes()
        } catch {
            errorText = "加载失败：\(error.localizedDescription)"
        }
    }
}

/// 新发布表单：发布名即 URL 路径（小写字母/数字/连字符，3-32 位）。
private struct NewPublishSheet: View {
    var onDone: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var port = ""
    @State private var peers: [PeerNode] = []
    @State private var selectedPeer = ""
    @State private var isSaving = false
    @State private var errorText = ""

    private var nameValid: Bool {
        !name.isEmpty && name.range(of: "^[a-z0-9-]{3,32}$", options: .regularExpression) != nil
    }
    private var portValid: Bool {
        (1...65535).contains(Int(port) ?? 0)
    }
    private var canSave: Bool { nameValid && portValid && !selectedPeer.isEmpty }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("my-service", text: $name)
                        .font(.system(.body, design: .monospaced))
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    LabeledContent("目标节点") {
                        Picker("目标节点", selection: $selectedPeer) {
                            ForEach(peers) { peer in
                                Text(peer.displayName.isEmpty ? peer.name : peer.displayName)
                                    .tag(peer.name)
                            }
                        }
                        .labelsHidden()
                    }
                    TextField("8080", text: $port)
                        .font(.system(.body, design: .monospaced))
                        .keyboardType(.numberPad)
                } header: {
                    Text("发布配置")
                } footer: {
                    Text("发布后可通过 \(name.isEmpty ? "<发布名>" : name)/ 路径访问该节点的本地端口。发布名：小写字母、数字、连字符，3-32 位。")
                }

                Section {
                    Button {
                        Task { await save() }
                    } label: {
                        if isSaving {
                            ProgressView().frame(maxWidth: .infinity)
                        } else {
                            Text("创建发布").frame(maxWidth: .infinity)
                        }
                    }
                    .disabled(!canSave)
                }

                if !errorText.isEmpty {
                    Section {
                        Text(errorText).font(.caption).foregroundColor(.red)
                    }
                }
            }
            .navigationTitle("新发布")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("取消") { dismiss() }
                }
            }
            .task {
                peers = ((try? await LatticeAPI.shared.listPeers()) ?? [])
                    .filter { $0.approvalStatus != "pending" }
                selectedPeer = peers.first?.name ?? ""
            }
        }
    }

    private func save() async {
        guard let portNumber = Int(port) else {
            errorText = "端口必须是数字"
            return
        }
        isSaving = true
        errorText = ""
        defer { isSaving = false }
        do {
            try await LatticeAPI.shared.createPublish(name: name, peer: selectedPeer, port: portNumber)
            onDone()
            dismiss()
        } catch {
            errorText = "创建失败：\(error.localizedDescription)"
        }
    }
}
