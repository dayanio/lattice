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
import SwiftUI
import NetworkExtension

/// Owns the VPN profile (NETunnelProviderManager) and its lifecycle, shared
/// between LatticeMac and Lattice (iOS) — the two platforms differ only in
/// tunnel bundle ID. The tunnel itself runs inside the platform's extension
/// (LatticeTunnelMac / LatticeTunnel); this class creates/updates the
/// profile, toggles the connection, and polls the extension for per-peer
/// connection quality over the NE provider-message channel.
final class TunnelManager: ObservableObject {
    static let shared = TunnelManager()

    #if os(macOS)
    static let tunnelBundleID = "io.lattice.mac.tunnel"
    #else
    static let tunnelBundleID = "io.lattice.ios.tunnel"
    #endif
    static let profileName = "Lattice"

    @Published private(set) var isConfigured = false
    @Published private(set) var status: NEVPNStatus = .invalid
    @Published private(set) var lastStartError: String = ""
    /// Per-peer connection quality from the tunnel process
    /// (peer name → "ice-ready" | "lrp-ready" | "probing" | ...).
    @Published private(set) var peerStates: [String: String] = [:]

    /// 本 App 会话内连接建立的时刻（spec §六：冷启动无法取回系统真实起点，
    /// 用"发现连接的时刻"作为计时起点，离开 connected 即清空）。
    @Published private(set) var connectedSince: Date?

    /// The management server this profile points at (panel subtitle).
    var serverURL: String? {
        (manager?.protocolConfiguration as? NETunnelProviderProtocol)?.serverAddress
    }

    var statusText: String {
        switch status {
        case .connected: return "已连接"
        case .connecting, .reasserting: return "连接中…"
        case .disconnecting: return "断开中…"
        default: return "未连接"
        }
    }

    /// Binding for a connect toggle: turning it on with no profile yet is a
    /// no-op (the join flow drives profile creation).
    var connectedBinding: Binding<Bool> {
        Binding(
            get: { self.status == .connected },
            set: { on in
                if on {
                    self.connect()
                } else {
                    self.disconnect()
                }
            }
        )
    }

    /// UI 侧连接态（ConnectionHero 消费；不暴露 NEVPNStatus 给组件层）。
    var connectionState: ConnectionState {
        switch status {
        case .connected: return .connected
        case .connecting, .disconnecting, .reasserting: return .connecting
        default: return .disconnected
        }
    }

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?
    private var statePoller: Timer?

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

    /// Creates or updates the VPN profile with join parameters, then enables
    /// it. Any previous Lattice profile is removed first: the OS pins the
    /// provider's code requirement at profile-creation time, so a stale
    /// profile would reject a rebuilt (correctly signed) extension forever.
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

    /// Removes the Lattice VPN profile from system preferences (退出网络).
    /// Without this the profile lingers, isConfigured stays true, and the
    /// overview would keep treating the device as joined.
    func removeProfile(completion: (() -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            let stale = (managers ?? []).filter {
                ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
            }
            let group = DispatchGroup()
            for m in stale {
                group.enter()
                m.removeFromPreferences { _ in group.leave() }
            }
            group.notify(queue: .main) {
                self.manager = nil
                self.isConfigured = false
                self.status = .invalid
                self.connectedSince = nil
                self.peerStates = [:]
                completion?()
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
        lastStartError = ""
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
        if status == .connected {
            if connectedSince == nil { connectedSince = Date() }
            lastStartError = ""
            startStatePoller()
        } else {
            connectedSince = nil
            stopStatePoller()
            if peerStates.isEmpty == false {
                peerStates = [:]
            }
        }
    }

    private func observeStatus() {
        if let observer {
            NotificationCenter.default.removeObserver(observer)
        }
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: manager?.connection,
            queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    // MARK: - Connection quality (provider message channel)

    private func startStatePoller() {
        guard statePoller == nil else { return }
        pollPeerStates()
        statePoller = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            self?.pollPeerStates()
        }
    }

    private func stopStatePoller() {
        statePoller?.invalidate()
        statePoller = nil
    }

    private struct ProviderSnapshot: Codable {
        let peerStates: [String: String]
        let lastError: String?
    }

    /// Asks the tunnel process for its latest peer-state snapshot over the
    /// NE provider-message channel (see PacketTunnelProvider.handleAppMessage).
    /// Polls while connecting too — a start-phase engine failure otherwise
    /// dies silently and the UI would show 未连接 with no reason.
    private func pollPeerStates() {
        guard let connection = manager?.connection as? NETunnelProviderSession else { return }
        do {
            try connection.sendProviderMessage(Data("peerStates".utf8)) { [weak self] data in
                DispatchQueue.main.async {
                    guard let self else { return }
                    guard let data else {
                        if self.status == .connecting {
                            self.lastStartError = "隧道进程无响应，请重试连接或重启 App"
                        }
                        return
                    }
                    guard let snap = try? JSONDecoder().decode(ProviderSnapshot.self, from: data) else { return }
                    self.peerStates = snap.peerStates
                    if self.status != .connected, let err = snap.lastError, !err.isEmpty {
                        self.lastStartError = err
                    }
                }
            }
        } catch {
            // Session not ready; the next tick retries.
        }
    }
}
