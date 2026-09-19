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

/// The mockup design language: soft-background pills, halo dots, hover
/// highlights, colored icon squares — one place so every surface matches.
enum LatticePalette {
    static let accent = Color(red: 0.04, green: 0.52, blue: 1.00)   // #0A84FF
    static let online = Color(red: 0.13, green: 0.77, blue: 0.37)   // green
    static let relay = Color(red: 0.96, green: 0.63, blue: 0.18)    // orange
    static let blocked = Color(red: 0.90, green: 0.22, blue: 0.18)  // red
    static let ai = Color(red: 0.49, green: 0.48, blue: 1.00)       // purple
    static let neutral = Color.secondary

    static let aiSoft = ai.opacity(0.14)
}

// MARK: - Pills

/// Soft-background rounded pill (直连 / 经中继 / 已下线 …).
struct QualityPill: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text)
            .font(.system(size: 10, weight: .bold))
            .foregroundColor(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Capsule().fill(color.opacity(0.14)))
    }
}

/// Small square badge (AI / 本机 / 已下线).
struct TagBadge: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text)
            .font(.system(size: 9.5, weight: .heavy))
            .foregroundColor(color)
            .padding(.horizontal, 5)
            .padding(.vertical, 1.5)
            .background(RoundedRectangle(cornerRadius: 4).fill(color.opacity(0.14)))
    }
}

/// Status dot with the mockup's soft halo ring.
struct HaloDot: View {
    let color: Color
    var size: CGFloat = 8

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: size, height: size)
            .background(
                Circle()
                    .fill(color.opacity(0.18))
                    .frame(width: size + 7, height: size + 7)
            )
    }
}

/// "即将推出" pill — designed-but-unbuilt capabilities stay visible and honest.
struct SoonBadge: View {
    var body: some View {
        Text("即将推出")
            .font(.system(size: 9.5, weight: .bold))
            .foregroundColor(.secondary)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Capsule().fill(Color.secondary.opacity(0.14)))
    }
}

// MARK: - Layout atoms

/// Uppercase section subhead ("策略" / "操作" / "设备").
struct SectionHead: View {
    let title: String
    var trailing: String? = nil

    var body: some View {
        HStack {
            Text(title)
                .font(.caption2.weight(.bold))
                .textCase(.uppercase)
                .foregroundColor(.secondary)
            Spacer()
            if let trailing {
                Text(trailing).font(.caption2).foregroundColor(.secondary)
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 4)
    }
}

/// Nav row: colored icon square + label + value/chevron (+ 即将推出).
struct NavRow: View {
    let icon: String
    let iconColor: Color
    let title: String
    var value: String? = nil
    var showsChevron = false
    var soon = false
    var action: (() -> Void)? = nil
    @State private var hovered = false

    var body: some View {
        Button {
            if !soon { action?() }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: icon)
                    .font(.system(size: 11, weight: .medium))
                    .foregroundColor(.white)
                    .frame(width: 24, height: 24)
                    .background(RoundedRectangle(cornerRadius: 6).fill(iconColor))
                Text(title)
                    .font(.system(size: 13))
                    .foregroundColor(.primary)
                Spacer()
                if soon {
                    SoonBadge()
                } else if let value {
                    Text(value).font(.system(size: 12)).foregroundColor(.secondary)
                }
                if showsChevron {
                    Text("›").font(.system(size: 14)).foregroundColor(.secondary.opacity(0.7))
                }
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
            .background(hovered ? Color.primary.opacity(0.045) : Color.clear)
        }
        .buttonStyle(.plain)
        .disabled(soon)
        .opacity(soon ? 0.8 : 1)
        .onHover { hovered = $0 }
    }
}

/// Filled search field in the Tailscale panel style.
struct PanelSearchField: View {
    @Binding var text: String

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .font(.caption)
                .foregroundColor(.secondary)
            TextField("搜索设备", text: $text)
                .textFieldStyle(.plain)
                .font(.system(size: 12.5))
            if !text.isEmpty {
                Button {
                    text = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 9)
        .padding(.vertical, 6)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.primary.opacity(0.06)))
        .padding(.horizontal, 15)
        .padding(.vertical, 6)
    }
}

/// Mockup-style labeled input: uppercase label + filled rounded field.
struct LabeledField<Content: View>: View {
    let label: String
    @ViewBuilder var content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption2.weight(.bold))
                .textCase(.uppercase)
                .foregroundColor(.secondary)
            content()
                .font(.system(size: 12.5))
                .padding(.horizontal, 9)
                .padding(.vertical, 7)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color.primary.opacity(0.06)))
        }
    }
}

extension View {
    /// Row hover highlight used across list surfaces.
    func rowHover(_ hovered: Bool) -> some View {
        background(
            RoundedRectangle(cornerRadius: 8)
                .fill(Color.primary.opacity(hovered ? 0.05 : 0))
        )
    }
}

