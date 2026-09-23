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
import PlayerKit
import PlayerKitNative
import LatticeCastKit

/// iOS 投屏接收：LatticeCastRenderer 起 HTTP 服务（:7822）并经 mDNS 自报，
/// cast-agent 推流后由 PlayerKit（FFmpeg 后端，与 reflux 同一播放内核）播放。
@MainActor
final class IOSCastReceiverManager: ObservableObject {
    static let shared = IOSCastReceiverManager()

    @Published private(set) var isRunning = false
    @Published private(set) var startError: String?
    @Published private(set) var controller: PlayerKitCastController?

    private var renderer: LatticeCastRenderer?

    var config: LatticeCastConfig? { LatticeCastProvisioning.loadConfig() }

    /// 首启默认配对：设备名 + 固定房间 + 随机令牌（一次生成持久化）。
    func ensureDefaultConfig() {
        guard config == nil else { return }
        let token = UserDefaults.standard.string(forKey: "lattice.castReceiver.token") ?? {
            let chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
            let generated = String((0..<12).map { _ in chars.randomElement()! })
            UserDefaults.standard.set(generated, forKey: "lattice.castReceiver.token")
            return generated
        }()
        let name = UIDevice.current.name
        LatticeCastProvisioning.save(LatticeCastConfig(name: name, room: "lattice", token: token, port: 7822))
    }

    func start() {
        ensureDefaultConfig()
        guard let config = LatticeCastProvisioning.loadConfig() else {
            startError = "缺少配对配置"
            return
        }
        stop()
        do {
            let controller = try PlayerKitCastController()
            let renderer = LatticeCastRenderer(config: config, controller: controller)
            try renderer.start()
            self.renderer = renderer
            self.controller = controller
            isRunning = true
            startError = nil
        } catch {
            startError = error.localizedDescription
        }
    }

    func stop() {
        renderer?.stop()
        renderer = nil
        controller = nil
        isRunning = false
    }
}

/// PlayerKit（FFmpeg 后端）实现 Kit 的 PlaybackController 协议。
/// load 同步等待起播（15s 上限），把开流错误如实抛给 agent——与 reflux 的
/// RendererBridge 同源思路，但不依赖 reflux 应用层服务。
final class PlayerKitCastController: PlaybackController {
    let player: Player
    let nativeView: PlayerNativeView

    enum ControllerError: LocalizedError {
        case sourceUnreachable
        case loadTimeout
        var errorDescription: String? {
            switch self {
            case .sourceUnreachable: return "源无法播放"
            case .loadTimeout: return "源加载超时"
            }
        }
    }

    @MainActor
    init() throws {
        let backend = try NativeBackend()
        backend.displayCapability = DisplayCapability.probeCurrent()
        backend.doviEnabled = false
        let p = Player(backend: backend)
        player = p
        nativeView = PlayerNativeView(player: p)
    }

    // MARK: - LatticeCastKit.PlaybackController

    func load(url: URL, title: String?, positionMS: Int64) throws {
        let start = positionMS > 0 ? Duration.milliseconds(positionMS) : nil
        onMain { self.player.play(url: url, seekTo: start) }
        // PlayerKit 开流异步：等播放起步或报错（与 reflux RendererBridge 同款权衡）。
        let startedAt = Date()
        while Date().timeIntervalSince(startedAt) < 15 {
            let st = try onMain { self.player.state }
            if let err = st.error { throw ControllerError.sourceUnreachable }
            if st.isPlaying { return }
            Thread.sleep(forTimeInterval: 0.1)
        }
        throw ControllerError.loadTimeout
    }

    func pause() throws {
        onMain { self.player.pause() }
    }

    func stop() throws {
        onMain { self.player.stop() }
    }

    func seek(positionMS: Int64) throws {
        let target = Duration.milliseconds(positionMS)
        onMain { self.player.seek(to: target) }
    }

    func volume(level: Int) throws {
        let v = Double(level) / 100.0
        onMain { self.player.setVolume(v) }
    }

    func status() -> Status {
        let st = try onMain { self.player.state }
        let s = st.isPlaying ? "playing" : (st.position > .zero ? "paused" : "idle")
        return Status(state: s,
                      positionMS: Self.ms(st.position),
                      durationMS: Self.ms(st.duration),
                      title: "")
    }

    private static func ms(_ d: Duration) -> Int64 {
        let c = d.components
        return Int64(c.seconds) * 1000 + Int64(c.attoseconds / 1_000_000_000_000_000)
    }

    /// Player 是 @MainActor；RendererServer 的调用线程不定，这里统一 hop。
    private func onMain<T>(_ body: @MainActor @escaping () throws -> T) rethrows -> T {
        if Thread.isMainThread { return try MainActor.assumeIsolated(body) }
        return try DispatchQueue.main.sync { try MainActor.assumeIsolated(body) }
    }
}

/// 「投屏接收」页：开关接收、播放画面、配对信息。
struct CastReceiverPage: View {
    @StateObject private var manager = IOSCastReceiverManager.shared

    var body: some View {
        List {
            Section {
                Toggle("投屏接收", isOn: Binding(
                    get: { manager.isRunning },
                    set: { $0 ? manager.start() : manager.stop() }
                ))
                if manager.isRunning {
                    Label("已就绪，等待 cast-agent 推送", systemImage: "dot.radiowaves.left.and.right")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                if let err = manager.startError {
                    Text(err).font(.caption).foregroundColor(.red)
                }
            } footer: {
                Text("同一局域网内的 cast-agent 会经 mDNS 自动发现本机（_latticecast._tcp）。播放使用 Reflux 同款播放内核。")
            }

            if let controller = manager.controller {
                Section("正在播放") {
                    PlayerNativeView(player: controller.player)
                        .frame(height: 220)
                        .cornerRadius(10)
                    Text("投屏画面来自网内 cast-agent 的推送")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
            }

            Section("配对信息") {
                if let config = manager.config {
                    LabeledContent("设备名", value: config.name)
                    LabeledContent("房间", value: config.room)
                    LabeledContent("配对令牌", value: config.token)
                    LabeledContent("端口", value: String(config.port))
                }
            }
        }
        .navigationTitle("投屏接收")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { manager.ensureDefaultConfig() }
    }
}
