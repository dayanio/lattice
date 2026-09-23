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

import Combine
import Foundation

enum TunnelError: Error {
    case missingServerAddress
    case missingProviderConfiguration
}


// MARK: - Shared Models

struct PeerNode: Identifiable {
    /// Stable across refreshes (the list is rebuilt every few seconds); a fresh
    /// UUID per build made SwiftUI treat every row as new.
    var id: String { appID.isEmpty ? name : appID }
    let name: String
    let address: String
    let online: Bool
    var displayName: String = ""
    var disabled: Bool = false
    var os: String = ""
    var lastHandshake: String = "—"
    var appID: String = ""
    var labels: [String: String]? = nil
    /// CIDRs this peer offers to route for others (Exit Node = ["0.0.0.0/0"]).
    var advertisedRoutes: [String] = []

    /// 归一化的平台显示名：netmap 里的 os 值大小写混杂且可能缺失，
    /// UI 统一走这个出口。
    var displayOS: String {
        switch os.lowercased() {
        case "linux": return "Linux"
        case "windows": return "Windows"
        case "darwin", "macos", "mac": return "macOS"
        case "android": return "Android"
        case "ios": return "iOS"
        case "tvos": return "tvOS"
        case "": return "未知"
        default: return os
        }
    }
    var lastSeen: String = ""
    /// True when an AgentIdentity references this peer — AI agents are
    /// first-class network citizens and get a badge (UI mockup §04).
    var isAgent: Bool = false
    /// gVisor sandbox state from the AgentIdentity ("none" | "gvisor" | ...).
    var sandbox: String? = nil
    /// Enrollment approval state from the management API (ADR-0003):
    /// "approved" | "pending" | "revoked". nil for tunnel-only peers —
    /// the tunnel's netmap has no notion of approval.
    var approvalStatus: String? = nil
    /// The peer's WireGuard public key (base64) from the management API.
    var publicKey: String = ""

    var shownName: String { displayName.isEmpty ? name : displayName }
}

/// One remote node as the tunnel itself reports it (Engine.Peers()), so the
/// device list works without a management login.
struct TunnelPeer: Codable, Equatable {
    let appId: String
    let name: String
    let address: String
    let platform: String?
    /// probing, ice-ready (direct), lrp-ready (relayed), failed, closed, none.
    let state: String
    let online: Bool

    var node: PeerNode {
        PeerNode(name: name.isEmpty ? appId : name, address: address, online: online,
                 os: platform ?? "", appID: appId)
    }
}

enum PeerListMerge {
    /// The management API knows things the tunnel does not (display names,
    /// labels, routes, disabled state), so its entries win. Nodes only the
    /// tunnel knows about are appended, and the tunnel's list stands alone when
    /// the API has nothing (not logged in).
    static func merged(api: [PeerNode], tunnel: [PeerNode]) -> [PeerNode] {
        guard !api.isEmpty else { return tunnel }
        let known = Set(api.flatMap { [$0.appID, $0.name] }.filter { !$0.isEmpty })
        return api + tunnel.filter { !known.contains($0.appID) && !known.contains($0.name) }
    }
}

/// One peer's merged connection metrics from the tunnel process (engine
/// pollPeerStates payload). Absent fields = unknown. Also decodes the older
/// extension payload shape where the value was just the state string.
struct PeerStat: Codable {
    var state: String?
    var rx: UInt64?
    var tx: UInt64?
    var handshakeAgo: Int64?
    var rtt: Int64?

    private enum CodingKeys: String, CodingKey {
        case state, rx, tx, handshakeAgo, rtt
    }

    init(state: String? = nil, rx: UInt64? = nil, tx: UInt64? = nil,
         handshakeAgo: Int64? = nil, rtt: Int64? = nil) {
        self.state = state
        self.rx = rx
        self.tx = tx
        self.handshakeAgo = handshakeAgo
        self.rtt = rtt
    }

    init(from decoder: Decoder) throws {
        if let legacy = try? decoder.singleValueContainer().decode(String.self) {
            state = legacy
            return
        }
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = try c.decodeIfPresent(String.self, forKey: .state)
        rx = try c.decodeIfPresent(UInt64.self, forKey: .rx)
        tx = try c.decodeIfPresent(UInt64.self, forKey: .tx)
        handshakeAgo = try c.decodeIfPresent(Int64.self, forKey: .handshakeAgo)
        rtt = try c.decodeIfPresent(Int64.self, forKey: .rtt)
    }
}


// MARK: - Management login

/// Secrets live in the Keychain; the plain store is where older builds kept the
/// management token.
protocol SecretStoring {
    func secret(_ key: String) -> String?
    func setSecret(_ value: String, forKey key: String)
    func deleteSecret(_ key: String)
}

protocol PlainStoring {
    func string(forKey key: String) -> String?
    func removeObject(forKey key: String)
}

extension UserDefaults: PlainStoring {}

/// The management-API token. Older builds kept it in UserDefaults in plain text;
/// the first read moves it into the secret store.
struct AuthTokenStore {
    static let key = "lattice.authToken"

    let secrets: SecretStoring
    let legacy: PlainStoring

    func read() -> String {
        if let token = secrets.secret(Self.key), !token.isEmpty {
            legacy.removeObject(forKey: Self.key)
            return token
        }
        guard let old = legacy.string(forKey: Self.key), !old.isEmpty else { return "" }
        secrets.setSecret(old, forKey: Self.key)
        // Drop the plain copy only once the secret store really holds it.
        if secrets.secret(Self.key) == old {
            legacy.removeObject(forKey: Self.key)
        }
        return old
    }

    func write(_ token: String) {
        secrets.setSecret(token, forKey: Self.key)
        legacy.removeObject(forKey: Self.key)
    }

    func clear() {
        secrets.deleteSecret(Self.key)
        legacy.removeObject(forKey: Self.key)
    }
}

/// Coordinates "log in when a management action needs it". An action that finds
/// no login awaits `requestLogin()`; the UI presents the login sheet while
/// `isPresenting` is true and reports the outcome through `finish`. Every
/// action waiting at that moment resumes with the same result, so the action
/// the user asked for carries on after a successful login.
@MainActor
final class LoginCoordinator: ObservableObject {
    static let shared = LoginCoordinator()

    @Published private(set) var isPresenting = false
    private var waiters: [CheckedContinuation<Bool, Never>] = []

    func requestLogin() async -> Bool {
        isPresenting = true
        return await withCheckedContinuation { waiters.append($0) }
    }

    func finish(success: Bool) {
        isPresenting = false
        let pending = waiters
        waiters = []
        for waiter in pending { waiter.resume(returning: success) }
    }
}
