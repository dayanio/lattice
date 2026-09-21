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
    @Published var detailPeerName: String?
    @Published var page: Page?

    enum Page: String {
        case networkSettings
        case share
    }
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
                .frame(width: 360)
                .frame(minHeight: 420, maxHeight: 640)
        }
        .windowStyle(.hiddenTitleBar)
        .windowResizability(.contentMinSize)

        Window("Lattice AI 助手", id: "ai") {
            ChatWindow()
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 900, height: 660)
        .windowResizability(.contentMinSize)
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
/// every flow that needs typing routes to the main window).
struct MenuBarPanel: View {
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        VStack(spacing: 0) {
            ContentView(
                inPanel: true,
                openMain: {
                    openWindow(id: "main")
                    NSApp.activate(ignoringOtherApps: true)
                },
                openAI: {
                    openWindow(id: "ai")
                    NSApp.activate(ignoringOtherApps: true)
                }
            )
            .frame(width: 340)
            .frame(minHeight: 380, maxHeight: 560)
            Divider()
            HStack {
                Button {
                    UIState.shared.showJoin = true
                    openWindow(id: "main")
                    NSApp.activate(ignoringOtherApps: true)
                } label: {
                    Text("加入网络…").font(.caption)
                }
                .buttonStyle(.plain)
                Spacer()
                Button {
                    NSApp.terminate(nil)
                } label: {
                    Text("退出 Lattice").font(.caption)
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
        }
    }
}

class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.activate(ignoringOtherApps: true)
    }
}

