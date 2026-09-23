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
import UIKit
import LatticeCore

/// iOS PacketTunnelProvider: runs the same Lattice Go engine as the macOS
/// client inside the packet-tunnel extension. Mirrors
/// LatticeTunnelMac/PacketTunnelProvider.swift; only the default device
/// name and bundle id differ.
class PacketTunnelProvider: NEPacketTunnelProvider {
    private var engine: LatticeEngineEngine?
    private var pendingStart: ((Error?) -> Void)?
    private var pumping = false
    /// Latest per-peer connection-quality snapshot, served to the containing
    /// app via handleAppMessage (the app cannot read engine state directly).
    private var latestPeerStates = "{}"
    /// Last fatal engine error ("error: " events). Surfaced to the containing
    /// app over the handleAppMessage channel so the UI can show WHY the
    /// tunnel is not connected instead of failing silently.
    private var latestError = ""
    /// "awaiting-approval" while the workspace holds this device for an
    /// administrator; empty otherwise. Served to the app with the peer states.
    private var latestPhase = ""
    /// Latest extra-routes snapshot from the engine (JSON array of CIDRs),
    /// applied as NEIPv4Routes once the tunnel is up. Empty until the first
    /// OnRoutesChanged call.
    private var latestExtraRoutes: [String] = []
    private var currentOverlayIP = "10.96.0.1"

    override func handleAppMessage(_ messageData: Data, completionHandler: ((Data?) -> Void)?) {
        if String(data: messageData, encoding: .utf8) == "peerStates" {
            // latestPeerStates 是 name→metrics 对象的 JSON——原样解成对象塞进
            // 信封（字段由引擎定义，这里不感知具体结构）。
            let states = (try? JSONSerialization.jsonObject(with: Data(latestPeerStates.utf8))) as? [String: Any] ?? [:]
            let peers = (try? JSONSerialization.jsonObject(with: Data((engine?.peers() ?? "[]").utf8))) as? [[String: Any]] ?? []
            let snapshot: [String: Any] = [
                "peerStates": states,
                "lastError": latestError,
                "publicKey": engine?.publicKey() ?? "",
                "overlayIP": currentOverlayIP,
                "peers": peers,
                "phase": latestPhase,
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
            completionHandler(NSError(
                domain: "io.lattice.tunnel",
                code: 1,
                userInfo: [NSLocalizedDescriptionKey: "缺少 serverURL 或 token 配置"]
            ))
            return
        }
        if pc["resetIdentity"] as? Bool == true {
            _ = LatticeEngineResetIdentity(nil)
        }
        let name = (pc["name"] as? String) ?? (UIDevice.current.name)
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

    private func makeSettings(overlayIP: String, extraRoutes: [String]) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        // LatticeDNS: 只有 *.lattice 的 DNS 查询进隧道（由引擎内置应答器解析），
        // 其余域名的解析走系统默认 DNS。10.96.0.1 是 overlay 内的保留未分配
        // 地址，发往它的 DNS 包经 TUN 进入引擎即被 LatticeDNS 拦截应答。
        let dns = NEDNSSettings(servers: ["10.96.0.1"])
        if extraRoutes.contains("0.0.0.0/0") {
            // 出口模式（UI 已选择出口节点）：全部 DNS 经隧道由出口侧解析，
            // 否则 google.com 等域名会被本机 DNS 污染，出口代理形同虚设。
            dns.matchDomains = nil
        } else {
            dns.matchDomains = ["lattice"]
        }
        dns.searchDomains = ["lattice"] // 短名 node-a 自动补全为 node-a.lattice
        settings.dnsSettings = dns

        // IPv6 接管暂不启用：v6 黑洞的前提是 IPv4 0/0 已经把流量接管进隧道
        // 走出口（否则黑洞只是单纯掐断 v6，没有出口兜底）。上面的 IPv4 0/0
        // 捕获因出口数据面尚未达到生产稳定性而推迟，这里必须同步推迟，
        // 否则无论是否选择出口节点，每次连接都会无条件丢弃设备的 v6 流量。

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        var included = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        var excluded: [NEIPv4Route] = []

        for cidr in extraRoutes {
            // 0.0.0.0/0 用 /1 拆分进 includedRoutes（WireGuard 同款）：
            // 比物理 default 更具体必胜，且由 NE 托管——隧道关闭时系统
            // 原子恢复原路由表，不会残留半套路由把设备网络搞挂。
            if cidr == "0.0.0.0/0" {
                included.append(NEIPv4Route(destinationAddress: "0.0.0.0", subnetMask: "128.0.0.0"))
                included.append(NEIPv4Route(destinationAddress: "128.0.0.0", subnetMask: "128.0.0.0"))
                continue
            }
            guard let route = Self.ipv4Route(fromCIDR: cidr) else { continue }
            included.append(route)
        }

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
    func deliverPacket(_ packet: Data!) throws {
        packetFlow.writePackets([packet], withProtocols: [NSNumber(value: AF_INET)])
    }

    func onEvent(_ event: String!) {
        NSLog("[Lattice] engine event: \(event ?? "")")
        if event == "awaiting-approval" {
            latestPhase = "awaiting-approval"
            return
        }
        if event == "connected" || event == "disconnected" || event?.hasPrefix("error: ") == true {
            latestPhase = ""
        }
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
        // considers the tunnel up. Tear the session down so the UI shows
        // 未连接 and the next connect tap spawns a fresh engine instead of
        // silently no-oping against a zombie provider.
        NSLog("[Lattice] engine failed post-start, tearing down: \(message)")
        // NEPacketTunnelProvider exposes no callable cancelTunnel in this SDK;
        // exiting the extension marks the session down in NE, and the next
        // connect tap spawns a fresh engine instead of a zombie session.
        exit(0)
    }

    func onTunnelUp(_ overlayIP: String!) {
        NSLog("[Lattice] tunnel up, overlay IP \(overlayIP ?? "?")")
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

    func onPeerStates(_ statesJSON: String!) {
        latestPeerStates = statesJSON ?? "{}"
    }

    func onRoutesChanged(_ routesJSON: String!) {
        guard let data = routesJSON?.data(using: .utf8),
              let routes = try? JSONDecoder().decode([String].self, from: data) else { return }
        latestExtraRoutes = routes
        guard pendingStart == nil else { return }
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: routes), completionHandler: nil)
    }
}

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
