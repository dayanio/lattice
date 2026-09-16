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

// MARK: - Shared design pieces (UI mockup visual language)

/// "即将推出" pill for capabilities that are designed but not yet backed by
/// the control plane. The mockup doc's honesty principle: render the UI,
/// label it clearly, never fake function.
struct SoonBadge: View {
    var body: some View {
        Text("即将推出")
            .font(.caption2.weight(.bold))
            .foregroundColor(.secondary)
            .padding(.horizontal, 6)
            .padding(.vertical, 1.5)
            .background(Color.secondary.opacity(0.14))
            .cornerRadius(100)
    }
}

/// Nav row from the mockup: colored icon square + label + trailing value or
/// chevron. Optionally disabled with a "即将推出" badge.
struct NavRow: View {
    let icon: String
    let iconColor: Color
    let title: String
    var value: String? = nil
    var showsChevron = false
    var soon = false
    var action: (() -> Void)? = nil

    var body: some View {
        Button {
            if !soon { action?() }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: icon)
                    .font(.caption)
                    .foregroundColor(.white)
                    .frame(width: 22, height: 22)
                    .background(iconColor)
                    .cornerRadius(6)
                Text(title)
                    .font(.system(.body))
                    .foregroundColor(.primary)
                Spacer()
                if soon {
                    SoonBadge()
                } else if let value {
                    Text(value)
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                if showsChevron {
                    Text("›")
                        .font(.body)
                        .foregroundColor(.secondary)
                }
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(soon)
        .opacity(soon ? 0.75 : 1)
    }
}

/// Uppercase section subhead ("策略" / "操作" in the mockup).
struct SectionHead: View {
    let title: String

    var body: some View {
        Text(title)
            .font(.caption2.weight(.bold))
            .textCase(.uppercase)
            .foregroundColor(.secondary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.top, 10)
    }
}

// MARK: - Quality / status atoms

/// Soft-background pill from the mockup: 直连 (green), 经中继 (orange), etc.
struct QualityPill: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text)
            .font(.system(size: 10, weight: .bold))
            .foregroundColor(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 1.5)
            .background(color.opacity(0.14))
            .cornerRadius(100)
    }
}

/// Status dot with the mockup's soft halo ring.
struct HaloDot: View {
    let color: Color
    var size: CGFloat = 7

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: size, height: size)
            .background(
                Circle()
                    .fill(color.opacity(0.18))
                    .frame(width: size + 6, height: size + 6)
            )
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
                .font(.system(.caption))
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
        .background(Color.primary.opacity(0.06))
        .cornerRadius(8)
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
                .font(.system(.caption, design: .default))
                .padding(.horizontal, 9)
                .padding(.vertical, 7)
                .background(Color.primary.opacity(0.06))
                .cornerRadius(8)
        }
    }
}
