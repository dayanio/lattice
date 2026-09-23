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
    @State private var pendingIdentityReset = false
    @State private var showingResetIdentityConfirm = false
    @State private var showedCopiedFeedback = false
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue
    @ObservedObject private var auth = AuthSession.shared

    private var truncatedPublicKey: String {
        let key = tunnel.localPublicKey
        guard key.count > 16 else { return key }
        return "\(key.prefix(8))…\(key.suffix(8))"
    }

    /// 本机节点的 mesh 域名：引擎内置 DNS 以 <节点名>.lattice 应答，可代替 IP 互访。
    private var dnsName: String {
        let name = UserDefaults.standard.string(forKey: "lattice.nodeName") ?? ""
        guard !name.isEmpty else { return "—" }
        return "\(DeviceName.normalized(name)).lattice"
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    if !auth.isLoggedIn {
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
                        NavigationLink("出口节点") { ExitNodeView() }
                        NavigationLink("广播子网路由") { SubnetRoutesView() }
                        NavigationLink("投屏接收") { CastReceiverPage() }
                        LabeledContent("本机节点", value: UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "—")
                        LabeledContent("LatticeDNS") {
                            Text(dnsName)
                                .font(.system(.caption, design: .monospaced))
                                .textSelection(.enabled)
                        }
                    } else {
                        Button { showingJoin = true } label: {
                            Label("加入网络", systemImage: "qrcode.viewfinder")
                        }
                    }
                }

                if auth.isLoggedIn {
                    Section("共享") {
                        NavigationLink("共享发布") { ShareView() }
                        NavigationLink("传文件") { FileDropView() }
                    }
                }

                if tunnel.isConfigured {
                    Section("设备身份") {
                        Button {
                            PeerActions.copyToClipboard(tunnel.localPublicKey)
                            showedCopiedFeedback = true
                            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
                                showedCopiedFeedback = false
                            }
                        } label: {
                            HStack {
                                Text("公钥")
                                    .foregroundColor(.primary)
                                Spacer()
                                Text(showedCopiedFeedback ? "已复制" : truncatedPublicKey)
                                    .font(.system(.body, design: .monospaced))
                                    .foregroundColor(.secondary)
                            }
                        }
                        LabeledContent("Overlay IP", value: tunnel.localOverlayIP)
                        Text("私钥已在本机安全存储，不会显示或导出。")
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Button("重新生成密钥", role: .destructive) {
                            showingResetIdentityConfirm = true
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
            .sheet(isPresented: $showingJoin, onDismiss: {
                pendingIdentityReset = false
            }) {
                JoinView(onFinished: {
                    showingJoin = false
                    pendingIdentityReset = false
                }, mode: .scan, initialResetIdentity: pendingIdentityReset)
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
            .confirmationDialog(
                "重新生成密钥？",
                isPresented: $showingResetIdentityConfirm,
                titleVisibility: .visible
            ) {
                Button("重新生成", role: .destructive) { startIdentityReset() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("这会清除当前设备身份，需要重新扫码或输入令牌入网。仅在怀疑密钥泄露时使用。")
            }
        }
    }

    /// 退出管理会话：只清除登录态，保留加入信息与 VPN 配置。
    private func logout() {
        LatticeAPI.shared.logout()
    }

    private func leaveNetwork() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            LatticeAPI.shared.logout()
            tunnel.load()
        }
    }

    /// 重新生成密钥：清空当前入网状态后弹出加入页面，这一次的加入请求会带上
    /// resetIdentity，让隧道进程先删掉本地持久化的私钥文件再重新注册。
    private func startIdentityReset() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            LatticeAPI.shared.logout()
            tunnel.load()
            pendingIdentityReset = true
            showingJoin = true
        }
    }
}
