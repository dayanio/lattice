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
