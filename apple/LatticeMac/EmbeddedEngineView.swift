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
import EmbeddedKit

/// Owns the in-process embedded engine so it survives SwiftUI view
/// recreation. The WireGuard identity is persisted in the Keychain: the
/// control plane rejects re-registering the same device name under a
/// different key, so the first run's generated key is stored and replayed.
@MainActor
final class EmbeddedEngineRunner: ObservableObject {
    static let shared = EmbeddedEngineRunner()

    enum Phase: Equatable {
        case idle, starting, running, stopping
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var overlayIP: String?
    @Published var lines: [String] = []

    static let deviceName = "lattice-mac-embedded"
    private static let keychainKey = "lattice.embedded.wgkey"
    static let defaultEchoPort: UInt16 = 9500

    private var engine: EmbeddedEngine?
    private var echoListener: EmbeddedListener?
    private var echoTask: Task<Void, Never>?

    func start() {
        guard phase == .idle else { return }
        phase = .starting
        Task { await doStart() }
    }

    func stop() {
        guard phase == .running else { return }
        phase = .stopping
        Task { await doStop() }
    }

    func startEcho(port: UInt16) {
        guard let engine = engine, echoListener == nil else { return }
        do {
            let listener = try engine.listen(network: "tcp", addr: "\(overlayIP ?? ""):\(port)")
            echoListener = listener
            log("监听 \(overlayIP!):\(port) 等待入站…")
            echoTask = Task { [weak self] in
                while !Task.isCancelled, let self {
                    do {
                        let conn = try listener.accept()
                        self.log("入站连接已接受")
                        let data = self.readAll(conn)
                        if let text = String(data: data, encoding: .utf8) {
                            self.log("收到 \(data.count) 字节：\(text.isEmpty ? "(空)" : text)")
                        } else {
                            self.log("收到 \(data.count) 字节（非 UTF-8）")
                        }
                        try? conn.write(data)
                        conn.close()
                        self.log("已回显并关闭")
                    } catch {
                        if !Task.isCancelled { self.log("accept 结束：\(error.localizedDescription)") }
                        break
                    }
                }
            }
        } catch {
            log("监听失败：\(error.localizedDescription)")
        }
    }

    func stopEcho() {
        echoTask?.cancel()
        echoListener?.close()
        echoListener = nil
        log("监听已关闭")
    }

    var echoActive: Bool { echoListener != nil }

    func dial(target: String, payload: String) {
        guard let engine = engine else { return }
        Task {
            do {
                let conn = try engine.dial(network: "tcp", addr: target)
                let data = Data(payload.utf8)
                try conn.write(data)
                log("已向 \(target) 发送 \(data.count) 字节")
                let reply = self.readAll(conn)
                if let text = String(data: reply, encoding: .utf8), !text.isEmpty {
                    log("来自 \(target) 的回包：\(text)")
                } else {
                    log("\(target) 无回包（对端关闭前未发送数据）")
                }
                conn.close()
            } catch {
                log("拨号 \(target) 失败：\(error.localizedDescription)")
            }
        }
    }

    // MARK: - internals

    private func doStart() async {
        do {
            let token = try await LatticeAPI.shared.createDeviceToken()
            var storedKey = KeychainStore.get(Self.keychainKey) ?? ""
            var config: [String: String] = [
                "serverURL": LatticeAPI.shared.serverURL,
                "token": token,
                "name": Self.deviceName,
            ]
            if !storedKey.isEmpty { config["privateKey"] = storedKey }
            let jsonData = try JSONSerialization.data(withJSONObject: config)
            let engine = try EmbeddedEngine(configJSON: String(data: jsonData, encoding: .utf8)!)
            self.engine = engine
            try engine.start()

            // Poll for registration; self-approve each cycle in case the
            // workspace gates new devices behind approval.
            for _ in 0..<30 {
                if let addr = engine.overlayAddress {
                    overlayIP = addr
                    if storedKey.isEmpty {
                        storedKey = engine.privateKey ?? ""
                        if !storedKey.isEmpty {
                            KeychainStore.set(storedKey, forKey: Self.keychainKey)
                            log("已生成并保存本机 WireGuard 身份（Keychain）")
                        }
                    }
                    phase = .running
                    log("引擎已启动，overlay 地址 \(addr)（无 NE，进程内 netstack）")
                    return
                }
                try? await LatticeAPI.shared.setPeerApproval(Self.deviceName, approved: true)
                try await Task.sleep(nanoseconds: 500_000_000)
            }
            log("注册超时：15 秒内未拿到 overlay 地址")
            await doStop()
        } catch {
            log("启动失败：\(error.localizedDescription)")
            phase = .idle
        }
    }

    private func doStop() async {
        stopEcho()
        engine?.stop()
        engine = nil
        overlayIP = nil
        phase = .idle
        log("引擎已停止")
    }

