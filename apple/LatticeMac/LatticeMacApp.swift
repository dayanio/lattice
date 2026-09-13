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

import SwiftUI

// MARK: - App Entry

@main
struct LatticeMacApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        WindowGroup {
            ContentView()
                .frame(width: 340)
                .frame(minHeight: 400, maxHeight: 560)
        }
        .windowStyle(.hiddenTitleBar)
        .windowResizability(.contentMinSize)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.activate(ignoringOtherApps: true)
    }
}

// MARK: - API Client

final class LatticeAPI {
    static let shared = LatticeAPI()
    private var baseURL: String {
        UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://localhost:8080"
    }
    private var token: String {
        UserDefaults.standard.string(forKey: "lattice.authToken") ?? ""
    }

    private func request(method: String, path: String, body: [String: Any]? = nil) async throws -> Data {
        guard let url = URL(string: baseURL + path) else {
            throw URLError(.badURL)
        }
        var req = URLRequest(url: url, timeoutInterval: 15)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue(UserDefaults.standard.string(forKey: "lattice.workspaceId") ?? "", forHTTPHeaderField: "X-Workspace-Id")
        if !token.isEmpty {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        if let body = body {
            req.httpBody = try JSONSerialization.data(withJSONObject: body)
        }
        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            let body = String(data: data, encoding: .utf8) ?? ""
            throw LatticeAPIError.server(body)
        }
        return data
    }

    func login(user: String, pass: String) async throws -> String {
        guard let url = URL(string: baseURL + "/api/v1/users/login") else { throw URLError(.badURL) }
        var req = URLRequest(url: url, timeoutInterval: 15)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["username": user, "password": pass])
        let (data, _) = try await URLSession.shared.data(for: req)
        let decoded = try JSONDecoder().decode(LoginResponse.self, from: data)
        guard let t = decoded.data?.token else { throw LatticeAPIError.noToken }
        UserDefaults.standard.set(t, forKey: "lattice.authToken")
        UserDefaults.standard.set(user, forKey: "lattice.adminUser")
        return t
    }

    func listPeers() async throws -> [PeerNode] {
        let data = try await request(method: "GET", path: "/api/v1/peers/list?page=1&pageSize=50", body: nil)
        let decoded = try JSONDecoder().decode(PeerListResponse.self, from: data)
        guard let list = decoded.data?.list else { return [] }
        return list.map { p in
            PeerNode(name: p.name, address: p.address, online: p.status == "online", os: "Linux")
        }
    }
}

// MARK: - Response types

struct LoginResponse: Codable {
    let code: Int
    let data: LoginData?
    let msg: String?
    struct LoginData: Codable { let token: String? }
}

struct PeerListResponse: Codable {
    let code: Int
    let data: PeerListData?
    struct PeerListData: Codable {
        let total: Int
        let list: [PeerItem]?
    }
    struct PeerItem: Codable {
        let name: String
        let address: String
        let status: String
        let lastSeen: String?
    }
}

enum LatticeAPIError: Error {
    case server(String)
    case noToken
    case noWorkspace
}
