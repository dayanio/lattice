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

import AVKit
import Foundation
import LatticeCastKit

/// 系统 sink：LatticeCastKit 的 PlaybackController 落在 AVPlayer 上。
/// 协议注释里点名的预期实现——渲染用系统播放器，Lattice 不引入媒体栈；
/// 富格式（MKV/字幕/115 深度体验）由 Reflux 注册自己的 sink 承接。
final class AVPlayerPlaybackController: PlaybackController {
    static let shared = AVPlayerPlaybackController()

    let player = AVPlayer()
    private var currentTitle: String = ""

    private init() {}

    func load(url: URL, title: String?, positionMS: Int64) throws {
        currentTitle = title ?? url.lastPathComponent
        player.replaceCurrentItem(with: AVPlayerItem(url: url))
        if positionMS > 0 {
            player.seek(to: CMTime(value: CMTimeValue(positionMS), timescale: 1000))
        }
        player.play()
        DispatchQueue.main.async {
            CastPlayerWindowController.shared.present(player: self.player, title: self.currentTitle)
        }
    }

    func pause() throws { player.pause() }

    func stop() throws {
        player.pause()
        player.replaceCurrentItem(with: nil)
        currentTitle = ""
    }

    func seek(positionMS: Int64) throws {
        player.seek(to: CMTime(value: CMTimeValue(positionMS), timescale: 1000))
    }

    func volume(level: Int) throws {
        player.volume = Float(level) / 100.0
    }

    func status() -> Status {
        guard let item = player.currentItem else {
            return Status(state: "idle", positionMS: 0, durationMS: 0, title: currentTitle)
        }
        let state: String
        switch player.timeControlStatus {
        case .playing: state = "playing"
        case .paused: state = "paused"
        @unknown default: state = "paused"
        }
        let position = Int64(player.currentTime().seconds * 1000)
        let seconds = item.duration.seconds
        let duration = Int64((seconds.isFinite ? seconds : 0) * 1000)
        return Status(state: state, positionMS: position, durationMS: duration, title: currentTitle)
    }
}

/// 投屏播放窗：接收会话加载媒体时弹出，承载 AVPlayer 画面。
@MainActor
final class CastPlayerWindowController {
    static let shared = CastPlayerWindowController()

    private var window: NSWindow?

    func present(player: AVPlayer, title: String) {
        let window = self.window ?? makeWindow()
        self.window = window
        window.title = "Lattice 投屏 — \(title)"
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func makeWindow() -> NSWindow {
        let view = AVPlayerView()
        view.player = AVPlayerPlaybackController.shared.player
        view.showsFullScreenToggleButton = true
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 960, height: 540),
            styleMask: [.titled, .closable, .resizable, .miniaturizable],
            backing: .buffered, defer: false
        )
        window.contentView = view
        window.center()
        window.isReleasedWhenClosed = false
        window.title = "Lattice 投屏"
        return window
    }
}
