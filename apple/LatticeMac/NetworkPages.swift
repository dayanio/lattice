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

// MARK: - Network settings page (mockup §02)

/// Exit Node / subnet routes / MagicDNS live here per the mockup. All three
/// capabilities are roadmap items without control-plane support yet, so the
/// page renders its final shape with everything disabled + 即将推出 badges.
struct NetworkSettingsView: View {
    var onBack: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()

            settingsRow(
                title: "使用退出节点",
                desc: "全部流量经由所选节点转发",
                trailing: { Text("›").font(.body).foregroundColor(.secondary) }
            )
            Divider().padding(.leading, 15)

            settingsRow(
                title: "广播子网路由",
                desc: "把本机所在局域网开放给 workspace 里的其它设备",
                trailing: { disabledToggle }
            )
            Divider().padding(.leading, 15)

            settingsRow(
                title: "MagicDNS",
                desc: "用节点名代替 overlay IP 互相访问",
                monoValue: "节点名.mac-demo.lattice.internal",
                trailing: { disabledToggle }
            )

            Spacer(minLength: 0)
            Divider()
            footerBar
        }
    }

    private var disabledToggle: some View {
        ZStack {
            Capsule()
                .fill(Color.secondary.opacity(0.28))
                .frame(width: 26, height: 16)
            Circle()
                .fill(Color.white)
                .frame(width: 12, height: 12)
                .offset(x: -5)
                .shadow(radius: 1, y: 0.5)
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 3) {
            Button {
                onBack()
            } label: {
                Text("‹ 返回主面板")
                    .font(.caption)
                    .foregroundColor(.accentColor)
            }
            .buttonStyle(.plain)
            HStack(spacing: 6) {
                Text("网络设置")
                    .font(.system(.headline, design: .rounded))
                SoonBadge()
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
    }

    private func settingsRow(
        title: String,
        desc: String,
        monoValue: String? = nil,
        @ViewBuilder trailing: () -> some View
    ) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(title).font(.system(.body))
                Spacer()
                trailing()
            }
            Text(desc)
                .font(.caption)
                .foregroundColor(.secondary)
            if let monoValue {
                Text(monoValue)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundColor(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 5)
                    .background(Color.primary.opacity(0.06))
                    .cornerRadius(6)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 15)
        .padding(.vertical, 9)
        .opacity(0.85)
    }

    private var footerBar: some View {
        HStack {
            Text("Mac Demo")
            Spacer()
            Text("以上能力随阶段二 roadmap 落地")
        }
        .font(.caption2)
        .foregroundColor(.secondary)
        .padding(.horizontal, 16)
        .padding(.vertical, 9)
    }
}

// MARK: - Share page (Funnel / Serve, mockup §02 right)

/// Local-service sharing page shape. Funnel/Serve is a roadmap item: the
/// form renders disabled with an explicit 即将推出 marker, no fake links.
struct ShareView: View {
    var onBack: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            VStack(alignment: .leading, spacing: 3) {
                Button {
                    onBack()
                } label: {
                    Text("‹ 返回主面板")
                        .font(.caption)
                        .foregroundColor(.accentColor)
                }
                .buttonStyle(.plain)
                HStack(spacing: 6) {
                    Text("共享本地服务")
                        .font(.system(.headline, design: .rounded))
                    SoonBadge()
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            Divider()

            VStack(alignment: .leading, spacing: 4) {
                Text("本地端口")
                    .font(.caption2.weight(.bold))
                    .textCase(.uppercase)
                    .foregroundColor(.secondary)
                Text("127.0.0.1:8080")
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundColor(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 7)
                    .background(Color.primary.opacity(0.06))
                    .cornerRadius(7)
            }
            .padding(.horizontal, 15)
            .padding(.top, 10)

            HStack(spacing: 3) {
                Text("仅 workspace 内（Serve）")
                    .lineLimit(1)
                Text("公开访问（Funnel）")
                    .lineLimit(1)
                    .fontWeight(.semibold)
                    .padding(.vertical, 5)
                    .frame(maxWidth: .infinity)
                    .background(Color.primary.opacity(0.08))
                    .cornerRadius(6)
            }
            .font(.caption)
            .foregroundColor(.secondary)
            .padding(3)
            .background(Color.primary.opacity(0.06))
            .cornerRadius(8)
            .padding(.horizontal, 15)
            .padding(.top, 10)

            Text("生成的公开链接会显示在这里 · 该能力尚未上线")
                .font(.caption2)
                .foregroundColor(.secondary)
                .padding(.horizontal, 15)
                .padding(.top, 12)

            Spacer(minLength: 0)
            Divider()
            HStack {
                Text("Funnel")
                Spacer()
                Text("停止共享").foregroundColor(.secondary)
            }
            .font(.caption2)
            .foregroundColor(.secondary)
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
        }
    }
}
