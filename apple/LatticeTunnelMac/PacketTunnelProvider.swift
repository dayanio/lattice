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

/// Appends diagnostics to a file in the extension's sandbox — NSLog does not
/// surface in the unified log from this process, which blinded debugging.
enum TunnelLog {
    static func write(_ message: String) {
        let dir = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
        guard let path = dir?.appendingPathComponent("lattice-tunnel.log") else { return }
        let stamp = DateFormatter.localizedString(from: Date(), dateStyle: .none, timeStyle: .medium)
        let line = "[\(stamp)] \(message)\n"
        if let handle = try? FileHandle(forWritingTo: path) {
            defer { try? handle.close() }
            _ = try? handle.seekToEnd()
            handle.write(line.data(using: .utf8)!)
        } else {
            try? line.data(using: .utf8)!.write(to: path)
        }
    }
}

/// Runs the Lattice Go engine (WireGuard + signaling + netmap convergence)
/// inside the Network Extension, bridging packets between NEPacketFlow and
/// the engine via the gomobile-generated LatticeEngine bindings.
class PacketTunnelProvider: NEPacketTunnelProvider {
    private var engine: LatticeEngineEngine?
    private var pendingStart: ((Error?) -> Void)?
    private var pumping = false
    /// Latest per-peer connection-quality snapshot, served to the containing
    /// app via handleAppMessage (the app cannot read engine state directly).
    private var latestPeerStates = "{}"
    /// Last fatal engine error — surfaced to the app over handleAppMessage.
    private var latestError = ""
    /// Latest extra-routes snapshot from the engine (JSON array of CIDRs),
    /// applied as NEIPv4Routes once the tunnel is up. Empty until the first
    /// OnRoutesChanged call.
    private var latestExtraRoutes: [String] = []
    private var currentOverlayIP = "10.96.0.1"

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        if String(data: messageData, encoding: .utf8) == "peerStates" {
            // latestPeerStates 本身是 map 的 JSON 字符串——先解成对象再装进
            // 信封，避免把整个 map 当字符串二次编码（App 端会解码失败）。
            let states = (try? JSONSerialization.jsonObject(with: Data(latestPeerStates.utf8))) as? [String: String] ?? [:]
            let snapshot: [String: Any] = [
                "peerStates": states,
                "lastError": latestError,
                "publicKey": engine?.publicKey() ?? "",
                "overlayIP": currentOverlayIP,
            ]
            completionHandler?(try? JSONSerialization.data(withJSONObject: snapshot))
            return
        }
        completionHandler?(nil)
    }

    override func startTunnel(
        options: [String: NSObject]?,
        completionHandler: @escaping (Error?) -> Void
    ) {
        guard let pc = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration,
              let serverURL = pc["serverURL"] as? String,
              let token = pc["token"] as? String else {
            NSLog("[Lattice] startTunnel: missing serverURL/token in provider config")
            completionHandler(NSError(
                domain: "io.lattice.tunnel",
                code: 1,
                userInfo: [NSLocalizedDescriptionKey: "缺少 serverURL 或 token 配置"]
            ))
            return
        }
        if pc["resetIdentity"] as? Bool == true {
            if !LatticeEngineResetIdentity(nil) {
                TunnelLog.write("startTunnel: resetIdentity failed")
            }
        }
        let name = (pc["name"] as? String) ?? (Host.current().localizedName ?? "lattice-mac")
        TunnelLog.write("startTunnel: server=\(serverURL) token=\(token.count) chars name=\(name)")
        let config = EngineConfig(serverURL: serverURL, token: token, name: name, mtu: 1280)

        do {
            engine = try LatticeEngineEngine(config.jsonString, delegate: self)
            TunnelLog.write("engine created")
        } catch {
            NSLog("[Lattice] engine create FAILED: \(error)")
            completionHandler(error)
            return
        }
        pendingStart = completionHandler
        do {
            try engine?.start()
            TunnelLog.write("engine start() returned")
        } catch {
            TunnelLog.write("engine start FAILED: \(error)")
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

    private func makeSettings(overlayIP: String, extraRoutes: [String]) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        // LatticeDNS: 只有 *.lattice 的 DNS 查询进隧道（由引擎内置应答器解析），
        // 其余域名的解析走系统默认 DNS。
        let dns = NEDNSSettings(servers: ["10.96.0.1"])
        dns.matchDomains = ["lattice"]
        settings.dnsSettings = dns

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        // Route the overlay range into the tunnel always. No default route
        // unless a selected Exit Node advertises 0.0.0.0/0 (handled below):
        // Lattice joins a mesh, it does not replace the uplink by default.
        var included = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        var excluded: [NEIPv4Route] = []

        for cidr in extraRoutes {
            guard let route = Self.ipv4Route(fromCIDR: cidr) else { continue }
            included.append(route)
        }

        // Exit Node (0.0.0.0/0): exclude this device's own control-plane
        // server from the tunnel, or every packet talking to it would loop
        // back through the tunnel it's trying to keep alive. The LRP relay
        // address isn't known on the Swift side yet — if traffic to it also
        // needs excluding, that's a follow-up once this is verified against
        // a real Exit Node (see the design doc's open item on this).
        if extraRoutes.contains("0.0.0.0/0"),
           let serverURL = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration?["serverURL"] as? String,
           let host = URL(string: serverURL)?.host,
           let hostIP = Self.ipv4Route(fromCIDR: "\(host)/32") {
            excluded.append(hostIP)
        }

        ipv4.includedRoutes = included
        ipv4.excludedRoutes = excluded.isEmpty ? nil : excluded
        settings.ipv4Settings = ipv4
        return settings
    }

    /// Parses "a.b.c.d/n" into an NEIPv4Route. Returns nil for anything that
    /// isn't a plain dotted-quad CIDR (defense in depth — the engine already
    /// validates on the server side, but this is the last line before an OS
    /// API call that would otherwise silently no-op on a bad string).
    private static func ipv4Route(fromCIDR cidr: String) -> NEIPv4Route? {
        let parts = cidr.split(separator: "/")
        guard parts.count == 2, let prefixLen = UInt8(parts[1]), prefixLen <= 32 else { return nil }
        let address = String(parts[0])
        let mask = prefixLen == 0 ? "0.0.0.0" : ipv4SubnetMask(prefixLength: prefixLen)
        return NEIPv4Route(destinationAddress: address, subnetMask: mask)
    }

    private static func ipv4SubnetMask(prefixLength: UInt8) -> String {
        let mask: UInt32 = prefixLength == 0 ? 0 : ~UInt32(0) << (32 - prefixLength)
        return [24, 16, 8, 0].map { String((mask >> $0) & 0xFF) }.joined(separator: ".")
    }
}

