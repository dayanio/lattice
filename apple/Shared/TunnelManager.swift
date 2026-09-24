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

    /// Nodes as the tunnel reports them; lets the device list work without a
    /// management login. Empty while the tunnel is not connected.
    @Published private(set) var tunnelPeers: [PeerNode] = []
    private var tunnelPeerRaw: [TunnelPeer] = []

    /// The workspace is holding this device until an administrator approves it.
    /// The tunnel connects on its own afterwards.
    @Published private(set) var awaitingApproval = false

    /// What to tell the user about a connection that is not up: the reason, or
    /// that an administrator has to approve the device first. nil when there is
    /// nothing to say.
    var lastFailure: JoinFailure? {
        if awaitingApproval { return .awaitingApproval }
        return lastStartError.isEmpty ? nil : JoinFailure.classify(lastStartError)
    }
    /// Per-peer connection quality from the tunnel process
    /// (peer name → "ice-ready" | "lrp-ready" | "probing" | ...).
    @Published private(set) var peerStates: [String: String] = [:]
    /// Per-peer merged metrics (state + WireGuard counters + RTT) from the
    /// tunnel process, refreshed with the same poll as peerStates.
    @Published private(set) var peerStats: [String: PeerStat] = [:]
    /// This device's own WireGuard public key, polled from the running
    /// extension — "" until the engine has loaded/generated its identity.
    @Published private(set) var localPublicKey: String = ""
    /// This device's overlay IP, polled from the running extension.
    @Published private(set) var localOverlayIP: String = ""

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
        case .connecting, .reasserting: return .connecting
        case .disconnecting: return .disconnecting
        default: return .disconnected
        }
    }

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?
    private var statePoller: Timer?
    private var connectingSince: Date?

    /// Nonce recorded when the last join was a reset-join (createProfile wrote
    /// "resetIdentity" into the persisted provider configuration). The reset
    /// itself is applied by the extension on that session's startTunnel; the
    /// flag must then be stripped from the persisted profile after the first
    /// connected observation, or every later system-initiated restart (reboot,
    /// VPN toggle, jetsam kill+restart) would regenerate the identity again —
    /// same node name, different key — which the server rejects, locking the
    /// device out.
    private var pendingProfileResetNonce: String?

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
    ///   - resetIdentity: when true, the extension regenerates the engine's
    ///     WireGuard identity instead of reusing the stored one
    func saveJoin(serverURL: String, token: String, name: String, resetIdentity: Bool = false, completion: ((String?) -> Void)? = nil) {
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
                self.createProfile(serverURL: serverURL, token: token, name: name, resetIdentity: resetIdentity, completion: completion)
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

    private func createProfile(serverURL: String, token: String, name: String, resetIdentity: Bool = false, completion: ((String?) -> Void)? = nil) {
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = serverURL
        var config: [String: Any] = [
            "serverURL": serverURL,
            "token": token,
            "name": name,
        ]
        if resetIdentity {
            config["resetIdentity"] = true
            // Fresh nonce per reset-join: marks this profile copy as armed so
            // the flag can be consumed exactly once (see pendingProfileResetNonce);
            // a later genuine reset always re-arms with a new value.
            let nonce = UUID().uuidString
            config["resetNonce"] = nonce
            pendingProfileResetNonce = nonce
        }
        proto.providerConfiguration = config

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
            connectingSince = nil
            if connectedSince == nil { connectedSince = Date() }
            lastStartError = ""
            startStatePoller()
            consumeProfileResetFlagIfNeeded()
        } else {
            connectedSince = nil
            // The engine says why it is not up (an error, or waiting for approval)
            // only while the extension runs, so keep asking while it connects.
            if status == .connecting || status == .reasserting {
                if connectingSince == nil { connectingSince = Date() }
                startStatePoller()
            } else {
                connectingSince = nil
                stopStatePoller()
            }
            if peerStates.isEmpty == false {
                peerStates = [:]
            }
            if peerStats.isEmpty == false {
                peerStats = [:]
            }
            if !tunnelPeerRaw.isEmpty {
                tunnelPeerRaw = []
                tunnelPeers = []
            }
            if awaitingApproval && (status == .disconnected || status == .invalid) {
                awaitingApproval = false
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

    /// Consume-once for the reset-join flag: on the first .connected
    /// observation after a reset-join, rewrite the persisted profile WITHOUT
    /// "resetIdentity"/"resetNonce". The extension already applied the reset
    /// during that session's startTunnel, so the rewrite only prevents
    /// SUBSEQUENT system-initiated restarts from re-resetting. Save-only (no
    /// enable/disable churn — a one-time session re-save is acceptable);
    /// the pending nonce is cleared immediately on the main queue in all
    /// paths so repeated connected observations or a failed save can't loop.
    private func consumeProfileResetFlagIfNeeded() {
        guard pendingProfileResetNonce != nil else { return }
        pendingProfileResetNonce = nil
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            DispatchQueue.main.async {
                guard let mgr = managers?.first(where: {
                    ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
                }),
                let proto = mgr.protocolConfiguration as? NETunnelProviderProtocol,
                let config = proto.providerConfiguration,
                config["resetIdentity"] != nil else { return }
                var cleaned = config
                cleaned.removeValue(forKey: "resetIdentity")
                cleaned.removeValue(forKey: "resetNonce")
                proto.providerConfiguration = cleaned
                mgr.saveToPreferences { _ in }
            }
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
        let peerStates: [String: PeerStat]
        let lastError: String?
        let publicKey: String?
        let overlayIP: String?
        let peers: [TunnelPeer]?
        let phase: String?
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
                        // The extension takes a moment to start; only a long
                        // silence is worth reporting.
                        if self.status == .connecting,
                           let since = self.connectingSince, Date().timeIntervalSince(since) > 15 {
                            self.lastStartError = "隧道进程无响应，请重试连接或重启 App"
                        }
                        return
                    }
                    guard let snap = try? JSONDecoder().decode(ProviderSnapshot.self, from: data) else { return }
                    self.peerStats = snap.peerStates
                    let states = snap.peerStates.mapValues { $0.state ?? "" }
                    if states != self.peerStates {
                        self.peerStates = states
                    }
                    if let list = snap.peers, list != self.tunnelPeerRaw {
                        self.tunnelPeerRaw = list
                        self.tunnelPeers = list.map(\.node)
                    }
                    if let publicKey = snap.publicKey, !publicKey.isEmpty {
                        self.localPublicKey = publicKey
                    }
                    // "10.96.0.1" is the providers' pre-connect fallback, not a real assignment.
                    if let overlayIP = snap.overlayIP, !overlayIP.isEmpty, overlayIP != "10.96.0.1" {
                        self.localOverlayIP = overlayIP
                    }
                    self.awaitingApproval = snap.phase == "awaiting-approval" && self.status != .connected
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
