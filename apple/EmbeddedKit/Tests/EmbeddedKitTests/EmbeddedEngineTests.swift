import XCTest
@testable import EmbeddedKit

// The M3 acceptance harness: one embedded engine started from Swift,
// verified end to end against the local mac-demo control plane and its
// containers — Listen side receives a payload from mac-node-a, Dial side
// delivers a payload to mac-node-a. Set LATTICE_EMBED_INTEGRATION=1 to run
// (mirrors the Go integration tests); other tests in the repo stay green
// without the local environment.
final class EmbeddedEngineTests: XCTestCase {
    static let server = ProcessInfo.processInfo.environment["LATTICE_EMBED_TEST_SERVER"] ?? "http://127.0.0.1:8080"

    // MARK: - control plane helpers (mirror the Go integration tests)

    private struct LoginResponse: Decodable {
        struct Data: Decodable { let token: String }
        let data: Data
    }

    private func login() async throws -> String {
        var req = URLRequest(url: URL(string: "\(Self.server)/api/v1/users/login")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try JSONEncoder().encode(["username": "admin", "password": "123456"])
        let (body, _) = try await URLSession.shared.data(for: req)
        return try JSONDecoder().decode(LoginResponse.self, from: body).data.token
    }

    private struct WorkspaceList: Decodable {
        struct Workspace: Decodable { let id: String; let slug: String }
        struct Data: Decodable { let list: [Workspace] }
        let data: Data
    }

    private func macDemoWorkspaceID(bearer: String) async throws -> String {
        var req = URLRequest(url: URL(string: "\(Self.server)/api/v1/workspaces/list")!)
        req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        let (body, _) = try await URLSession.shared.data(for: req)
        let decoded = try JSONDecoder().decode(WorkspaceList.self, from: body)
        guard let ws = decoded.data.list.first(where: { $0.slug == "mac-demo" }) else {
            throw XCTSkip("mac-demo workspace not found")
        }
        return ws.id
    }

    private struct TokenResponse: Decodable {
        struct Data: Decodable { let token: String }
        let data: Data
    }

    private func mintToken(bearer: String, workspaceID: String, name: String) async throws -> String {
        var req = URLRequest(url: URL(string: "\(Self.server)/api/v1/token/generate")!)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        req.setValue(workspaceID, forHTTPHeaderField: "X-Workspace-Id")
        req.httpBody = try JSONSerialization.data(withJSONObject: ["name": name, "expiry": "1h", "limit": 1])
        let (body, _) = try await URLSession.shared.data(for: req)
        return try JSONDecoder().decode(TokenResponse.self, from: body).data.token
    }

    private func approve(bearer: String, workspaceID: String, name: String) async {
        var req = URLRequest(url: URL(string: "\(Self.server)/api/v1/peers/\(name)/approval")!)
        req.httpMethod = "PUT"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue("Bearer \(bearer)", forHTTPHeaderField: "Authorization")
        req.setValue(workspaceID, forHTTPHeaderField: "X-Workspace-Id")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["status": "approved"])
        _ = try? await URLSession.shared.data(for: req)
    }

    // MARK: - docker helpers

    @discardableResult
    private func shell(_ command: String) throws -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/zsh")
        process.arguments = ["-c", command]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = Pipe()
        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else {
            throw NSError(domain: "shell", code: Int(process.terminationStatus),
                          userInfo: [NSLocalizedDescriptionKey: command])
        }
        return String(data: data, encoding: .utf8) ?? ""
    }

    // MARK: - the M3 acceptance test

    func testListenAndDialAgainstContainer() async throws {
        guard ProcessInfo.processInfo.environment["LATTICE_EMBED_INTEGRATION"] == "1" else {
            throw XCTSkip("set LATTICE_EMBED_INTEGRATION=1 to run against the local mac-demo control plane")
        }

        let bearer = try await login()
        let workspaceID = try await macDemoWorkspaceID(bearer: bearer)
        let deviceName = "embed-swift-\(Int(Date().timeIntervalSince1970 * 1000))"
        let token = try await mintToken(bearer: bearer, workspaceID: workspaceID, name: deviceName)

        let configJSON = try JSONSerialization.data(withJSONObject: [
            "serverURL": Self.server,
            "token": token,
            "name": deviceName,
        ])
        let engine = try EmbeddedEngine(configJSON: String(data: configJSON, encoding: .utf8)!)
        try engine.start()
        defer { engine.stop() }

        // Registration may land pending approval; approve until an overlay
        // address shows up (mirrors the Go integration tests).
        var overlay: String?
        for _ in 0..<30 {
            await approve(bearer: bearer, workspaceID: workspaceID, name: deviceName)
            if let addr = engine.overlayAddress { overlay = addr; break }
            try await Task.sleep(nanoseconds: 500_000_000)
        }
        let addr = try XCTUnwrap(overlay, "engine never acquired an overlay address")
        print("embedded engine (Swift) got overlay address \(addr)")

        // Give the container's WireGuard handshake a moment to arrive: the
        // engine has no NATS/relay signaling of its own, so its outbound
        // path to the container only exists after the inbound handshake
        // (same 3s convergence wait as the Go integration tests).
        try await Task.sleep(nanoseconds: 3_000_000_000)

        // --- Listen side: mac-node-a dials in and sends a payload. ---
        let listener = try engine.listen(network: "tcp", addr: "\(addr):9500")
        let acceptTask = Task {
            let conn = try listener.accept()
            defer { conn.close() }
            var received = Data()
            do {
                while let chunk = try conn.read(maxBytes: 4096) {
                    received.append(chunk)
                }
            } catch {
                // EOF from the peer closing the connection ends the stream.
            }
            return received
        }
        _ = try shell("docker exec mac-node-a sh -c 'echo -n hello-from-container | nc \(addr) 9500'")
        let listened = try await acceptTask.value
        XCTAssertEqual(String(data: listened, encoding: .utf8), "hello-from-container",
                       "payload from mac-node-a did not arrive on the Swift-accepted connection")
        listener.close()

        // --- Dial side: the engine delivers a payload to mac-node-a. ---
        // The outbound path needs the container's inbound WireGuard handshake
        // to arrive first, and the container's dial backoff can exceed a
        // single attempt window — retry the whole dial+write like a real
        // embedder would.
        var delivered = ""
        dialLoop: for _ in 0..<4 {
            try shell("docker exec mac-node-a sh -c 'rm -f /tmp/m3-payload.txt; (nc -l -p 9501 > /tmp/m3-payload.txt &)'")
            try await Task.sleep(nanoseconds: 1_000_000_000)
            let conn = try engine.dial(network: "tcp", addr: "10.96.0.2:9501")
            try conn.write(Data("hello-from-swift".utf8))
            conn.close()
            for _ in 0..<10 {
                try await Task.sleep(nanoseconds: 500_000_000)
                delivered = try shell("docker exec mac-node-a cat /tmp/m3-payload.txt")
                if !delivered.isEmpty { break dialLoop }
            }
        }
        XCTAssertEqual(delivered, "hello-from-swift",
                       "payload dialed from Swift never reached mac-node-a")

        engine.stop()
        XCTAssertNil(engine.overlayAddress, "overlay address should be cleared after stop")
    }
}
