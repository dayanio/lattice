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

// MARK: - ContentView

struct ContentView: View {
    @State private var peers: [PeerNode] = []
    @State private var isLoading = true
    @State private var errorMsg = ""
    @State private var showingSettings = false

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
        .task { await loadPeers() }
        .sheet(isPresented: $showingSettings) {
            SettingsView {
                showingSettings = false
                Task { await loadPeers() }
            }
        }
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
            Button {
                showingSettings = true
            } label: {
                Image(systemName: "gearshape")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("登录设置")

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
            peers = try await LatticeAPI.shared.listPeers()
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
                copyAddress()
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
            copyAddress()
        }
    }

    private func copyAddress() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(peer.address, forType: .string)
        copied = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
    }
}

// MARK: - Settings (Login)

struct SettingsView: View {
    var onDone: () -> Void

    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://127.0.0.1:8080"
    @State private var username = UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("连接到 Lattice")
                .font(.system(.headline, design: .rounded))

            VStack(alignment: .leading, spacing: 4) {
                Text("服务器地址").font(.caption).foregroundColor(.secondary)
                TextField("http://127.0.0.1:8080", text: $serverURL)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(.caption, design: .monospaced))
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("用户名").font(.caption).foregroundColor(.secondary)
                TextField("admin", text: $username)
                    .textFieldStyle(.roundedBorder)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("密码").font(.caption).foregroundColor(.secondary)
                SecureField("", text: $password)
                    .textFieldStyle(.roundedBorder)
            }

            if !loginError.isEmpty {
                Text(loginError)
                    .font(.caption)
                    .foregroundColor(.red)
            }

            HStack {
                Spacer()
                if isLoggingIn {
                    ProgressView().controlSize(.small)
                } else {
                    Button("登录") { Task { await login() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(serverURL.isEmpty || username.isEmpty || password.isEmpty)
                }
            }
        }
        .padding(20)
        .frame(width: 300)
    }

    private func login() async {
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            onDone()
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}
