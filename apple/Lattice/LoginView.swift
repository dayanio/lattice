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

/// 管理后台登录页：仅用于解锁设备列表/管理功能。隧道连接本身不需要登录。
struct LoginView: View {
    var onFinished: () -> Void

    @State private var username = "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""
    @Environment(\.dismiss) private var dismiss

    private var serverURL: String { UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "" }

    var body: some View {
        NavigationStack {
            Form {
                Section("登录管理面板") {
                    Text("登录后可查看与管理设备列表。隧道连接本身不依赖登录。")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    if serverURL.isEmpty {
                        Label("尚未加入网络，请先在首页选择加入方式。", systemImage: "exclamationmark.triangle")
                            .font(.caption)
                            .foregroundColor(LatticePalette.blocked)
                    } else {
                        LabeledContent("服务器", value: serverURL)
                            .font(.system(.caption, design: .monospaced))
                    }
                    TextField("用户名", text: $username)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    SecureField("密码", text: $password)
                }

                if !loginError.isEmpty {
                    Text(loginError).font(.caption).foregroundColor(.red)
                }

                Section {
                    Button {
                        Task { await login() }
                    } label: {
                        if isLoggingIn {
                            ProgressView()
                        } else {
                            Text("登录")
                        }
                    }
                    .disabled(username.isEmpty || password.isEmpty || isLoggingIn || serverURL.isEmpty)
                }
            }
            .navigationTitle("登录")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("取消") { dismiss() }
                }
            }
        }
    }

    private func login() async {
        guard !serverURL.isEmpty else {
            loginError = "尚未加入网络，请先在首页选择加入方式。"
            return
        }
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            dismiss()
            onFinished()
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}
