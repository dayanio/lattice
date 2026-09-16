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

struct SettingsView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var showingLeaveConfirm = false

    var body: some View {
        NavigationStack {
            List {
                Section("网络") {
                    LabeledContent("服务器", value: tunnel.serverURL ?? "—")
                    NavigationLink("退出节点") {
                        ExitNodeView()
                    }
                }

                Section {
                    Button("退出网络", role: .destructive) {
                        showingLeaveConfirm = true
                    }
                }

                Section {
                    LabeledContent("版本", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                }
            }
            .navigationTitle("设置")
            .confirmationDialog(
                "退出网络？",
                isPresented: $showingLeaveConfirm,
                titleVisibility: .visible
            ) {
                Button("退出网络", role: .destructive) { leaveNetwork() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("需要重新扫码或输入 token 才能再次加入。")
            }
        }
    }

    private func leaveNetwork() {
        tunnel.disconnect()
        UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
        UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
        UserDefaults.standard.removeObject(forKey: "lattice.authToken")
        UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        KeychainStore.delete("lattice.password")
        // Actual VPN-profile removal happens the same way TunnelManager.saveJoin
        // already does it (removeFromPreferences for the stale profile) — the
        // next join flow's saveJoin call handles cleanup, so there is nothing
        // further to do here beyond clearing local state and forcing RootView
        // to re-show the join flow, which happens because isConfigured/
        // isLoggedIn now evaluate false the next time evaluateJoinState() runs
        // (RootView.onAppear) — trigger that by reloading:
        tunnel.load()
    }
}