// MARK: - Connection hero (Tailscale-style)

/// Hero 连接状态，从 NetworkExtension 解耦，便于预览与复用。
enum ConnectionState { case disconnected, connecting, connected }

/// 大号椭圆连接开关：未连接灰 / 连接中呼吸光环 / 已连接实心绿 + 实时计时。
struct ConnectionHero: View {
    let state: ConnectionState
    let connectedSince: Date?
    let aggregateText: String
    var selfAddress: String = ""
    var errorText: String = ""
    /// The text is a notice (e.g. waiting for approval), not a failure.
    var errorIsNotice: Bool = false
    let onToggle: () -> Void

    @State private var breathe = false

    private var heroColor: Color {
        switch state {
        case .connected: return LatticePalette.online
        case .connecting: return LatticePalette.online.opacity(0.55)
        case .disconnected: return LatticePalette.neutral
        }
    }

    var body: some View {
        VStack(spacing: 14) {
            if isOn {
                connectedContent
            } else {
                centeredOval
                // 非连接态的信息行：质量 + 本机地址（固定高度，避免跳动）。
                HStack(spacing: 8) {
                    if !aggregateText.isEmpty {
                        QualityPill(
                            text: aggregateText,
                            color: aggregateText == "直连" ? LatticePalette.online : LatticePalette.relay
                        )
                    }
                    if !selfAddress.isEmpty {
                        Text("本机 \(selfAddress)")
                            .font(.system(size: 12, design: .monospaced))
                            .foregroundColor(.secondary)
                    }
                }
                .frame(minHeight: 20)
                if !errorText.isEmpty {
                    Text(errorText)
                        .font(.caption)
                        .foregroundColor(errorIsNotice ? .orange : LatticePalette.blocked)
                        .multilineTextAlignment(.center)
                        .padding(.horizontal, 8)
                }
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 20)
        .background(
            RoundedRectangle(cornerRadius: 20, style: .continuous)
                .fill(cardFill)
                .shadow(color: .black.opacity(0.06), radius: 12, y: 4)
        )
        .padding(.horizontal, 15)
    }

    /// 连接时的卡片填充：整卡绿色渐变，不再内嵌胶囊。
    private var cardFill: AnyShapeStyle {
        if isOn {
            return AnyShapeStyle(LinearGradient(
                colors: [LatticePalette.online, LatticePalette.online.opacity(0.72)],
                startPoint: .topLeading, endPoint: .bottomTrailing
            ))
        }
        return AnyShapeStyle(.regularMaterial)
    }

    /// 已连接：左右布局，左=状态与计时，右=质量与本机地址。
    private var connectedContent: some View {
        HStack(spacing: 14) {
            HaloDot(color: .white, size: 18)
            VStack(alignment: .leading, spacing: 3) {
                Text("已连接")
                    .font(.system(size: 20, weight: .bold))
                    .foregroundColor(.white)
                if let since = connectedSince {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        Text(timerText(at: context.date))
                            .font(.system(size: 12, weight: .semibold, design: .monospaced))
                            .foregroundColor(.white.opacity(0.85))
                    }
                }
            }
            Spacer()
            VStack(alignment: .trailing, spacing: 5) {
                if !aggregateText.isEmpty {
                    // 绿底上用白色胶囊，最易读。
                    QualityPill(text: aggregateText, color: .white)
                }
                if !selfAddress.isEmpty {
                    Text(selfAddress)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundColor(.white.opacity(0.9))
                }
            }
        }
        .padding(.horizontal, 20)
        .frame(minHeight: 92)
        .contentShape(Rectangle())
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("断开连接")
        .accessibilityHint("点击断开 VPN")
        .onTapGesture { onToggle() }
    }

