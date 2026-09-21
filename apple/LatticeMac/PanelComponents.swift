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

// MARK: - Page header (secondary pages)

/// Top bar of a secondary page: "‹ 返回" + title (+ optional accessory such as
/// a 即将推出 badge). Replaces the hand-rolled "‹ 返回主面板" headers.
struct PageHeader<Accessory: View>: View {
    let title: String
    let onBack: () -> Void
    @ViewBuilder var accessory: () -> Accessory

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Button(action: onBack) {
                    HStack(spacing: 2) {
                        Image(systemName: "chevron.left")
                            .font(.system(size: 11, weight: .semibold))
                        Text("返回").font(.system(size: 12))
                    }
                    .foregroundColor(.accentColor)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                Text(title)
                    .font(.system(.headline, design: .rounded))
                    .lineLimit(1)
                accessory()
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 16)
            .frame(height: 44)
            Divider()
        }
    }
}

extension PageHeader where Accessory == EmptyView {
    init(title: String, onBack: @escaping () -> Void) {
        self.init(title: title, onBack: onBack) { EmptyView() }
    }
}

// MARK: - Sheet scaffold

/// Shared frame of every sheet: title + a ✕ close button on the top row
/// (Esc closes too), fixed 340pt width, 20pt padding.
struct SheetScaffold<Content: View>: View {
    let title: String
    let onClose: () -> Void
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Text(title)
                    .font(.system(.headline, design: .rounded))
                Spacer()
                Button(action: onClose) {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 15))
                        .foregroundColor(.secondary)
                }
                .buttonStyle(.plain)
                .keyboardShortcut(.cancelAction)
                .help("关闭")
            }
            content()
        }
        .padding(20)
        .frame(width: 340)
    }
}

// MARK: - State view (loading / error / empty)

/// One layout for the loading, error and empty states of the first screen.
struct StateView: View {
    var icon: String? = nil
    var iconColor: Color = .secondary
    let title: String
    var message: String? = nil
    var actionTitle: String? = nil
    var prominent = false
    var isLoading = false
    var action: (() -> Void)? = nil

    var body: some View {
        VStack(spacing: 10) {
            if isLoading {
                ProgressView().controlSize(.small)
            } else if let icon {
                Image(systemName: icon)
                    .font(.system(size: 30))
                    .foregroundColor(iconColor)
            }
            Text(title).foregroundColor(.secondary)
            if let message {
                Text(message)
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let actionTitle, let action {
                if prominent {
                    Button(actionTitle, action: action).buttonStyle(.borderedProminent)
                } else {
                    Button(actionTitle, action: action).buttonStyle(.bordered)
                }
            }
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

// MARK: - Bottom nav bar

struct PanelNavItem: Identifiable {
    let id: String
    let icon: String
    let title: String
    var showsDot = false
    let action: () -> Void
}

/// The single icon bar at the bottom of the first screen.
struct PanelNavBar: View {
    let items: [PanelNavItem]

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 0) {
                ForEach(items) { item in
                    PanelNavButton(item: item)
                }
            }
        }
    }
}

private struct PanelNavButton: View {
    let item: PanelNavItem
    @State private var hovered = false

    var body: some View {
        Button(action: item.action) {
            VStack(spacing: 3) {
                Image(systemName: item.icon)
                    .font(.system(size: 15, weight: .medium))
                    .overlay(alignment: .topTrailing) {
                        if item.showsDot {
                            Circle()
                                .fill(LatticePalette.online)
                                .frame(width: 7, height: 7)
                                .offset(x: 4, y: -2)
                        }
                    }
                Text(item.title).font(.system(size: 10.5))
            }
            .foregroundColor(Color.primary.opacity(0.8))
            .frame(maxWidth: .infinity)
            .padding(.vertical, 7)
            .contentShape(Rectangle())
            .background(hovered ? Color.primary.opacity(0.045) : Color.clear)
        }
        .buttonStyle(.plain)
        .onHover { hovered = $0 }
    }
}

// MARK: - Window visibility

/// Calls `onHidden` whenever the hosting window stops being visible. The
/// menu-bar panel keeps its SwiftUI state across open/close and does not
/// reliably fire onAppear/onDisappear, so window occlusion is the signal.
struct WindowHiddenObserver: NSViewRepresentable {
    var onHidden: () -> Void

    func makeNSView(context: Context) -> NSView {
        ObserverView(onHidden: onHidden)
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        (nsView as? ObserverView)?.onHidden = onHidden
    }

    private final class ObserverView: NSView {
        var onHidden: () -> Void
        private var token: NSObjectProtocol?

        init(onHidden: @escaping () -> Void) {
            self.onHidden = onHidden
            super.init(frame: .zero)
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let token { NotificationCenter.default.removeObserver(token) }
            token = nil
            guard let window else { return }
            token = NotificationCenter.default.addObserver(
                forName: NSWindow.didChangeOcclusionStateNotification,
                object: window,
                queue: .main
            ) { [weak self] note in
                guard let w = note.object as? NSWindow, !w.occlusionState.contains(.visible) else { return }
                self?.onHidden()
            }
        }

        deinit {
            if let token { NotificationCenter.default.removeObserver(token) }
        }
    }
}
