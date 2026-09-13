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

import NetworkExtension
import LatticeCore

/// Runs the Lattice Go engine (WireGuard + signaling + netmap convergence)
/// inside the Network Extension, bridging packets between NEPacketFlow and
/// the engine via the gomobile-generated LatticeEngine bindings.
class PacketTunnelProvider: NEPacketTunnelProvider {
    private var engine: LatticeEngineEngine?
    private var pendingStart: ((Error?) -> Void)?
    private var pumping = false

    override func startTunnel(
        options: [String: NSObject]?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        guard let pc = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration,
              let serverURL = pc["serverURL"] as? String,
              let token = pc["token"] as? String else {
            completionHandler(NSError(
                domain: "io.lattice.tunnel",
                code: 1,
                userInfo: [NSLocalizedDescriptionKey: "缺少 serverURL 或 token 配置"]
            ))
            return
        }
        let name = (pc["name"] as? String) ?? (Host.current().localizedName ?? "lattice-mac")
        let config = EngineConfig(serverURL: serverURL, token: token, name: name, mtu: 1280)

        do {
            engine = try LatticeEngineEngine(config.jsonString, delegate: self)
        } catch {
            completionHandler(error)
            return
        }
        pendingStart = completionHandler
        do {
            try engine?.start()
        } catch {
            pendingStart = nil
            completionHandler(error)
        }
    }

    override func stopTunnel(
        with reason: NEProviderStopReason,
        completionHandler: @escaping () -> Void
    ) {
        pumping = false
        do {
            try engine?.stop()
        } catch {
            // Best-effort teardown; the process exits after stop returns.
        }
        engine = nil
        completionHandler()
    }

    // MARK: - Packet pump (system → tunnel)

    /// Continuously reads packets the macOS stack routes into the tunnel and
    /// hands them to the engine for encryption.
    private func pumpPackets() {
        guard !pumping else { return }
        pumping = true
        packetFlow.readPackets { [weak self] packets, _ in
            guard let self, self.pumping else { return }
            for packet in packets {
                try? self.engine?.sendPacket(packet)
            }
            self.pumping = false
            self.pumpPackets()
        }
    }

    // MARK: - Network settings

    private func makeSettings(overlayIP: String) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        // Route the overlay range into the tunnel. No default route: Lattice
        // joins a mesh, it does not replace the uplink.
        ipv4.includedRoutes = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        settings.ipv4Settings = ipv4
        return settings
    }
}

extension PacketTunnelProvider: LatticeEngineEngineDelegateProtocol {
    /// Engine → system: decrypted packets bound for the overlay.
    func deliverPacket(_ packet: Data!) throws {
        packetFlow.writePackets([packet], withProtocols: [NSNumber(value: AF_INET)])
    }

    func onEvent(_ event: String!) {
        NSLog("[Lattice] engine event: \(event ?? "")")
    }

    /// Registration finished and an overlay IP was assigned: install the
    /// network settings, then let the system proceed with the tunnel.
    func onTunnelUp(_ overlayIP: String!) {
        NSLog("[Lattice] tunnel up, overlay IP \(overlayIP ?? "?")")
        setTunnelNetworkSettings(makeSettings(overlayIP: overlayIP ?? "10.96.0.1")) { [weak self] error in
            guard let self else { return }
            self.pendingStart?(error)
            self.pendingStart = nil
            if error == nil {
                self.pumpPackets()
            }
        }
    }
}

/// JSON payload for the engine config (see apple/engine/engine.go).
private struct EngineConfig: Encodable {
    let serverURL: String
    let token: String
    let name: String
    let mtu: Int

    var jsonString: String {
        if let data = try? JSONEncoder().encode(self),
           let json = String(data: data, encoding: .utf8) {
            return json
        }
        return "{}"
    }
}
