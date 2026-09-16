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

/// Two-step full-screen join flow:
///   1. Server URL + enrollment token (scan or paste) + device name → creates
///      the VPN profile (TunnelManager.saveJoin) and connects.
///   2. Admin username/password → LatticeAPI.shared.login. Required because
///      the peer-list API needs an admin Bearer token, not the enrollment
///      token (confirmed against the live backend — this is a real backend
///      constraint, not a design choice).
struct JoinView: View {
    var onFinished: () -> Void

    private enum Step { case network, login }
    @State private var step: Step = .network

    // Step 1 state
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var networkError = ""
    @State private var showingScanner = false
    @State private var scannerError = ""

    // Step 2 state
    @State private var username = "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""

    var body: some View {
        NavigationStack {
            switch step {
            case .network: networkStep
            case .login: loginStep
            }
        }
    }

    private var networkStep: some View {
        Form {
            Section("扫码加入") {
                Button {
                    showingScanner = true
                } label: {
                    Label("扫描二维码", systemImage: "qrcode.viewfinder")
                }
            }

            Section("或手动输入") {
                TextField("服务器 URL (http://…)", text: $serverURL)
                    .keyboardType(.URL)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                SecureField("入网令牌", text: $joinToken)
                TextField("节点名称", text: $deviceName)
            }

            if !networkError.isEmpty {
                Text(networkError).font(.caption).foregroundColor(.red)
            }

            Section {
                Button {
                    saveAndConnect()
                } label: {
                    if isSavingNetwork {
                        ProgressView()
                    } else {
                        Text("加入网络")
                    }
                }
                .disabled(serverURL.isEmpty || joinToken.isEmpty || isSavingNetwork)
            }
        }
        .navigationTitle("加入网络")
        .sheet(isPresented: $showingScanner) {
            NavigationStack {
                ZStack(alignment: .bottom) {
                    QRScannerView(
                        onCode: { code in
                            handleScanned(code)
                            showingScanner = false
                        },
                        onError: { message in
                            scannerError = message
                        }
                    )
                    if !scannerError.isEmpty {
                        Text(scannerError)
                            .font(.caption)
                            .foregroundColor(.white)
                            .padding(8)
                            .background(.black.opacity(0.6))
                            .cornerRadius(8)
                            .padding(.bottom, 24)
                    }
                }
                .navigationTitle("扫描二维码")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("取消") { showingScanner = false }
                    }
                }
            }
        }
    }

    private func handleScanned(_ code: String) {
        guard let payload = JoinPayload(code) else {
            scannerError = "二维码格式不正确"
            return
        }
        if let server = payload.serverURL { serverURL = server }
        if let token = payload.token { joinToken = token }
    }

    private func saveAndConnect() {
        isSavingNetwork = true
        networkError = ""
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        UserDefaults.standard.set(deviceName, forKey: "lattice.nodeName")
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: joinToken, name: deviceName) { err in
            isSavingNetwork = false
            if let err {
                networkError = err
                return
            }
            joinToken = ""
            TunnelManager.shared.connect()
            step = .login
        }
    }

    private var loginStep: some View {
        Form {
            Section("登录管理面板") {
                Text("查看节点列表需要管理员账号。")
                    .font(.caption)
                    .foregroundColor(.secondary)
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
                .disabled(username.isEmpty || password.isEmpty || isLoggingIn)
            }
        }
        .navigationTitle("登录")
    }

    private func login() async {
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            onFinished()
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}
