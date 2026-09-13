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
import NetworkExtension

struct ContentView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSaving = false
    @State private var errorText = ""

    var body: some View {
        NavigationStack {
            List {
                Section("连接") {
                    HStack {
                        Text(tunnel.statusText)
                            .foregroundColor(tunnel.status == .connected ? .green : .primary)
                        Spacer()
                        Toggle("", isOn: tunnel.connectedBinding)
                            .labelsHidden()
                    }
                    if !tunnel.lastStartError.isEmpty {
                        Text(tunnel.lastStartError)
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                }

                Section("入网配置") {
                    TextField("服务器 URL (http://…)", text: $serverURL)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    SecureField("入网令牌", text: $joinToken)
                    TextField("节点名称", text: $deviceName)
                    if !errorText.isEmpty {
                        Text(errorText).font(.caption).foregroundColor(.red)
                    }
                    Button {
                        saveAndConnect()
                    } label: {
                        if isSaving {
                            ProgressView()
                        } else {
                            Text("保存并连接")
                        }
                    }
                    .disabled(serverURL.isEmpty || joinToken.isEmpty || isSaving)
                }
            }
            .navigationTitle("Lattice")
            .onAppear { tunnel.load() }
        }
    }

    private func saveAndConnect() {
        isSaving = true
        errorText = ""
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: joinToken, name: deviceName) { err in
            isSaving = false
            if let err {
                errorText = err
            } else {
                joinToken = ""
                tunnel.connect()
            }
        }
    }
}
