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

/// 广播子网路由：把本机可达的网段（CIDR）宣告进网络，其他节点经由本机访问
/// 这些网段。声明经服务端同步到对端；对端是否代为转发取决于其网关能力。
struct SubnetRoutesView: View {
    @State private var routes: [String] = []
    @State private var newRoute = ""
    @State private var suggestion: String?
    @State private var isSaving = false
    @State private var infoText = ""
    @State private var errorText = ""
    @State private var loaded = false

    private var selfName: String {
        UserDefaults.standard.string(forKey: "lattice.nodeName") ?? ""
    }

    var body: some View {
        List {
            Section {
                ForEach(routes, id: \.self) { cidr in
                    Text(cidr)
                        .font(.system(.body, design: .monospaced))
                }
                .onDelete { routes.remove(atOffsets: $0) }

                HStack {
                    TextField("192.168.1.0/24", text: $newRoute)
                        .font(.system(.body, design: .monospaced))
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                        .onSubmit(addRoute)
                    Button("添加") { addRoute() }
                        .disabled(newRoute.isEmpty)
                }

                if let suggestion {
                    Button {
                        if !routes.contains(suggestion) { routes.append(suggestion) }
                    } label: {
                        Label("添加本机网段 \(suggestion)", systemImage: "plus.circle.fill")
                            .font(.caption)
                    }
                }
            } header: {
                Text("宣告的网段")
            } footer: {
                Text("其他节点访问这些网段时，流量经本机代为转发。声明经服务端同步给对端；对端是否代转取决于其网关能力。")
            }

            Section {
                Button {
                    Task { await save() }
                } label: {
                    if isSaving {
                        ProgressView()
                    } else {
                        Text("保存并广播").frame(maxWidth: .infinity)
                    }
                }
            } footer: {
                if !infoText.isEmpty {
                    Text(infoText).foregroundColor(.green)
                }
                if !errorText.isEmpty {
                    Text(errorText).foregroundColor(.red)
                }
            }
        }
        .navigationTitle("广播子网路由")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        guard let peers = try? await LatticeAPI.shared.listPeers(),
              let mine = peers.first(where: { $0.name == selfName }) else {
            errorText = "无法加载本机节点信息"
            return
        }
        routes = mine.advertisedRoutes.filter { $0 != "0.0.0.0/0" }
        suggestion = SubnetRoute.localSuggestion()
        loaded = true
    }

    private func addRoute() {
        let raw = newRoute.trimmingCharacters(in: .whitespaces)
        guard !raw.isEmpty else { return }
        guard let cidr = SubnetRoute.normalized(raw) else {
            errorText = "无效的 CIDR 格式：\(raw)"
            return
        }
        errorText = ""
        if !routes.contains(cidr) { routes.append(cidr) }
        newRoute = ""
    }

    private func save() async {
        guard !selfName.isEmpty else {
            errorText = "未知本机节点名"
            return
        }
        isSaving = true
        errorText = ""
        infoText = ""
        defer { isSaving = false }
        do {
            try await LatticeAPI.shared.setAdvertisedRoutes(selfName, routes: routes)
            infoText = "已保存并广播"
        } catch {
            errorText = "保存失败：\(error.localizedDescription)"
        }
    }
}
