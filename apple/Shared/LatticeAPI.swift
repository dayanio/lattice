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

final class LatticeAPI {
    static let shared = LatticeAPI()

    private var baseURL: String {
        UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://127.0.0.1:8080"
    }

    private let tokenStore = AuthTokenStore.standard

    private var token: String { tokenStore.read() }

    func listPeers() async throws -> [PeerNode] {
        try await resolveWorkspaceIfNeeded()
        do {
            return try await fetchPeers()
        } catch {
            // The cached workspace id may point at a workspace the server no
            // longer knows about (e.g. backend data was reset). Re-resolve
            // once instead of failing forever on a stale id.
            UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
            try await resolveWorkspaceIfNeeded()
            return try await fetchPeers()
        }
    }

    private func fetchPeers() async throws -> [PeerNode] {
        let data = try await request(method: "GET", path: "/api/v1/peers/list?page=1&pageSize=50")
        let decoded = try JSONDecoder().decode(PeerListResponse.self, from: data)
        guard let list = decoded.data?.list else { return [] }
        return list.map { p in
            let name = (p.name?.isEmpty == false) ? p.name! : (p.appId ?? p.address)
            return PeerNode(
                name: name,
                address: p.address,
                online: p.status == "online",
                displayName: p.displayName ?? "",
                disabled: p.disabled ?? false,
                appID: p.appId ?? "",
                labels: p.labels,
                advertisedRoutes: p.advertisedRoutes ?? [],
                lastSeen: p.lastSeen ?? "",
                approvalStatus: p.approvalStatus,
                publicKey: p.publicKey ?? ""
            )
        }
    }

    /// Renames a peer (display name only — the peer's WG identity never changes).
    func renamePeer(_ name: String, displayName: String) async throws {
        try await request(method: "PUT", path: "/api/v1/peers/update",
                          body: ["name": name, "displayName": displayName])
    }

    // Pins a peer's real reachable address (IP:port), bypassing automatic
    // discovery for topologies where it fails (e.g. a NATed container with
    // a published port). An empty string clears the pin, reverting to
    // automatic discovery.
    func setPeerEndpoint(_ name: String, endpoint: String) async throws {
        try await request(method: "PUT", path: "/api/v1/peers/update",
                          body: ["name": name, "endpoint": endpoint])
    }

    func setPeerDisabled(_ name: String, _ disabled: Bool) async throws {
        let action = disabled ? "disable" : "enable"
        try await request(method: "PUT", path: "/api/v1/peers/\(encodePath(name))/\(action)")
    }

    func deletePeer(_ name: String) async throws {
        try await request(method: "DELETE", path: "/api/v1/peers/\(encodePath(name))")
    }

    /// Approves ("approved") or rejects ("revoked") a pending enrollment
    /// (ADR-0003). Requires a workspace-admin login.
    func setPeerApproval(_ name: String, approved: Bool) async throws {
        try await request(method: "PUT",
                          path: "/api/v1/peers/\(encodePath(name))/approval",
                          body: ["status": approved ? "approved" : "revoked"])
    }

    // MARK: Account & workspace (账号与网络页)

    struct MeInfo: Codable {
        let username: String?
        let systemRole: String?
    }

    /// The logged-in account. Empty strings when the token is invalid.
    func fetchMe() async throws -> MeInfo {
        let data = try await request(method: "GET", path: "/api/v1/users/getme")
        struct Response: Codable { let data: MeInfo? }
        return try JSONDecoder().decode(Response.self, from: data).data ?? MeInfo(username: nil, systemRole: nil)
    }

    func listWorkspaces() async throws -> [WorkspaceItem] {
        let data = try await request(method: "GET", path: "/api/v1/workspaces/list")
        let decoded = try JSONDecoder().decode(WorkspaceListResponse.self, from: data)
        return decoded.data?.list ?? []
    }

    /// Declares (or clears, if `routes` is empty) the CIDRs `name` offers to
    /// route for other peers in the workspace.
    func setAdvertisedRoutes(_ name: String, routes: [String]) async throws {
        try await request(method: "POST", path: "/api/v1/peers/\(encodePath(name))/advertised-routes",
                          body: ["routes": routes])
    }

    /// `consumer` opts in (selected: true) or out of `provider`'s advertised routes.
    func setRouteSelection(consumer: String, provider: String, selected: Bool) async throws {
        try await request(method: "POST", path: "/api/v1/peers/\(encodePath(consumer))/route-selection",
                          body: ["provider": provider, "selected": selected])
    }

