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

/// Exit Node / subnet routes / MagicDNS live here per the mockup. Exit Node
/// selection is backed by real API calls; subnet-route advertising is a no-op
/// toggle (CIDR entry UI deferred — see design doc §6.2); MagicDNS stays
/// disabled until its backend ships.
struct NetworkSettingsView: View {
    var onBack: () -> Void

    @State private var candidates: [PeerNode] = []
    @State private var selectedProviders: Set<String> = []
    @State private var selfName: String = UserDefaults.standard.string(forKey: "lattice.nodeName") ?? (Host.current().localizedName ?? "")
    @State private var isLoading = true
    @State private var errorText = ""
    @State private var advertisingSubnet = false
    @State private var showingPicker = false

    var body: some View {
        VStack(spacing: 0) {
            if showingPicker {
                exitNodePicker
            } else {
                settingsList
            }
        }
        .task { await load() }
    }

    private var settingsList: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.networkSettings.title, onBack: onBack)

            settingsRow(
                title: "使用退出节点",
                desc: exitNodeDesc,
                trailing: { Text("›").font(.body).foregroundColor(.secondary) }
            )
            .onTapGesture { showingPicker = true }
            Divider().padding(.leading, 15)

            settingsRow(
                title: "广播子网路由",
                desc: "把本机所在局域网开放给 workspace 里的其它设备",
                trailing: {
                    Toggle("", isOn: Binding(
                        get: { advertisingSubnet },
                        set: { toggleAdvertiseSubnet($0) }
                    )).labelsHidden().toggleStyle(.switch).controlSize(.small)
                }
            )
            Divider().padding(.leading, 15)

            settingsRow(
                title: "MagicDNS",
                desc: "用节点名代替 overlay IP 互相访问",
                monoValue: "节点名.mac-demo.lattice.internal",
                trailing: { disabledToggle }
            )

            if !errorText.isEmpty {
                Text(errorText).font(.caption2).foregroundColor(.red)
                    .padding(.horizontal, 15).padding(.top, 6)
            }

            Spacer(minLength: 0)
            Divider()
            footerBar
        }
    }

    private var exitCandidates: [PeerNode] {
        candidates.filter { $0.advertisedRoutes.contains("0.0.0.0/0") }
    }

    private var currentExitName: String? {
        selectedProviders.first { provider in exitCandidates.contains { $0.name == provider } }
    }

    private var exitNodeDesc: String {
        if let picked = currentExitName { return "当前：\(picked)" }
        return "全部流量经由所选节点转发 · 当前：无"
    }

    private func load() async {
        isLoading = true
        errorText = ""
        do {
            let peers = try await LatticeAPI.shared.listPeers()
            candidates = peers.filter { !$0.advertisedRoutes.isEmpty }
            let selected = try await LatticeAPI.shared.listRouteSelections(selfName)
            selectedProviders = Set(selected)
            if let mine = peers.first(where: { $0.name == selfName }) {
                advertisingSubnet = !mine.advertisedRoutes.isEmpty && !mine.advertisedRoutes.contains("0.0.0.0/0")
            }
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func toggleAdvertiseSubnet(_ on: Bool) {
        advertisingSubnet = on
        Task {
            do {
                // MVP: hand-entered CIDR isn't collected by this pass — see
                // the design doc §6.2 note that auto-detecting the local
                // subnet is deferred. Advertise a placeholder-free empty
                // set when turning off; turning on with no real CIDR input
                // UI yet is intentionally a no-op beyond persisting the
                // toggle, until a CIDR entry field is added.
                if !on {
                    try await LatticeAPI.shared.setAdvertisedRoutes(selfName, routes: [])
                }
            } catch {
                errorText = "更新失败: \(error.localizedDescription)"
                advertisingSubnet = !on
            }
        }
    }

    private var exitNodePicker: some View {
        VStack(spacing: 0) {
            PageHeader(title: "选择退出节点", onBack: { showingPicker = false })
            ScrollView {
                VStack(spacing: 0) {
                    pickerRow(title: "无（关闭）", selected: currentExitName == nil) {
                        Task { await selectExitNode(nil) }
                    }
                    ForEach(exitCandidates) { peer in
                        Divider().padding(.leading, 15)
                        pickerRow(title: peer.name, selected: currentExitName == peer.name) {
                            Task { await selectExitNode(peer.name) }
                        }
                    }
                }
            }
            if !errorText.isEmpty {
                Text(errorText).font(.caption2).foregroundColor(.red)
                    .padding(.horizontal, 15).padding(.vertical, 6)
            }
        }
    }

    private func pickerRow(title: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack {
                Text(title).font(.system(size: 13))
                Spacer()
                if selected {
                    Image(systemName: "checkmark")
                        .font(.system(size: 11, weight: .semibold))
                        .foregroundColor(.accentColor)
                }
            }
            .padding(.horizontal, 15)
            .padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private func selectExitNode(_ name: String?) async {
        let oldExitNodes = selectedProviders.filter { provider in
            candidates.first(where: { c in c.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true
        }
        do {
            if let name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: name, selected: true)
            }
            for provider in oldExitNodes where provider != name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: provider, selected: false)
            }
            showingPicker = false
            await load()
        } catch {
            errorText = "选择失败: \(error.localizedDescription)"
            await load()
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
            PageHeader(title: PanelPage.share.title, onBack: onBack) { SoonBadge() }

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