extension PacketTunnelProvider: LatticeEngineEngineDelegateProtocol {
    /// Engine → system: decrypted packets bound for the overlay.
    func deliverPacket(_ packet: Data!) throws {
        packetFlow.writePackets([packet], withProtocols: [NSNumber(value: AF_INET)])
    }

    func onEvent(_ event: String!) {
        TunnelLog.write("engine event: \(event ?? "")")
        guard let event, event.hasPrefix("error: ") else { return }
        let message = String(event.dropFirst("error: ".count))
        latestError = message
        if let pendingStart {
            // Failure during start: surface the reason to NE (and thus to the
            // containing app) as a failed start.
            self.pendingStart = nil
            pendingStart(NSError(
                domain: "io.lattice.tunnel",
                code: 2,
                userInfo: [NSLocalizedDescriptionKey: message]
            ))
            return
        }
        // Failure AFTER start completed: the engine is dead but NE still
        // considers the tunnel up. Tear the session down so the panel shows
        // 未连接 and the next connect tap spawns a fresh engine instead of
        // silently no-oping against a zombie provider.
        TunnelLog.write("engine failed post-start, tearing down: \(message)")
        // macOS NEProvider has no cancelTunnel; exiting the extension marks
        // the session down in NE, and the next connect from the panel spawns
        // a fresh engine. The engine already stopped its NATS drain by now.
        exit(0)
    }

    /// Registration finished and an overlay IP was assigned: install the
    /// network settings, then let the system proceed with the tunnel.
    func onTunnelUp(_ overlayIP: String!) {
        TunnelLog.write("tunnel up, overlay IP \(overlayIP ?? "?")")
        currentOverlayIP = overlayIP ?? "10.96.0.1"
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: latestExtraRoutes)) { [weak self] error in
            guard let self else { return }
            self.pendingStart?(error)
            self.pendingStart = nil
            if error == nil {
                self.pumpPackets()
            }
        }
    }

    /// Per-peer connection-quality snapshot changed (JSON: name → state).
    func onPeerStates(_ statesJSON: String!) {
        latestPeerStates = statesJSON ?? "{}"
        TunnelLog.write("peer states: \(latestPeerStates)")
    }

    /// Extra CIDRs to route into the tunnel changed — reapply network
    /// settings if the tunnel is already up (first call, at startup, is a
    /// no-op here since onTunnelUp installs settings itself right after).
    func onRoutesChanged(_ routesJSON: String!) {
        guard let data = routesJSON?.data(using: .utf8),
              let routes = try? JSONDecoder().decode([String].self, from: data) else { return }
        latestExtraRoutes = routes
        guard pendingStart == nil else { return } // still starting up — onTunnelUp will apply this set
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: routes), completionHandler: nil)
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
