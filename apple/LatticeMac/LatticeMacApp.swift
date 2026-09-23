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

/// Cross-window UI requests. The menu-bar panel cannot host text input
/// (the panel is not a key window — clicking outside dismisses it), so any
/// flow that needs typing is routed to the real main window via this state.
final class UIState: ObservableObject {
    static let shared = UIState()
    @Published var showJoin = false
    @Published var showSettings = false
    @Published var showCastPairing = false
    @Published var showAI = false
    @Published var showAccount = false
    @Published var detailPeerName: String?
}

// MARK: - App Entry

/// Menu-bar-resident client (see the UI mockup doc §02): the tray icon opens
/// the main panel as a popover window; the dock icon is hidden (LSUIElement)
/// and a regular window is available from the panel footer.
@main
struct LatticeMacApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        MenuBarExtra {
            MenuBarPanel()
        } label: {
            MenuBarGlyph()
        }
        .menuBarExtraStyle(.window)

        Window("Lattice", id: "main") {
            ContentView()
                // Capped, not just minned: this is a compact utility panel
                // (320pt fixed sidebar, capped-width detail pane), not a
                // dashboard — letting it stretch across a 27" display just
                // leaves the right pane floating in dead space.
                .frame(minWidth: 680, maxWidth: 1100, minHeight: 480, maxHeight: 780)
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 760, height: 640)
        .windowResizability(.contentSize)
    }
}

/// The tray glyph: a Tailscale-like hotspot icon, green while connected.
/// Also opens the onboarding window automatically on first run so the app
/// is discoverable (a bare menu-bar icon is easy to miss).
struct MenuBarGlyph: View {
    @Environment(\.openWindow) private var openWindow
    @StateObject private var tunnel = TunnelManager.shared

    var body: some View {
        // 晶格六边形：Lattice 品牌隐喻，与 Reflux 接收端的天线图标区分
        Image(systemName: "circle.hexagongrid.fill")
            .font(.system(size: 14, weight: .medium))
            .foregroundStyle(iconStyle)
            .onAppear {
                // Deferred: mutating the window scene during view update
                // trips "Modifying state during view update".
                DispatchQueue.main.async {
                    if !UserDefaults.standard.bool(forKey: "lattice.joined") {
                        openWindow(id: "main")
                    }
                }
            }
    }

    private var iconStyle: AnyShapeStyle {
        switch tunnel.status {
        case .connected:
            return AnyShapeStyle(LinearGradient(colors: [.green, .teal],
                           startPoint: .topLeading, endPoint: .bottomTrailing))
        case .connecting, .reasserting, .disconnecting:
            return AnyShapeStyle(Color.orange)
        default:
            return AnyShapeStyle(Color.primary)
        }
    }
}

/// Popover content: the shared main panel in panel mode (read-mostly —
/// every flow that needs typing routes to the main window). "加入网络" and
/// "退出 Lattice" live in the header's ⋯ menu.
struct MenuBarPanel: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        ContentView(
            inPanel: true,
            openMain: {
                openWindow(id: "main")
                NSApp.activate(ignoringOtherApps: true)
            }
        )
        .frame(width: 340)
        .frame(minHeight: 380, maxHeight: 560)
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.activate(ignoringOtherApps: true)
    }
}

