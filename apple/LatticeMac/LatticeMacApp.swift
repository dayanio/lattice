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
        UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "http://127.0.0.1:8080"
    }

    private var token: String {
        UserDefaults.standard.string(forKey: "lattice.authToken") ?? ""
    }

    func listPeers() async throws -> [PeerNode] {
        try await resolveWorkspaceIfNeeded()
        let data = try await request(method: "GET", path: "/api/v1/peers/list?page=1&pageSize=50")
        let decoded = try JSONDecoder().decode(PeerListResponse.self, from: data)
        guard let list = decoded.data?.list else { return [] }
        return list.map { p in
            PeerNode(name: p.name, address: p.address, online: p.status == "online")
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
        UserDefaults.standard.set(t, forKey: "lattice.authToken")
        UserDefaults.standard.set(user, forKey: "lattice.adminUser")
        UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
        try await resolveWorkspaceIfNeeded()
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

    private func request(method: String, path: String) async throws -> Data {
        guard let url = URL(string: baseURL + path) else {
            throw URLError(.badURL)
        }
        var req = URLRequest(url: url, timeoutInterval: 10)
        req.httpMethod = method
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !workspaceID.isEmpty {
            req.setValue(workspaceID, forHTTPHeaderField: "X-Workspace-Id")
        }
        if !token.isEmpty {
            req.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
        let (data, response) = try await URLSession.shared.data(for: req)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw LatticeAPIError.server(Self.serverMessage(from: data, fallback: "HTTP \( (response as? HTTPURLResponse)?.statusCode ?? -1)"))
        }
        return data
    }

    private static func serverMessage(from data: Data, fallback: String) -> String {
        if let decoded = try? JSONDecoder().decode(LoginResponse.self, from: data), let msg = decoded.msg, !msg.isEmpty {
            return msg
        }
        return fallback
    }
}

// MARK: - API Response Types

struct PeerListResponse: Codable {
    let code: Int
    let data: PeerListData?
    let msg: String?

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

struct LoginResponse: Codable {
    let code: Int
    let data: LoginData?
    let msg: String?

    struct LoginData: Codable {
        let token: String?
    }
}

struct WorkspaceListResponse: Codable {
    let code: Int
    let data: WorkspaceListData?
    let msg: String?

    struct WorkspaceListData: Codable {
        let list: [WorkspaceItem]?
    }

    struct WorkspaceItem: Codable {
        let id: String?
    }
}

enum LatticeAPIError: Error {
    case server(String)
}