    /// Provider names `consumer` currently has selected.
    func listRouteSelections(_ consumer: String) async throws -> [String] {
        let data = try await request(method: "GET", path: "/api/v1/peers/\(encodePath(consumer))/route-selection")
        struct Response: Codable { let data: [String]? }
        return try JSONDecoder().decode(Response.self, from: data).data ?? []
    }

    // MARK: 对外发布 (publish gateway, v1)

    func listPublishes() async throws -> [PublishItem] {
        let data = try await request(method: "GET", path: "/api/v1/publish/list")
        struct Response: Codable { let data: [PublishItem]? }
        return try JSONDecoder().decode(Response.self, from: data).data ?? []
    }

    func createPublish(name: String, peer: String, port: Int) async throws {
        try await request(method: "POST", path: "/api/v1/publish",
                          body: ["name": name, "peerName": peer, "port": port])
    }

    func deletePublish(_ name: String) async throws {
        try await request(method: "DELETE", path: "/api/v1/publish/\(encodePath(name))")
    }

    // MARK: Policies (ACL view)

    func listPolicies() async throws -> [LatticePolicy] {
        let data = try await request(method: "GET", path: "/api/v1/policies/list")
        let decoded = try JSONDecoder().decode(PolicyListResponse.self, from: data)
        return decoded.data?.list ?? []
    }

    // MARK: Intent AI (natural-language policy)

    func planIntent(_ text: String) async throws -> IntentPlanView {
        let data = try await request(
            method: "POST",
            path: "/api/v1/ai/intent/plan",
            body: ["workspaceId": workspaceID, "intent": text, "dryRun": true]
        )
        guard let plan = try JSONDecoder().decode(IntentPlanResponse.self, from: data).data else {
            throw LatticeAPIError.server("AI 未返回策略方案")
        }
        return plan
    }

    func applyIntent(planID: String) async throws {
        try await request(
            method: "POST",
            path: "/api/v1/ai/intent/apply",
            body: ["planId": planID]
        )
    }

    // MARK: Agent identities (AI badge)

    func listAgentIdentities() async throws -> [AgentIdentityVO] {
        let data = try await request(method: "GET", path: "/api/v1/agent-identities")
        let decoded = try JSONDecoder().decode(AgentIdentityListResponse.self, from: data)
        return decoded.data ?? []
    }

    private func encodePath(_ value: String) -> String {
        value.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? value
    }

    // MARK: AI chat (SSE stream; the control plane executes MCP tools)

    /// Streams one chat turn from POST /api/v1/ai/chat. Events are delivered
    /// on the main queue. The control plane runs the LLM with MCP tools, so
    /// the assistant can actually operate the network — the client only
    /// renders the stream.
    func streamChat(
        message: String,
        history: [ChatAPIMessage],
        onEvent: @escaping (ChatEvent) -> Void
    ) async throws {
        guard await ensureLoggedIn() else { throw LatticeAPIError.server("已取消登录") }
        guard let url = URL(string: baseURL + "/api/v1/ai/chat") else { throw URLError(.badURL) }
        var req = URLRequest(url: url, timeoutInterval: 180)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        if !workspaceID.isEmpty {
            req.setValue(workspaceID, forHTTPHeaderField: "X-Workspace-Id")
        }
        if !token.isEmpty {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        let body: [String: Any] = [
            "message": message,
            "workspaceId": workspaceID,
            "history": history.map { ["role": $0.role, "content": $0.content] },
        ]
        req.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (bytes, response) = try await URLSession.shared.bytes(for: req)
        if let http = response as? HTTPURLResponse, http.statusCode != 200 {
            var data = Data()
            for try await b in bytes { data.append(b) }
            throw LatticeAPIError.server(Self.serverMessage(from: data, fallback: "HTTP \(http.statusCode)"))
        }

        for try await line in bytes.lines {
            guard line.hasPrefix("data:") else { continue }
            let payload = line.dropFirst(5).trimmingCharacters(in: .whitespaces)
            guard let payloadData = payload.data(using: .utf8),
                  let event = try? JSONDecoder().decode(ChatStreamEvent.self, from: payloadData) else { continue }

            let chatEvent: ChatEvent
            switch event.type {
            case "token":
                chatEvent = .token(event.content ?? "")
            case "tool_use":
                chatEvent = .toolUse(event.tool ?? "tool")
            case "error":
                chatEvent = .error(event.error ?? "未知错误")
            case "done":
                chatEvent = .done
            default:
                continue
            }
            let captured = chatEvent
            await MainActor.run { onEvent(captured) }
        }
    }

    func login(user: String, pass: String) async throws {
        guard let url = URL(string: baseURL + "/api/v1/users/login") else { throw URLError(.badURL) }
        var req = URLRequest(url: url, timeoutInterval: 10)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["username": user, "password": pass])
        let (data, _) = try await URLSession.shared.data(for: req)
        let decoded = try JSONDecoder().decode(LoginResponse.self, from: data)
        guard let t = decoded.data?.token, !t.isEmpty else { throw LatticeAPIError.server(decoded.msg ?? "登录失败") }
        tokenStore.write(t)
        UserDefaults.standard.set(user, forKey: "lattice.adminUser")
        KeychainStore.set(pass, forKey: "lattice.password")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        await MainActor.run { AuthSession.shared.refresh() }
        try await resolveWorkspaceIfNeeded()
    }

