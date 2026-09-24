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
        var config = EngineConfig(serverURL: serverURL, token: token, name: name, mtu: 1280)
        // ADR-0007 的升级重试是 break-before-make：直连在本机网络环境可用前，
        // 每次重试只会拆掉正常工作的中继会话制造断网窗口，先关掉。
        config.disableUpgrade = true
        // macOS NE 无自动自豁免：provider 自己的 WG/ICE UDP 与中继 TCP 会被
        // 截进本隧道。此刻隧道路由尚未生效，route get 拿到的就是物理出口
        // 网卡，交给引擎做 IP_BOUND_IF 绑定。
        let bindIface = Self.physicalInterface(for: URL(string: serverURL)?.host ?? "")
        config.bindInterface = bindIface
        TunnelLog.write("bind interface: \(bindIface)")

        do {
            engine = try LatticeEngineEngine(config.jsonString, delegate: self)
            TunnelLog.write("engine created")
        } catch {
            NSLog("[Lattice] engine create FAILED: \(error)")
            completionHandler(error)
            return
        }
        pendingStart = completionHandler
        // 出口 DNS 上游：此刻系统 DNS 尚未被隧道接管，/etc/resolv.conf 还是
        // 物理解析器。显式交给引擎——否则引擎懒解析时隧道 DNS 已生效，上游
        // 变成 10.96.0.1 自环，表现为"隧道通但一切域名都不解析"。
        let upstream = Self.physicalDNSServers()
        engine?.setUpstreamDNS(upstream.joined(separator: ","))
        TunnelLog.write("upstream DNS: \(upstream.joined(separator: ", "))")
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

    // 出口全局路由（0.0.0.0/0 → /1 拆分）直接进 NE includedRoutes：
    // NE 自己维护这些路由且 /1 比物理 default 更具体必胜。曾经的
    // route(8) 手动方案被系统路由 reassertion 在数秒内清除，已废弃。

    private func makeSettings(overlayIP: String, extraRoutes: [String]) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        // LatticeDNS: 只有 *.lattice 的 DNS 查询进隧道（由引擎内置应答器解析），
        // 其余域名的解析走系统默认 DNS。
        let dns = NEDNSSettings(servers: ["10.96.0.1"])
        if extraRoutes.contains("0.0.0.0/0") {
            // 出口模式：全部 DNS 经隧道由出口侧解析；同时本机作为提供方
            // 需要系统级 IP 转发（把网内流量转出公网，root 下可设）。
            dns.matchDomains = nil
            enableIPForwarding()
        }
        dns.searchDomains = ["lattice"] // 短名 node-a 自动补全为 node-a.lattice
        settings.dnsSettings = dns

        // IPv6 接管暂不启用：v6 黑洞的前提是 IPv4 0/0 已经把流量接管进隧道
        // 走出口（否则黑洞只是单纯掐断 v6，没有出口兜底）。上面的 IPv4 0/0
        // 捕获因出口数据面尚未达到生产稳定性而推迟，这里必须同步推迟，
        // 否则无论是否选择出口节点，每次连接都会无条件丢弃设备的 v6 流量。

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        // Route the overlay range into the tunnel always. No default route
        // unless a selected Exit Node advertises 0.0.0.0/0 (handled below):
        // Lattice joins a mesh, it does not replace the uplink by default.
        var included = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        var excluded: [NEIPv4Route] = []

        for cidr in extraRoutes {
            // Exit Node (0.0.0.0/0)：单个 /0 的 included 路由会被 NE 降级
            // scoped、抢不过物理默认，route(8) 手动补的 /1 又会被系统
            // reassertion 秒删；WireGuard 同款做法是把 /1 拆分直接交给
            // NE includedRoutes —— NE 自己维护、/1 比 default 更具体必胜。
            if cidr == "0.0.0.0/0" {
                included.append(NEIPv4Route(destinationAddress: "0.0.0.0", subnetMask: "128.0.0.0"))
                included.append(NEIPv4Route(destinationAddress: "128.0.0.0", subnetMask: "128.0.0.0"))
                continue
            }
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
    /// 出口节点转发需要系统级 IP 转发（把网内流量转出公网）。幂等：重复
    /// 设置无害。NE 扩展以 root 运行，具备设置权限。
    /// 出口模式下非 lattice 域名的上游解析器：startTunnel 时（隧道 DNS 设置
    /// 应用前）从 /etc/resolv.conf 抓物理解析器，跳过隧道自身的 10.96.0.1。
    /// 抓不到时退回公共 DNS——空列表会让引擎走 resolv.conf 懒解析而自环。
    private static func physicalDNSServers() -> [String] {
        guard let data = FileManager.default.contents(atPath: "/etc/resolv.conf"),
              let text = String(data: data, encoding: .utf8) else {
            return ["223.5.5.5", "119.29.29.29"]
        }
        var out: [String] = []
        for line in text.components(separatedBy: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard trimmed.hasPrefix("nameserver "), out.count < 3 else { continue }
            let ns = trimmed.dropFirst("nameserver ".count).trimmingCharacters(in: .whitespaces)
            if !ns.isEmpty, ns != "10.96.0.1" { out.append(ns) }
        }
        return out.isEmpty ? ["223.5.5.5", "119.29.29.29"] : out
    }

    /// 隧道设置生效前查询到服务器地址的出口网卡（macOS 的物理 uplink）。
    private static func physicalInterface(for host: String) -> String {
        guard !host.isEmpty else { return "en0" }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/sbin/route")
        p.arguments = ["-n", "get", host]
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = Pipe()
        do { try p.run() } catch { return "en0" }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        guard let out = String(data: data, encoding: .utf8) else { return "en0" }
        for line in out.components(separatedBy: "\n") where line.contains("interface:") {
            let ifc = line.components(separatedBy: ":").last?.trimmingCharacters(in: .whitespaces) ?? ""
            if ifc.hasPrefix("en") { return ifc }
        }
        return "en0"
    }

    private func enableIPForwarding() {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: "/usr/sbin/sysctl")
        proc.arguments = ["-w", "net.inet.ip.forwarding=1"]
        proc.standardOutput = Pipe()
        proc.standardError = Pipe()
        do { try proc.run() } catch { NSLog("[Lattice] enable forwarding failed: \(error)") }
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
    /// Engine → system: decrypted packets bound for the overlay.
    func deliverPacket(_ packet: Data!) throws {
        packetFlow.writePackets([packet], withProtocols: [NSNumber(value: AF_INET)])
    }

    func onEvent(_ event: String!) {
        TunnelLog.write("engine event: \(event ?? "")")
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

    /// Per-peer connection-quality snapshot changed (JSON: name → metrics).
    /// Logged only in outline — this fires every couple of seconds.
    func onPeerStates(_ statesJSON: String!) {
        latestPeerStates = statesJSON ?? "{}"
    }

    /// Extra CIDRs to route into the tunnel changed — reapply network
    /// settings if the tunnel is already up (first call, at startup, is a
    /// no-op here since onTunnelUp installs settings itself right after).
    func onRoutesChanged(_ routesJSON: String!) {
        guard let data = routesJSON?.data(using: .utf8),
              let routes = try? JSONDecoder().decode([String].self, from: data) else { return }
        latestExtraRoutes = routes
        guard pendingStart == nil else { return } // still starting up — onTunnelUp will apply this set
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: routes)) { [weak self] error in
            guard let self, error == nil else { return }
        }
    }
}

/// JSON payload for the engine config (see apple/engine/engine.go).
private struct EngineConfig: Encodable {
    let serverURL: String
    let token: String
    let name: String
    let mtu: Int
    var disableUpgrade: Bool = false
    var bindInterface: String = ""

    var jsonString: String {
        if let data = try? JSONEncoder().encode(self),
           let json = String(data: data, encoding: .utf8) {
            return json
        }
        return "{}"
    }
}
