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

    private var isOn: Bool { state == .connected }

    var body: some View {
        VStack(spacing: 12) {
            if isOn {
                connectedContent
            } else {
                compactOfflineContent
                // 质量与本机地址信息行（固定高度，避免跳动）。
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
                .frame(minHeight: 18)
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
        .padding(.vertical, isOn ? 16 : 12)
        .background(
            RoundedRectangle(cornerRadius: 20, style: .continuous)
                .fill(cardFill)
                .shadow(color: .black.opacity(0.06), radius: 12, y: 4)
        )
        .padding(.horizontal, 15)
    }

    // MARK: 已连接

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
            // 断开走显式开关：深色小胶囊承载，整卡不再可点——
            // 之前整卡都是断开热区，一个误触就断网。
            HStack(spacing: 6) {
                Text("已连接")
                    .font(.caption2)
                    .foregroundColor(.white.opacity(0.9))
                Toggle("断开连接", isOn: disconnectBinding)
                    .labelsHidden()
                    .tint(.white.opacity(0.35))
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(Capsule().fill(.black.opacity(0.22)))
        }
        .padding(.horizontal, 20)
        .frame(minHeight: 76)
    }

    /// 开关语义：连接态恒为 on；拨到 off 即断开。断开后切到未连接形态。
    private var disconnectBinding: Binding<Bool> {
        Binding(get: { true }, set: { newValue in
            if !newValue { onToggle() }
        })
    }

    private var cardFill: AnyShapeStyle {
        isOn ? AnyShapeStyle(LinearGradient(
            colors: [LatticePalette.online, LatticePalette.online.opacity(0.72)],
            startPoint: .topLeading, endPoint: .bottomTrailing
        )) : AnyShapeStyle(.regularMaterial)
    }

    // MARK: 未连接 / 连接中（紧凑行，替代原 200x96 大椭圆）

    private var compactOfflineContent: some View {
        HStack(spacing: 12) {
            if state == .connecting {
                ProgressView()
                VStack(alignment: .leading, spacing: 2) {
                    Text("连接中…").font(.system(.headline, design: .rounded))
                    Text("正在建立隧道").font(.caption).foregroundColor(.secondary)
                }
            } else {
                HaloDot(color: LatticePalette.neutral, size: 14)
                VStack(alignment: .leading, spacing: 2) {
                    Text("未连接").font(.system(.headline, design: .rounded))
                    Text("一键连入 mesh 网络").font(.caption).foregroundColor(.secondary)
                }
            }
            Spacer()
            if state == .disconnected {
                Button {
                    onToggle()
                } label: {
                    Text("连接")
                        .font(.subheadline.weight(.semibold))
                        .padding(.horizontal, 18)
                        .padding(.vertical, 8)
                        .background(Capsule().fill(LatticePalette.accent))
                        .foregroundColor(.white)
                }
            }
        }
        .padding(.horizontal, 14)
    }

    // MARK: 计时

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
