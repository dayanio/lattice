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

/// 「晶格」design language — Lattice's own visual identity: deep-space
/// surfaces, gradient accents, pulsing status rings, glowing device cards.
/// Deliberately distinct from Tailscale's flat light minimalism.
enum LatticeTheme {
    // Deep space surfaces
    static let bgTop = Color(red: 0.055, green: 0.06, blue: 0.10)
    static let bgBottom = Color(red: 0.09, green: 0.10, blue: 0.16)
    static let card = Color.white.opacity(0.055)
    static let cardHover = Color.white.opacity(0.09)
    static let cardBorder = Color.white.opacity(0.09)

    // Gradient identity (violet-blue → cyan): AI-native branding
    static let gradStart = Color(red: 0.42, green: 0.44, blue: 1.00)
    static let gradEnd = Color(red: 0.16, green: 0.82, blue: 0.86)
    static let gradient = LinearGradient(
        colors: [gradStart, gradEnd],
        startPoint: .topLeading, endPoint: .bottomTrailing
    )

    // Status
    static let online = Color(red: 0.22, green: 0.86, blue: 0.55)
    static let relay = Color(red: 1.00, green: 0.72, blue: 0.26)
    static let blocked = Color(red: 1.00, green: 0.38, blue: 0.35)
    static let ai = Color(red: 0.55, green: 0.52, blue: 1.00)

    static let textBright = Color.white.opacity(0.94)
    static let textDim = Color.white.opacity(0.55)
    static let textFaint = Color.white.opacity(0.34)

    /// Deep-space background gradient for window roots.
    static var background: some View {
        LinearGradient(colors: [bgTop, bgBottom], startPoint: .top, endPoint: .bottom)
            .ignoresSafeArea()
    }
}

/// Status indicator: glowing core + pulsing halo ring.
struct PulseStatusDot: View {
    let color: Color
    var size: CGFloat = 10
    @State private var pulsing = false

    var body: some View {
        ZStack {
            Circle()
                .stroke(color.opacity(0.30), lineWidth: 2)
                .frame(width: size + 8, height: size + 8)
            Circle()
                .fill(color)
                .frame(width: size - 2, height: size - 2)
                .shadow(color: color.opacity(0.8), radius: pulsing ? 4 : 1.5)
            Circle()
                .stroke(color.opacity(pulsing ? 0.0 : 0.4), lineWidth: 1.5)
                .frame(width: size + 8, height: size + 8)
                .scaleEffect(pulsing ? 1.25 : 0.9)
        }
        .animation(.easeInOut(duration: 1.4).repeatForever(autoreverses: true), value: pulsing)
        .onAppear { pulsing = true }
    }
}

/// Glowing device card: rounded surface, status-tinted border.
struct GlowCard<Content: View>: View {
    var glow: Color = .clear
    var hovered: Bool = false
    @ViewBuilder var content: () -> Content

    var body: some View {
        content()
            .padding(.horizontal, 12)
            .padding(.vertical, 9)
            .background(
                RoundedRectangle(cornerRadius: 12)
                    .fill(LatticeTheme.card)
            )
            .overlay(
                RoundedRectangle(cornerRadius: 12)
                    .strokeBorder(
                        hovered ? Color.white.opacity(0.22) : (glow == .clear ? LatticeTheme.cardBorder : glow.opacity(0.45)),
                        lineWidth: 1
                    )
            )
    }
}

/// Brand wordmark with the gradient identity.
struct GradientBrand: View {
    var text: String
    var font: Font = .system(size: 13, weight: .bold, design: .rounded)

    var body: some View {
        Text(text)
            .font(font)
            .foregroundStyle(
                LinearGradient(colors: [LatticeTheme.gradStart, LatticeTheme.gradEnd],
                               startPoint: .leading, endPoint: .trailing)
            )
    }
}
