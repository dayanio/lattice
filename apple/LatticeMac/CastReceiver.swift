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
import LatticeCastKit

/// Lattice 内置投屏接收端：LatticeCastRenderer（协议栈）+ AVPlayer sink。
/// 传输与信令归 LatticeCastKit，渲染走系统 AVPlayer——媒体业务不在
/// Lattice；Reflux 可注册富渲染 sink 深度承接 115 内容。
@MainActor
final class CastReceiverManager: ObservableObject {
    static let shared = CastReceiverManager()

    @Published private(set) var isRunning = false
    @Published private(set) var startError: String?
    @Published private(set) var isEnabled: Bool

    private var renderer: LatticeCastRenderer?
    private let sink = AVPlayerPlaybackController.shared

    private init() {
        isEnabled = UserDefaults.standard.object(forKey: "lattice.castReceiver.enabled") as? Bool ?? true
    }

    var config: LatticeCastConfig? { LatticeCastProvisioning.loadConfig() }

    /// 首启默认配对：机器名 + 固定房间 + 随机令牌（一次生成持久化）。
    func ensureDefaultConfig() {
        guard config == nil else { return }
        let token = UserDefaults.standard.string(forKey: "lattice.castReceiver.token") ?? {
            let chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
            let generated = String((0..<12).map { _ in chars.randomElement()! })
            UserDefaults.standard.set(generated, forKey: "lattice.castReceiver.token")
            return generated
        }()
        let name = Host.current().localizedName ?? "lattice-mac"
        LatticeCastProvisioning.save(LatticeCastConfig(name: name, room: "lattice", token: token, port: 7822))
    }

    func startIfNeeded() {
        guard !isRunning, isEnabled, let config = LatticeCastProvisioning.loadConfig() else { return }
        start(config: config)
    }

    func start(config: LatticeCastConfig) {
        stop()
        let renderer = LatticeCastRenderer(config: config, controller: sink)
        do {
            try renderer.start()
            self.renderer = renderer
            isRunning = true
            startError = nil
        } catch {
            startError = error.localizedDescription
        }
    }

    func stop() {
        renderer?.stop()
        renderer = nil
        isRunning = false
    }

    func save(config: LatticeCastConfig, enabled: Bool) {
        LatticeCastProvisioning.save(config)
        UserDefaults.standard.set(enabled, forKey: "lattice.castReceiver.enabled")
        isEnabled = enabled
        if enabled {
            start(config: config)
        } else {
            stop()
        }
    }

    func setEnabled(_ enabled: Bool) {
        UserDefaults.standard.set(enabled, forKey: "lattice.castReceiver.enabled")
        isEnabled = enabled
        if enabled {
            startIfNeeded()
        } else {
            stop()
        }
    }
}

// MARK: - Panel section (投屏接收)

struct CastSectionView: View {
    @ObservedObject private var receiver = CastReceiverManager.shared
    @State private var showingPairing = false

    var body: some View {
        SectionHead(title: "投屏接收")
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("接收投屏").font(.system(size: 13)).foregroundColor(.primary)
                Spacer()
                Toggle("", isOn: Binding(
                    get: { receiver.isEnabled },
                    set: { receiver.setEnabled($0) }
                ))
                .toggleStyle(.switch)
                .controlSize(.mini)
                .labelsHidden()
            }
            if receiver.isRunning, let config = receiver.config {
                Text("接收中 · \(config.name) · 房间 \(config.room)")
                    .font(.caption)
                    .foregroundColor(.secondary)
            } else if let err = receiver.startError {
                Text(err).font(.caption).foregroundColor(.red)
            } else {
                Text("未配对 — 保存配对后即可接收").font(.caption).foregroundColor(.secondary)
            }
            Button("配对信息…") { showingPairing = true }
                .font(.caption)
                .buttonStyle(.plain)
                .foregroundColor(.accentColor)
        }
        .padding(10)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.primary.opacity(0.04))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 10)
                .strokeBorder(Color.primary.opacity(0.08))
        )
        .padding(.horizontal, 10)
        .sheet(isPresented: $showingPairing) {
            CastPairingSheet { showingPairing = false }
        }
    }
}

/// 配对信息编辑（渲染端身份：name/room/token/port）。
struct CastPairingSheet: View {
    var onDone: () -> Void

    @State private var name = Host.current().localizedName ?? "lattice-mac"
    @State private var room = "lattice"
    @State private var token = ""
    @State private var port = "7822"

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("投屏接收配对")
                .font(.system(.headline, design: .rounded))
            LabeledField(label: "接收端名称") {
                TextField("lattice-mac", text: $name).textFieldStyle(.plain)
            }
            LabeledField(label: "房间") {
                TextField("lattice", text: $room).textFieldStyle(.plain)
            }
            LabeledField(label: "配对令牌") {
                SecureField("cast-agent 签发", text: $token).textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }
            LabeledField(label: "端口") {
                TextField("7822", text: $port).textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }
            Text("在 cast-agent 侧登记此名称与令牌后，即可向本机发起投屏。")
                .font(.caption2).foregroundColor(.secondary)
            HStack {
                Spacer()
                Button("保存") { save() }
                    .buttonStyle(.borderedProminent)
                    .disabled(name.isEmpty || room.isEmpty || token.isEmpty)
            }
        }
        .padding(20)
        .frame(width: 320)
        .onAppear {
            if let existing = CastReceiverManager.shared.config {
                name = existing.name
                room = existing.room
                token = existing.token
                port = String(existing.port)
            }
        }
    }

    private func save() {
        let config = LatticeCastConfig(
            name: name, room: room, token: token,
            port: Int(port) ?? 7822
        )
        CastReceiverManager.shared.save(config: config, enabled: true)
        onDone()
    }
}
