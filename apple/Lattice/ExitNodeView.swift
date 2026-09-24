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

/// Exit Node picker — same data flow as LatticeMac's NetworkSettingsView:
/// candidates are peers advertising "0.0.0.0/0"; selecting one calls
/// setRouteSelection(consumer: selfName, provider: name, selected: true)
/// and clears any previously-selected exit node.
struct ExitNodeView: View {
    @State private var candidates: [PeerNode] = []
    @State private var selectedProviders: Set<String> = []
    @State private var selfName: String = UserDefaults.standard.string(forKey: "lattice.nodeName") ?? ""
    @State private var isLoading = true
    @State private var errorText = ""
    @ObservedObject private var auth = AuthSession.shared
    @State private var showingLogin = false

    var body: some View {
        List {
            if !auth.isLoggedIn {
                Section {
                    Button { showingLogin = true } label: {
                        Label("登录管理后台", systemImage: "person.crop.circle.badge.plus")
                    }
                    Text("出口节点选择需要管理后台登录后使用。")
                        .font(.caption).foregroundColor(.secondary)
                }
            } else if isLoading {
                HStack {
                    Spacer()
                    ProgressView()
                    Spacer()
                }
            } else {
                Section {
                    Button {
                        Task { await selectExitNode(nil) }
                    } label: {
                        HStack {
                            Text("无（关闭）")
                            Spacer()
                            if selectedExitNode == nil {
                                Image(systemName: "checkmark").foregroundColor(.accentColor)
                            }
                        }
                    }
                    .foregroundColor(.primary)

                    ForEach(candidates.filter { $0.advertisedRoutes.contains("0.0.0.0/0") }) { peer in
                        Button {
                            Task { await selectExitNode(peer.name) }
                        } label: {
                            HStack {
                                Text(peer.shownName)
                                Spacer()
                                if selectedExitNode == peer.name {
                                    Image(systemName: "checkmark").foregroundColor(.accentColor)
                                }
                            }
                        }
                        .foregroundColor(.primary)
                    }
                }
                if !errorText.isEmpty {
                    Text(errorText).font(.caption).foregroundColor(.red)
                }
            }
        }
        .navigationTitle("出口节点")
        .navigationBarTitleDisplayMode(.inline)
        .fullScreenCover(isPresented: $showingLogin, onDismiss: { Task { await load() } }) {
            LoginView(onFinished: { showingLogin = false })
        }
        .task { await load() }
    }

    private var selectedExitNode: String? {
        selectedProviders.first { provider in
            candidates.first(where: { $0.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true
        }
    }

    private func load() async {
        guard auth.isLoggedIn else { isLoading = false; return }
        isLoading = true
        errorText = ""
        do {
            let peers = try await LatticeAPI.shared.listPeers()
            candidates = peers.filter { !$0.advertisedRoutes.isEmpty }
            let selected = try await LatticeAPI.shared.listRouteSelections(selfName)
            selectedProviders = Set(selected)
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func selectExitNode(_ name: String?) async {
        let oldExitNodes = selectedProviders.filter { provider in
            candidates.first(where: { $0.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true
        }
        do {
            if let name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: name, selected: true)
            }
            for provider in oldExitNodes where provider != name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: provider, selected: false)
            }
            await load()
        } catch {
            errorText = "选择失败: \(error.localizedDescription)"
            await load()
        }
    }
}
