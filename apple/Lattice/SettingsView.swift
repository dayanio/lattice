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

/// 主题三选（spec §五），@AppStorage 持久化，键 lattice.theme。
enum LatticeTheme: String, CaseIterable, Identifiable {
    case system, light, dark
    var id: String { rawValue }
    var label: String {
        switch self {
        case .system: return "跟随系统"
        case .light: return "浅色"
        case .dark: return "深色"
        }
    }
    var colorScheme: ColorScheme? {
        switch self {
        case .system: return nil
        case .light: return .light
        case .dark: return .dark
        }
    }
}

struct SettingsView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var showingLeaveConfirm = false
    @State private var showingLogin = false
    @State private var showingJoin = false
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue
    @AppStorage("lattice.authToken") private var authToken = ""

    var body: some View {
        NavigationStack {
            List {
                Section {
                    if authToken.isEmpty {
                        Button { showingLogin = true } label: {
                            Label("登录管理后台", systemImage: "person.crop.circle.badge.plus")
                        }
                    } else {
                        HStack(spacing: 12) {
                            Image(systemName: "person.crop.circle.fill")
                                .font(.system(size: 40))
                                .foregroundColor(LatticePalette.accent)
                            VStack(alignment: .leading, spacing: 2) {
                                Text(UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin")
                                    .font(.system(.body, weight: .semibold))
                                Text(tunnel.serverURL ?? "—")
                                    .font(.system(.caption, design: .monospaced))
                                    .foregroundColor(.secondary)
                            }
                        }
                        .padding(.vertical, 4)
                        Button("退出登录") { logout() }
                    }
                }

                Section("网络") {
                    if tunnel.isConfigured {
                        NavigationLink("退出节点") { ExitNodeView() }
                        LabeledContent("本机节点", value: UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "—")
                    } else {
                        Button { showingJoin = true } label: {
                            Label("加入网络", systemImage: "qrcode.viewfinder")
                        }
                    }
                }

                Section("偏好") {
                    Picker("主题", selection: $theme) {
                        ForEach(LatticeTheme.allCases) { t in
                            Text(t.label).tag(t.rawValue)
                        }
                    }
                }

                Section {
                    LabeledContent("版本", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                }

                if tunnel.isConfigured {
                    Section {
                        Button("退出网络", role: .destructive) { showingLeaveConfirm = true }
                    }
                }
            }
            .navigationTitle("设置")
            .sheet(isPresented: $showingLogin) {
                LoginView(onFinished: { showingLogin = false })
            }
            .sheet(isPresented: $showingJoin) {
                JoinView(onFinished: { showingJoin = false }, mode: .scan)
            }
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

    /// 退出管理会话：只清除登录态，保留加入信息与 VPN 配置。
    private func logout() {
        UserDefaults.standard.removeObject(forKey: "lattice.authToken")
        UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
        KeychainStore.delete("lattice.password")
    }

    private func leaveNetwork() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            UserDefaults.standard.removeObject(forKey: "lattice.authToken")
            UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
            UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
            KeychainStore.delete("lattice.password")
            tunnel.load()
        }
    }
}