    /// Reads until EOF; errors are collapsed to the bytes read so far.
    private func readAll(_ conn: EmbeddedConnection) -> Data {
        var out = Data()
        while let chunk = try? conn.read(maxBytes: 16 * 1024), !chunk.isEmpty {
            out.append(chunk)
        }
        return out
    }

    func log(_ text: String) {
        let formatter = DateFormatter()
        formatter.dateFormat = "HH:mm:ss"
        lines.append("[\(formatter.string(from: Date()))] \(text)")
        if lines.count > 200 { lines.removeFirst(lines.count - 200) }
    }
}

/// 嵌入式引擎页面：App 进程内直接入网，不经过 NE 隧道扩展。
struct EmbeddedEnginePage: View {
    var onBack: () -> Void

    @ObservedObject private var runner = EmbeddedEngineRunner.shared
    @State private var echoPort: UInt16 = EmbeddedEngineRunner.defaultEchoPort
    @State private var dialTarget: String = "10.96.0.2:9501"
    @State private var payload: String = "hello from lattice mac (embedded)"

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.embeddedEngine.title, onBack: onBack)

            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    statusCard
                    echoCard
                    dialCard
                    consoleCard
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 20)
            }
        }
    }

    private var statusCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("进程内引擎").font(.headline)
                    Text("无 Network Extension、无内核 TUN、无系统 VPN 图标")
                        .font(.caption).foregroundColor(.secondary)
                }
                Spacer()
                switch runner.phase {
                case .idle:
                    Button("启动") { runner.start() }
                        .disabled(!LatticeAPI.shared.isLoggedIn)
                case .starting:
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text("注册中…").font(.caption).foregroundColor(.secondary)
                    }
                case .running:
                    Button("停止", role: .destructive) { runner.stop() }
                case .stopping:
                    Text("停止中…").font(.caption).foregroundColor(.secondary)
                }
            }

            if let addr = runner.overlayIP {
                LabeledContent {
                    Text(addr).font(.system(.caption, design: .monospaced))
                } label: {
                    Text("Overlay 地址").font(.caption).foregroundColor(.secondary)
                }
            }
            if !LatticeAPI.shared.isLoggedIn {
                Text("请先在主界面登录，再启动嵌入式引擎").font(.caption).foregroundColor(.orange)
            }
        }
        .padding(12)
        .background(Color.secondary.opacity(0.06))
        .cornerRadius(10)
        .padding(.top, 8)
    }

    private var echoCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("监听回显").font(.subheadline).fontWeight(.semibold)
            HStack {
                Text("端口").font(.caption).foregroundColor(.secondary)
                TextField("9500", value: $echoPort, format: .number)
                    .textFieldStyle(.roundedBorder).frame(width: 90)
                    .disabled(runner.phase != .running)
                Spacer()
                if runner.echoActive {
                    Button("停止监听") { runner.stopEcho() }
                } else {
                    Button("开始监听") { runner.startEcho(port: echoPort) }
                        .disabled(runner.phase != .running)
                }
            }
            Text("对端连接此端口后，收到什么就回什么（可从容器或另一节点 nc 测试）")
                .font(.caption2).foregroundColor(.secondary)
        }
        .padding(12)
        .background(Color.secondary.opacity(0.06))
        .cornerRadius(10)
    }

    private var dialCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("拨号测试").font(.subheadline).fontWeight(.semibold)
            HStack {
                TextField("10.96.0.2:9501", text: $dialTarget)
                    .textFieldStyle(.roundedBorder)
                    .font(.system(.caption, design: .monospaced))
                    .disabled(runner.phase != .running)
            }
            HStack {
                TextField("发送内容", text: $payload)
                    .textFieldStyle(.roundedBorder)
                    .disabled(runner.phase != .running)
                Button("发送") {
                    runner.dial(target: dialTarget, payload: payload)
                }
                .disabled(runner.phase != .running || payload.isEmpty)
            }
            Text("对端可用 `nc -l -p 端口` 接收；有回包时会显示在下方日志")
                .font(.caption2).foregroundColor(.secondary)
        }
        .padding(12)
        .background(Color.secondary.opacity(0.06))
        .cornerRadius(10)
    }

    private var consoleCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("日志").font(.subheadline).fontWeight(.semibold)
                Spacer()
                Button("清空") { runner.lines.removeAll() }
                    .font(.caption)
            }
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(Array(runner.lines.enumerated()), id: \.offset) { _, line in
                            Text(line)
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        Color.clear.frame(height: 1).id("bottom")
                    }
                }
                .frame(height: 160)
                .onChange(of: runner.lines.count) { _ in
                    proxy.scrollTo("bottom")
                }
            }
        }
        .padding(12)
        .background(Color.secondary.opacity(0.06))
        .cornerRadius(10)
    }
}
