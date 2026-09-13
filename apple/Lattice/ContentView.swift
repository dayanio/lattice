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
    @State private var vpnStatus: NEVPNStatus = .invalid
    @State private var serverURL = ""
    @State private var joinToken = ""
    @State private var showingEnroll = false

    @State private var tunnelManager: NETunnelProviderManager?

    var body: some View {
        NavigationStack {
            List {
                Section("连接") {
                    if vpnStatus == .connected {
                        HStack {
                            Image(systemName: "checkmark.circle.fill").foregroundColor(.green)
                            Text("已连接").font(.headline)
                        }
                    } else {
                        Button("连接到 Lattice") {
                            startTunnel()
                        }
                    }
                }

                Section("入网") {
                    TextField("服务器 URL", text: $serverURL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    SecureField("入网令牌", text: $joinToken)
                    Button("保存并启用") {
                        saveAndStart()
                    }
                    .disabled(serverURL.isEmpty || joinToken.isEmpty)
                }
            }
            .navigationTitle("Lattice")
            .onAppear { loadTunnelStatus() }
        }
    }

    private func loadTunnelStatus() {
        NETunnelProviderManager.loadAllFromPreferences { managers, error in
            DispatchQueue.main.async {
                if let m = managers?.first {
                    self.tunnelManager = m as? NETunnelProviderManager
                    self.vpnStatus = self.tunnelManager?.connection.status ?? .invalid
                }
            }
        }
    }

    private func startTunnel() {
        guard let tm = tunnelManager else { return }
        try? tm.connection.startVPNTunnel()
    }

    private func saveAndStart() {
        let providerProtocol = NETunnelProviderProtocol()
        providerProtocol.providerBundleIdentifier = "io.lattice.ios.tunnel"
        providerProtocol.serverAddress = serverURL
        providerProtocol.providerConfiguration = [
            "serverURL": serverURL,
            "token": joinToken,
        ]

        guard let tm = tunnelManager else { return }
        tm.protocolConfiguration = providerProtocol
        tm.isEnabled = true
        tm.saveToPreferences { error in
            DispatchQueue.main.async {
                if error == nil {
                    try? tm.connection.startVPNTunnel()
                }
            }
        }
    }
}
