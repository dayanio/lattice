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

/// 账号与网络页：登录账号、角色与退出登录；工作区全量信息与重新入网入口。
/// 低频敏感操作集中在这里，不占 tab——入口在 ⋯ 菜单与概览页。
struct AccountNetworkPage: View {
    var onBack: () -> Void

    @State private var username = ""
    @State private var systemRole = ""
    @State private var isLoggedIn = LatticeAPI.shared.isLoggedIn
    @State private var wsName = ""
    @State private var wsID = ""
    @State private var wsQuota = ""
    @State private var loading = true
    @State private var errorText = ""
    @State private var confirmingLogout = false
    @State private var showingLogin = false
    @State private var showingJoin = false
    @State private var copiedID = false

    private var serverURL: String {
        UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "—"
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.account.title, onBack: onBack)
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if loading {
                        HStack {
                            Spacer()
                            ProgressView().controlSize(.small).padding(.vertical, 16)
                            Spacer()
                        }
                    } else {
                        if !errorText.isEmpty {
                            Text(errorText).font(.caption).foregroundColor(.red)
                        }
                        accountSection
                        networkSection
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .task { await load() }
        .sheet(isPresented: $showingLogin) {
            SettingsView(
                onDone: {
                    showingLogin = false
                    Task { await load() }
                },
                onClose: { showingLogin = false }
            )
        }
        .sheet(isPresented: $showingJoin) {
            JoinView(
                onDone: { showingJoin = false },
                onClose: { showingJoin = false }
            )
        }
        .confirmationDialog("退出登录？", isPresented: $confirmingLogout, titleVisibility: .visible) {
            Button("退出登录", role: .destructive) { Task { await logout() } }
            Button("取消", role: .cancel) {}
        } message: {
            Text("退出后需要重新登录才能管理设备。")
        }
    }

    // MARK: 账号

    private var accountSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("账号")
                .font(.caption2.weight(.bold))
                .textCase(.uppercase)
                .foregroundColor(.secondary)
            if isLoggedIn {
                HStack(spacing: 12) {
                    ZStack {
                        Circle().fill(Color.accentColor.opacity(0.16)).frame(width: 40, height: 40)
                        Text(String(username.first ?? "A").uppercased())
                            .font(.system(.headline, design: .rounded))
                            .foregroundColor(.accentColor)
                    }
                    VStack(alignment: .leading, spacing: 2) {
                        Text(username.isEmpty ? "已登录" : username)
                            .font(.system(.body, design: .rounded).weight(.semibold))
                        Text(roleText).font(.caption2).foregroundColor(.secondary)
                    }
                    Spacer()
                    Button("退出登录") { confirmingLogout = true }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
                .padding(12)
            } else {
                HStack {
                    Text("未登录 — 登录后可管理设备")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Spacer()
                    Button("登录…") { showingLogin = true }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                }
                .padding(12)
            }
        }
        .background(cardBackground)
    }

    private var roleText: String {
        switch systemRole {
        case "platform_admin": return "平台管理员"
        case "admin": return "管理员"
        case "user": return "成员"
        default: return systemRole.isEmpty ? "成员" : systemRole
        }
    }

    // MARK: 网络

    private var networkSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("网络")
                .font(.caption2.weight(.bold))
                .textCase(.uppercase)
                .foregroundColor(.secondary)
            VStack(alignment: .leading, spacing: 8) {
                infoRow("工作区", wsName.isEmpty ? "—" : wsName)
                if !wsID.isEmpty {
                    HStack(spacing: 6) {
                        Text("工作区 ID").font(.caption2).foregroundColor(.secondary)
                        Spacer()
                        Text(wsID)
                            .font(.system(.caption2, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Button {
                            copy(wsID)
                            copiedID = true
                            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copiedID = false }
                        } label: {
                            Image(systemName: copiedID ? "checkmark" : "doc.on.doc")
                                .font(.caption2)
                                .foregroundColor(copiedID ? .green : .secondary)
                        }
                        .buttonStyle(.plain)
                        .help("复制工作区 ID")
                    }
                }
                infoRow("服务器", serverURL)
                if !wsQuota.isEmpty {
                    infoRow("节点配额", wsQuota)
                }
                Divider()
                Button {
                    showingJoin = true
                } label: {
                    Label("重新入网 / 更换网络", systemImage: "arrow.triangle.2.circlepath")
                        .font(.caption)
                }
                .buttonStyle(.plain)
                .foregroundColor(.accentColor)
            }
            .padding(12)
        }
        .background(cardBackground)
    }

    private func infoRow(_ label: String, _ value: String) -> some View {
        HStack(spacing: 8) {
            Text(label).font(.caption2).foregroundColor(.secondary)
            Spacer()
            Text(value)
                .font(.system(.caption, design: .monospaced))
                .lineLimit(1)
                .truncationMode(.middle)
                .textSelection(.enabled)
        }
    }

    private var cardBackground: some View {
        RoundedRectangle(cornerRadius: 10)
            .fill(Color.primary.opacity(0.04))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.08)))
    }

    private func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    private func load() async {
        loading = true
        errorText = ""
        isLoggedIn = LatticeAPI.shared.isLoggedIn
        defer { loading = false }
        guard isLoggedIn else { return }
        do {
            let me = try await LatticeAPI.shared.fetchMe()
            username = me.username ?? ""
            systemRole = me.systemRole ?? ""
            let wss = try await LatticeAPI.shared.listWorkspaces()
            if let ws = wss.first {
                wsName = ws.displayName ?? ws.slug ?? ""
                wsID = ws.id ?? ""
                if let used = ws.nodeCount, let max = ws.maxNodeCount {
                    wsQuota = "\(used) / \(max)"
                }
            }
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
    }

    private func logout() async {
        LatticeAPI.shared.logout()
        isLoggedIn = false
        username = ""
        systemRole = ""
    }
}