    /// Issues an enrollment token for this device. Single use (one new device),
    /// but long-lived: the device presents the token every time it registers
    /// again, and the server rejects an expired token even for a device that is
    /// already enrolled.
    func createDeviceToken() async throws -> String {
        try await resolveWorkspaceIfNeeded()
        let data = try await request(method: "POST", path: "/api/v1/token/generate",
                                     body: ["expiry": "8760h", "limit": 1])
        struct Reply: Decodable {
            struct Body: Decodable { let token: String? }
            let data: Body?
        }
        guard let token = try? JSONDecoder().decode(Reply.self, from: data).data?.token, !token.isEmpty else {
            throw LatticeAPIError.server("服务器没有返回入网令牌")
        }
        return token
    }

    /// "Log in and join": logs in against `server`, then issues an enrollment
    /// token for this device. The login stays, so later management actions
    /// need no second login. Failures say which step failed.
    func loginAndCreateDeviceToken(server: String, user: String, pass: String) async throws -> String {
        UserDefaults.standard.set(server, forKey: "lattice.serverURL")
        do {
            try await login(user: user, pass: pass)
        } catch {
            throw AccountJoinError.login(error.localizedDescription)
        }
        do {
            return try await createDeviceToken()
        } catch {
            throw AccountJoinError.token(error.localizedDescription)
        }
    }

    /// Ends the management session: the token, the saved password and the cached
    /// workspace. Joining the network and the VPN profile are untouched.
    func logout() {
        tokenStore.clear()
        UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        KeychainStore.delete("lattice.password")
        Task { @MainActor in AuthSession.shared.refresh() }
    }

    /// Asks the user to log in (the UI presents the sheet) and reports whether
    /// they did. Used by actions the user started, never by background loads.
    private func ensureLoggedIn() async -> Bool {
        if isLoggedIn { return true }
        return await LoginCoordinator.shared.requestLogin()
    }

    var isLoggedIn: Bool {
        !token.isEmpty
    }

    var serverURL: String { baseURL }

    private func resolveWorkspaceIfNeeded() async throws {
        if !workspaceID.isEmpty { return }
        let data = try await request(method: "GET", path: "/api/v1/workspaces/list?page=1&pageSize=1")
        let decoded = try JSONDecoder().decode(WorkspaceListResponse.self, from: data)
        guard let ws = decoded.data?.list?.first?.id else {
            throw LatticeAPIError.server("未找到可用的工作空间")
        }
        UserDefaults.standard.set(ws, forKey: "lattice.workspaceId")
    }

    private var workspaceID: String {
        UserDefaults.standard.string(forKey: "lattice.workspaceId") ?? ""
    }

