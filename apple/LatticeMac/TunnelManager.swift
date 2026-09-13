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

import Foundation
import NetworkExtension

/// Owns the macOS VPN profile (NETunnelProviderManager) and its lifecycle.
/// The tunnel itself runs inside the LatticeTunnelMac extension; this class
/// only creates/updates the profile and toggles the connection.
final class TunnelManager: ObservableObject {
    static let shared = TunnelManager()
    static let tunnelBundleID = "io.lattice.mac.tunnel"
    static let profileName = "Lattice"

    @Published private(set) var isConfigured = false
    @Published private(set) var status: NEVPNStatus = .invalid
    @Published private(set) var lastStartError: String?

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?

    private init() {}

    /// Loads (or reloads) the Lattice VPN profile and status from the system.
    func load(_ completion: (() -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            DispatchQueue.main.async {
                self.manager = managers?.first {
                    ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
                }
                self.isConfigured = self.manager != nil
                self.refreshStatus()
                self.observeStatus()
                completion?()
            }
        }
    }

    /// Creates or updates the VPN profile with join parameters, then enables it.
    /// Any previous Lattice profile is removed first: macOS pins the provider's
    /// code requirement at profile-creation time, so a stale profile would
    /// reject a rebuilt (correctly signed) extension forever.
    /// - Parameters:
    ///   - serverURL: management server base URL, e.g. http://172.20.10.4:8080
    ///   - token: enrollment token issued by the control plane
    ///   - name: stable node name (used as the peer identity)
    func saveJoin(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            let stale = (managers ?? []).filter {
                ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
            }
            let group = DispatchGroup()
            for manager in stale {
                group.enter()
                manager.removeFromPreferences { _ in group.leave() }
            }
            group.notify(queue: .main) {
                self.createProfile(serverURL: serverURL, token: token, name: name, completion: completion)
            }
        }
    }

    private func createProfile(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil) {
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = serverURL
        proto.providerConfiguration = [
            "serverURL": serverURL,
            "token": token,
            "name": name,
        ]

        let mgr = NETunnelProviderManager()
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = Self.profileName
        mgr.isEnabled = true
        mgr.saveToPreferences { [weak self] error in
            DispatchQueue.main.async {
                if let error {
                    completion?(error.localizedDescription)
                    return
                }
                // Reload so `manager.connection` points at the saved profile.
                self?.load {
                    completion?(nil)
                }
            }
        }
    }

    func connect() {
        lastStartError = nil
        guard let connection = manager?.connection else { return }
        do {
            try connection.startVPNTunnel()
        } catch {
            lastStartError = error.localizedDescription
        }
    }

    func disconnect() {
        manager?.connection.stopVPNTunnel()
    }

    private func refreshStatus() {
        status = manager?.connection.status ?? .invalid
    }

    private func observeStatus() {
        guard observer == nil else { return }
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: manager?.connection,
            queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
    }
}
