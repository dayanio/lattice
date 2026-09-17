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

/// Join-network flow (scan or manual entry): server URL + enrollment token +
/// device name → creates the VPN profile (TunnelManager.saveJoin) and
/// connects. Admin login is a separate optional step (LoginView) — the
/// tunnel itself only needs the enrollment token.
enum JoinMode {
    case scan
    case manual
}

struct JoinView: View {
    var onFinished: () -> Void
    /// .scan：出现即打开摄像头；.manual：停留在手动输入表单。
    var mode: JoinMode = .manual

    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var networkError = ""
    @State private var showingScanner = false
    @State private var scannerError = ""

    var body: some View {
        NavigationStack {
            networkStep
        }
        .onAppear {
            if mode == .scan { showingScanner = true }
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
        // 完整入网码（服务端地址 + 令牌都在）→ 直接继续，省去手输与再次点击。
        if payload.serverURL != nil && payload.token != nil {
            saveAndConnect()
        }
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
            onFinished()
        }
    }
}