    @discardableResult
    private func request(method: String, path: String, body: [String: Any]? = nil, allowRelogin: Bool = true) async throws -> Data {
        // Writes are actions the user asked for: without a login, ask for one and
        // carry on. Reads stay quiet (the device list works without a login).
        let userAction = method != "GET"
        if userAction, !(await ensureLoggedIn()) {
            throw LatticeAPIError.server("已取消登录")
        }
        guard let url = URL(string: baseURL + path) else { throw URLError(.badURL) }
        var req = URLRequest(url: url, timeoutInterval: 10)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let body {
            req.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        if !workspaceID.isEmpty {
            req.setValue(workspaceID, forHTTPHeaderField: "X-Workspace-Id")
        }
        if !token.isEmpty {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse else {
            throw LatticeAPIError.server("无响应")
        }
        // The control plane reports auth failures as HTTP 200 with
        // {"code":401,...} in the body, not as an HTTP 401 — so check both, or an
        // expired token is never noticed and the caller decodes an error body.
        let bodyCode = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["code"] as? Int
        let unauthorized = http.statusCode == 401 || (http.statusCode == 200 && bodyCode == 401)
        // Management tokens expire; silently re-login once from the
        // Keychain-stored credentials and retry instead of failing the call.
        if unauthorized, allowRelogin {
            if await relogin() {
                return try await request(method: method, path: path, body: body, allowRelogin: false)
            }
            // The saved credentials no longer work: for an action the user
            // started, ask again instead of failing.
            if userAction, await LoginCoordinator.shared.requestLogin() {
                return try await request(method: method, path: path, body: body, allowRelogin: false)
            }
        }
        guard http.statusCode == 200 else {
            throw LatticeAPIError.server(Self.serverMessage(from: data, fallback: "HTTP \(http.statusCode)"))
        }
        if unauthorized {
            // Still unauthorized after the re-login attempt: say so instead of
            // handing the caller an error body to mis-decode.
            throw LatticeAPIError.server(Self.serverMessage(from: data, fallback: "未登录或登录已过期"))
        }
        return data
    }

    /// Silent re-login from the Keychain-stored credentials when the
    /// management token expires.
    private func relogin() async -> Bool {
        guard let user = UserDefaults.standard.string(forKey: "lattice.adminUser"),
              let pass = KeychainStore.get("lattice.password") else { return false }
        do {
            _ = try await login(user: user, pass: pass)
            return true
        } catch {
            return false
        }
    }

    private static func serverMessage(from data: Data, fallback: String) -> String {
        if let decoded = try? JSONDecoder().decode(LoginResponse.self, from: data), let msg = decoded.msg, !msg.isEmpty {
            return msg
        }
        return fallback
    }
}

// MARK: - API Response Types

/// One 对外发布 rule as returned by /api/v1/publish/list.
struct PublishItem: Codable, Identifiable {
    let name: String
    let peerName: String
    let port: Int
    let enabled: Bool

    var id: String { name }
}

struct PeerListResponse: Codable {
    let code: Int
    let data: PeerListData?
    let msg: String?

    struct PeerListData: Codable {
        let total: Int
        let list: [PeerItem]?
    }

    struct PeerItem: Codable {
        let name: String?
        let appId: String?
        let address: String
        let status: String
        let lastSeen: String?
        let displayName: String?
        let disabled: Bool?
        let labels: [String: String]?
        let advertisedRoutes: [String]?
        let approvalStatus: String?
        let publicKey: String?
    }
}

struct LoginResponse: Codable {
    let code: Int
    let data: LoginData?
    let msg: String?

    struct LoginData: Codable {
        let token: String?
    }
}

/// One workspace as the account page shows it (name, quota).
struct WorkspaceItem: Codable, Identifiable {
    let id: String?
    let displayName: String?
    let slug: String?
    let nodeCount: Int?
    let quotaUsage: Int?
    let maxNodeCount: Int?
}

struct WorkspaceListResponse: Codable {
    let code: Int
    let data: WorkspaceListData?
    let msg: String?

    struct WorkspaceListData: Codable {
        let list: [WorkspaceItem]?
    }
}

enum LatticeAPIError: Error {
    case server(String)
}

// MARK: - AI Chat Types

struct ChatAPIMessage: Codable {
    let role: String
    let content: String
}

struct ChatStreamEvent: Codable {
    let type: String?
    let content: String?
    let tool: String?
    let error: String?
}

enum ChatEvent {
    case token(String)
    case toolUse(String)
    case error(String)
    case done
}

// MARK: - Policies / Intent / Agent Identity Types

struct PolicyListResponse: Codable {
    let code: Int
    let data: PolicyListData?
    let msg: String?

    struct PolicyListData: Codable {
        let list: [LatticePolicy]?
    }
}

struct LatticePolicy: Codable, Identifiable {
    let name: String
    let action: String
    let status: String?
    let policyTypes: [String]?
    let peerSelector: [String: String]?
    let ingress: [PolicyRuleSet]?
    let egress: [PolicyRuleSet]?

    var id: String { name }
}

struct PolicyRuleSet: Codable {
    let from: [PolicyPeer]?
    let to: [PolicyPeer]?
}

struct PolicyPeer: Codable {
    let ipBlock: IPBlock?
}

struct IPBlock: Codable {
    let cidr: String
}

/// aclEntries describes a policy's effect on one peer for the ACL debug view.
struct ACLEntry: Identifiable {
    let policy: String
    let allow: Bool
    let direction: String // "入站" / "出站" / "双向"
    let matched: Bool     // false = selector/network doesn't reference this peer

    var id: String { policy + direction }
}

extension LatticePolicy {
    /// Builds the ACL entries a policy produces for the given peer.
    /// Empty peer selectors match every peer in the network (same semantics
    /// the netmap compiler applies); CIDR blocks match by overlay address.
    func aclEntries(peer: PeerNode) -> [ACLEntry] {
        let selectorMatches = (peerSelector ?? [:]).isEmpty || {
            guard let labels = peer.labels else { return false }
            return (peerSelector ?? [:]).allSatisfy { key, value in labels[key] == value }
        }()

        var entries: [ACLEntry] = []
        func cidr(_ cidr: String, contains ip: String) -> Bool {
            guard let addr = IPv4Address(ip), let net = IPv4Address(cidr.split(separator: "/").first.map(String.init) ?? "") else { return false }
            let prefix = Int(cidr.split(separator: "/").last ?? "32") ?? 32
            guard prefix >= 0 && prefix <= 32 else { return false }
            let mask: UInt32 = prefix == 0 ? 0 : ~UInt32(0) << (32 - UInt32(prefix))
            return (addr.value & mask) == (net.value & mask)
        }

        func ruleIPs(_ peers: [PolicyPeer]?) -> [String] {
            (peers ?? []).compactMap { $0.ipBlock?.cidr }
        }

        let ingressCIDRs = ruleIPs(ingress?.flatMap { $0.from ?? [] })
        let egressCIDRs = ruleIPs(egress?.flatMap { $0.to ?? [] })
        let allow = action.lowercased() == "allow"

        if ingressCIDRs.isEmpty && egressCIDRs.isEmpty {
            entries.append(ACLEntry(
                policy: name, allow: allow,
                direction: (policyTypes?.count ?? 0) == 1 ? (policyTypes?.first == "Ingress" ? "入站" : "出站") : "双向",
                matched: selectorMatches
            ))
            return entries
        }
        if ingressCIDRs.contains(where: { cidr($0, contains: peer.address) }) {
            entries.append(ACLEntry(policy: name, allow: allow, direction: "入站", matched: true))
        }
        if egressCIDRs.contains(where: { cidr($0, contains: peer.address) }) {
            entries.append(ACLEntry(policy: name, allow: allow, direction: "出站", matched: true))
        }
        return entries
    }
}

/// Minimal IPv4 parse for CIDR containment checks.
struct IPv4Address {
    let value: UInt32
    init?(_ string: String) {
        var parts = string.split(separator: ".").compactMap { UInt32($0) }
        guard parts.count == 4 else { return nil }
        value = (parts[0] << 24) | (parts[1] << 16) | (parts[2] << 8) | parts[3]
    }
}

struct IntentPlanResponse: Codable {
    let code: Int
    let data: IntentPlanView?
    let msg: String?
}

struct IntentPlanView: Codable {
    let id: String
    let summary: String?
    let riskLevel: String?
    let changes: [IntentChange]?
}

struct IntentChange: Codable {
    let action: String?
    let name: String?
}

struct AgentIdentityListResponse: Codable {
    let code: Int
    let data: [AgentIdentityVO]?
    let msg: String?
}

struct AgentIdentityVO: Codable {
    let name: String?
    let peerRef: String?
    let sandbox: String?
}


/// Whether a management login exists, observable by the views. The token itself
/// lives in the Keychain (AuthTokenStore), where SwiftUI cannot watch it.
@MainActor
final class AuthSession: ObservableObject {
    static let shared = AuthSession()

    @Published private(set) var isLoggedIn: Bool

    private init() {
        isLoggedIn = !AuthTokenStore.standard.read().isEmpty
    }

    func refresh() {
        isLoggedIn = !AuthTokenStore.standard.read().isEmpty
    }
}


/// Which step of "log in and join" failed.
enum AccountJoinError: Error {
    case login(String)
    case token(String)

    var failure: JoinFailure {
        switch self {
        case .login(let message): return .loginFailed(message)
        case .token(let message): return .tokenNotIssued(message)
        }
    }
}