    /// 未连接/连接中：居中椭圆（呼吸环动画只在连接中出现）。
    private var centeredOval: some View {
        ZStack {
            if state == .connecting {
                Capsule()
                    .stroke(heroColor, lineWidth: 2)
                    .frame(width: 216, height: 104)
                    .scaleEffect(breathe ? 1.08 : 1.0)
                    .opacity(breathe ? 0.12 : 0.55)
            }
            Capsule()
                .fill(ovalFill)
                .frame(width: 200, height: 96)
            HStack(spacing: 14) {
                HaloDot(color: heroColor, size: 18)
                VStack(alignment: .leading, spacing: 3) {
                    Text(headline)
                        .font(.system(size: 22, weight: .bold))
                        .foregroundColor(.primary)
                    if state == .connecting {
                        Text("正在建立隧道")
                            .font(.system(size: 11))
                            .foregroundColor(.secondary)
                    }
                }
            }
        }
        .frame(width: 200, height: 96)
        .contentShape(Capsule())
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel(state == .connected ? "断开连接" : "连接网络")
        .accessibilityHint("切换 VPN 连接状态")
        .onTapGesture { onToggle() }
        .onChange(of: state) { _, newState in
            breathe = false
            if newState == .connecting {
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                    breathe = true
                }
            }
        }
        .onAppear {
            if state == .connecting {
                withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) {
                    breathe = true
                }
            }
        }
    }

    private var isOn: Bool { state == .connected }

    private var ovalFill: AnyShapeStyle {
        switch state {
        case .connected:
            return AnyShapeStyle(LinearGradient(
                colors: [LatticePalette.online, LatticePalette.online.opacity(0.72)],
                startPoint: .top, endPoint: .bottom
            ))
        default:
            return AnyShapeStyle(Color.primary.opacity(0.06))
        }
    }

    private var headline: String {
        switch state {
        case .connected: return "已连接"
        case .connecting: return "连接中…"
        case .disconnected: return "未连接"
        }
    }

    /// 计时语义（见 spec §六）：connectedSince 是本 App 会话内发现连接的时刻。
    private func timerText(at now: Date) -> String {
        guard state == .connected, let since = connectedSince else { return "" }
        let secs = max(0, Int(now.timeIntervalSince(since)))
        let h = secs / 3600, m = (secs % 3600) / 60, s = secs % 60
        return h > 0 ? String(format: "%02d:%02d:%02d", h, m, s) : String(format: "%02d:%02d", m, s)
    }
}

/// 首字母彩色头像：os 数据缺失时的 peer 头像。颜色由名称哈希决定，
/// 同一设备永远同色，一眼可辨。
struct MonogramAvatar: View {
    let name: String
    var size: CGFloat = 30

    private static let palette: [(Color, Color)] = [
        (Color(red: 0.04, green: 0.52, blue: 1.00), Color(red: 0.30, green: 0.69, blue: 1.00)),
        (Color(red: 0.13, green: 0.77, blue: 0.37), Color(red: 0.36, green: 0.85, blue: 0.55)),
        (Color(red: 0.96, green: 0.63, blue: 0.18), Color(red: 1.00, green: 0.78, blue: 0.40)),
        (Color(red: 0.49, green: 0.48, blue: 1.00), Color(red: 0.68, green: 0.67, blue: 1.00)),
        (Color(red: 0.94, green: 0.35, blue: 0.42), Color(red: 1.00, green: 0.55, blue: 0.58)),
        (Color(red: 0.20, green: 0.68, blue: 0.68), Color(red: 0.40, green: 0.82, blue: 0.82)),
    ]

    private var glyph: String {
        let base = name.isEmpty ? "?" : name
        return String(base.prefix(1)).uppercased()
    }

    private var gradient: LinearGradient {
        let idx = abs(name.unicodeScalars.reduce(0) { $0 &+ Int($1.value) }) % Self.palette.count
        let pair = Self.palette[idx]
        return LinearGradient(colors: [pair.1, pair.0], startPoint: .topLeading, endPoint: .bottomTrailing)
    }

    var body: some View {
        ZStack {
            Circle().fill(gradient)
            Text(glyph)
                .font(.system(size: size * 0.44, weight: .bold, design: .rounded))
                .foregroundColor(.white)
        }
        .frame(width: size, height: size)
    }
}

/// peer 平台图标：os 字符串（前缀、不区分大小写）→ SF Symbol。
struct PlatformIcon: View {
    let os: String
    var size: CGFloat = 30

    private var symbol: String {
        let o = os.lowercased()
        if o.hasPrefix("ios") || o.hasPrefix("iphone") || o.hasPrefix("ipad") { return "iphone" }
        if o.hasPrefix("macos") || o.hasPrefix("darwin") || o.hasPrefix("mac") { return "laptopcomputer" }
        if o.hasPrefix("windows") { return "pc" }
        if o.hasPrefix("linux") || o.hasPrefix("android") { return "desktopcomputer" }
        return "questionmark.circle"
    }

    var body: some View {
        RoundedRectangle(cornerRadius: size * 0.24)
            .fill(LatticePalette.accent.opacity(0.14))
            .frame(width: size, height: size)
            .overlay(
                Image(systemName: symbol)
                    .font(.system(size: size * 0.48, weight: .semibold))
                    .foregroundColor(LatticePalette.accent)
            )
    }
}

/// 行尾/菜单共用的收藏星标。
struct FavoriteStar: View {
    let isOn: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: isOn ? "star.fill" : "star")
                .font(.system(size: 14, weight: .semibold))
                .foregroundColor(isOn ? LatticePalette.relay : .secondary)
        }
        .buttonStyle(.plain)
    }
}
