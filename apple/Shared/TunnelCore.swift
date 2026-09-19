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
    var lastSeen: String = ""
    /// True when an AgentIdentity references this peer — AI agents are
    /// first-class network citizens and get a badge (UI mockup §04).
    var isAgent: Bool = false
    /// gVisor sandbox state from the AgentIdentity ("none" | "gvisor" | ...).
    var sandbox: String? = nil

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
