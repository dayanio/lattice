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
import CoreImage
import CoreImage.CIFilterBuiltins

// MARK: - Network settings page (mockup §02)

/// Exit Node / subnet routes / LatticeDNS live here per the mockup. Exit Node
/// selection is backed by real API calls; subnet-route advertising is a no-op
/// toggle (CIDR entry UI deferred — see design doc §6.2); LatticeDNS stays
/// disabled until its backend ships.
struct NetworkSettingsView: View {
    var onBack: () -> Void

    @State private var candidates: [PeerNode] = []
    @State private var selectedProviders: Set<String> = []
    // API 调用必须用归一化后的注册名（与 netmap/控制面一致），原始电脑名
    // （含空格）在服务端按名字找不到节点。
    @State private var selfName: String = DeviceName.normalized(
        UserDefaults.standard.string(forKey: "lattice.nodeName") ?? (Host.current().localizedName ?? ""))
    @State private var isLoading = true
    @State private var errorText = ""
    @State private var showingPicker = false
    @State private var showingSubnetEditor = false
    @State private var myAdvertisedRoutes: [String] = []
    @State private var draftRoutes: [String] = []
    @State private var newRoute = ""
    @State private var editorError = ""

    var body: some View {
        VStack(spacing: 0) {
            if showingPicker {
                exitNodePicker
            } else if showingSubnetEditor {
                subnetEditor
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
                title: "使用出口节点",
                desc: exitNodeDesc,
                trailing: { Text("›").font(.body).foregroundColor(.secondary) }
            )
            .onTapGesture { showingPicker = true }
            Divider().padding(.leading, 15)

            settingsRow(
                title: "广播子网路由",
                desc: advertisedDesc,
                trailing: { Text("›").font(.body).foregroundColor(.secondary) }
            )
            .onTapGesture { showingSubnetEditor = true }
            Divider().padding(.leading, 15)

            settingsRow(
                title: "LatticeDNS",
                desc: "已开启 · 用节点名代替 overlay IP 互相访问",
                monoValue: "\(DeviceName.normalized(Host.current().localizedName ?? "lattice-mac")).lattice",
                trailing: { EmptyView() }
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

    private var advertisedDesc: String {
        guard !myAdvertisedRoutes.isEmpty else {
            return "把本机所在局域网开放给 workspace 里的其它设备"
        }
        return "已广播 \(myAdvertisedRoutes.count) 条：\(myAdvertisedRoutes.joined(separator: "、"))"
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
                myAdvertisedRoutes = mine.advertisedRoutes.filter { $0 != "0.0.0.0/0" }
            }
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }


    private var exitNodePicker: some View {
        VStack(spacing: 0) {
            PageHeader(title: "选择出口节点", onBack: { showingPicker = false })
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

    // MARK: 广播子网路由编辑器

    private var subnetEditor: some View {
        VStack(spacing: 0) {
            PageHeader(title: "广播子网路由", onBack: { showingSubnetEditor = false })
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(draftRoutes, id: \.self) { route in
                        HStack {
                            Text(route).font(.system(.caption, design: .monospaced))
                            Spacer()
                            Button {
                                draftRoutes.removeAll { $0 == route }
                            } label: {
                                Image(systemName: "trash").font(.caption).foregroundColor(.red)
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 15)
                        .padding(.vertical, 7)
                    }
                    if draftRoutes.isEmpty {
                        Text("还没有广播任何子网")
                            .font(.caption).foregroundColor(.secondary)
                            .padding(.horizontal, 15)
                    }

                    HStack {
                        TextField("192.168.1.0/24", text: $newRoute)
                            .textFieldStyle(.plain)
                            .font(.system(.caption, design: .monospaced))
                        Button("添加") { addRoute() }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                    .padding(.horizontal, 15)

                    if let suggestion = SubnetRoute.localSuggestion(), !draftRoutes.contains(suggestion) {
                        Button {
                            newRoute = suggestion
                            addRoute()
                        } label: {
                            Label("使用本机子网 \(suggestion)", systemImage: "wifi")
                                .font(.caption)
                        }
                        .buttonStyle(.plain)
                        .foregroundColor(.accentColor)
                        .padding(.horizontal, 15)
                    }

                    if !editorError.isEmpty {
                        Text(editorError).font(.caption).foregroundColor(.red)
                            .padding(.horizontal, 15)
                    }

                    Text("其他设备把本机选为路由提供方后即可使用该子网；本机侧的转发能力开发中，暂不可达。")
                        .font(.caption2).foregroundColor(.secondary)
                        .padding(.horizontal, 15)
                }
                .padding(.top, 6)
            }

            Divider()
            HStack {
                Spacer()
                Button("保存") { Task { await saveRoutes() } }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(draftRoutes == myAdvertisedRoutes)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
        }
        .onAppear {
            draftRoutes = myAdvertisedRoutes
            editorError = ""
        }
    }

    private func addRoute() {
        editorError = ""
        guard let route = SubnetRoute.normalized(newRoute) else {
            editorError = "不是合法的 CIDR，例如 192.168.1.0/24"
            return
        }
        if draftRoutes.contains(route) {
            editorError = "该子网已在列表里"
            return
        }
        draftRoutes.append(route)
        newRoute = ""
    }

    private func saveRoutes() async {
        editorError = ""
        do {
            try await LatticeAPI.shared.setAdvertisedRoutes(selfName, routes: draftRoutes)
            showingSubnetEditor = false
            await load()
        } catch {
            editorError = "保存失败: \(error.localizedDescription)"
        }
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

// MARK: - Share page (对外发布, mockup §02 right)

/// 对外发布 v1（网关模式）：注册发布规则，经发布网关的 HTTP ingress 把
/// workspace 内节点的本地服务暴露出去。边界：HTTP、路径前缀路由、无 TLS。
struct ShareView: View {
    var onBack: () -> Void

    @State private var publishes: [PublishItem] = []
    @State private var isLoading = true
    @State private var errorText = ""
    @State private var showingCreate = false
    @State private var qrItem: PublishItem?
    @State private var copied = false

    /// 发布网关的对外地址：控制面主机 + 网关约定端口 8090。
    private var gatewayBase: String {
        let host = URL(string: UserDefaults.standard.string(forKey: "lattice.serverURL") ?? "")?.host ?? "127.0.0.1"
        return "http://\(host):8090"
    }

    var body: some View {
        VStack(spacing: 0) {
            PageHeader(title: PanelPage.share.title, onBack: onBack) { SoonBadge() }

            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    if isLoading {
                        HStack {
                            Spacer()
                            ProgressView().controlSize(.small).padding(.vertical, 16)
                            Spacer()
                        }
                    } else if !errorText.isEmpty {
                        Text(errorText).font(.caption).foregroundColor(.red)
                            .padding(16)
                    } else if publishes.isEmpty {
                        VStack(spacing: 8) {
                            Image(systemName: "globe")
                                .font(.system(size: 28))
                                .foregroundColor(.secondary)
                            Text("还没有对外发布")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 22)
                    } else {
                        ForEach(publishes) { item in
                            publishRow(item)
                            Divider().padding(.leading, 15)
                        }
                    }

                    Text("v1 说明：HTTP · 路径前缀路由 · 无 TLS；访问地址为发布网关的 8090 端口。")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                        .padding(.horizontal, 15)
                        .padding(.top, 8)
                        .padding(.bottom, 12)
                }
            }

            Divider()
            HStack {
                Spacer()
                Button {
                    showingCreate = true
                } label: {
                    Label("新发布", systemImage: "plus.circle")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
        }
        .task { await load() }
        .sheet(isPresented: $showingCreate) {
            CreatePublishSheet { Task { await load() } }
        }
        .sheet(item: $qrItem) { item in
            PublishQRCodeSheet(item: item, gatewayBase: gatewayBase)
        }
    }

    private func load() async {
        isLoading = publishes.isEmpty
        errorText = ""
        defer { isLoading = false }
        do {
            publishes = try await LatticeAPI.shared.listPublishes()
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
    }

    private func publishRow(_ item: PublishItem) -> some View {
        HStack(spacing: 9) {
            VStack(alignment: .leading, spacing: 1) {
                Text("/\(item.name)")
                    .font(.system(size: 12.5, weight: .semibold, design: .monospaced))
                Text("\(item.peerName):\(item.port) · \(gatewayBase)/\(item.name)/")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString("\(gatewayBase)/\(item.name)/", forType: .string)
                copied = true
                DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .font(.caption2)
                    .foregroundColor(copied ? .green : .secondary)
            }
            .buttonStyle(.plain)
            .help("复制访问链接")
            Button {
                qrItem = item
            } label: {
                Image(systemName: "qrcode")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .buttonStyle(.plain)
            .help("显示二维码")
            Button {
                Task {
                    try? await LatticeAPI.shared.deletePublish(item.name)
                    await load()
                }
            } label: {
                Image(systemName: "trash")
                    .font(.caption)
                    .foregroundColor(.red.opacity(0.7))
            }
            .buttonStyle(.plain)
            .help("下线该发布")
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
    }
}

/// 新发布表单：从已批准节点里选一个，暴露它的本地端口。
struct CreatePublishSheet: View {
    var onDone: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var port = "8080"
    @State private var peers: [PeerNode] = []
    @State private var selectedPeer = ""
    @State private var errorText = ""
    @State private var isSaving = false

    private var canSave: Bool {
        !name.isEmpty && !selectedPeer.isEmpty && Int(port) != nil && !isSaving
    }

    var body: some View {
        SheetScaffold(title: "新发布", onClose: { dismiss() }) {
            LabeledField(label: "发布名（访问路径）") {
                TextField("my-service", text: $name)
                    .textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }
            LabeledField(label: "目标节点") {
                Picker("", selection: $selectedPeer) {
                    ForEach(peers) { peer in
                        Text(peer.shownName).tag(peer.name)
                    }
                }
                .labelsHidden()
            }
            LabeledField(label: "节点上的本地端口") {
                TextField("8080", text: $port)
                    .textFieldStyle(.plain)
                    .font(.system(.caption, design: .monospaced))
            }
            Text("发布后可通过 http://<网关>:8090/\(name.isEmpty ? "发布名" : name)/ 访问该节点的服务。")
                .font(.caption2)
                .foregroundColor(.secondary)
                .fixedSize(horizontal: false, vertical: true)

            if !errorText.isEmpty {
                Text(errorText).font(.caption).foregroundColor(.red)
            }

            HStack {
                Spacer()
                if isSaving {
                    ProgressView().controlSize(.small)
                } else {
                    Button("创建发布") { Task { await save() } }
                        .buttonStyle(.borderedProminent)
                        .disabled(!canSave)
                }
            }
        }
        .task {
            peers = ((try? await LatticeAPI.shared.listPeers()) ?? [])
                .filter { $0.approvalStatus != "pending" }
            selectedPeer = peers.first?.name ?? ""
        }
    }

    private func save() async {
        guard let portNumber = Int(port) else {
            errorText = "端口必须是数字"
            return
        }
        isSaving = true
        errorText = ""
        defer { isSaving = false }
        do {
            try await LatticeAPI.shared.createPublish(name: name, peer: selectedPeer, port: portNumber)
            onDone()
            dismiss()
        } catch {
            errorText = "创建失败: \(error.localizedDescription)"
        }
    }
}

/// 发布链接二维码，供手机扫码直接访问。
struct PublishQRCodeSheet: View {
    let item: PublishItem
    let gatewayBase: String

    @Environment(\.dismiss) private var dismiss

    private var urlString: String {
        "\(gatewayBase)/\(item.name)/"
    }

    var body: some View {
        SheetScaffold(title: "扫码访问", onClose: { dismiss() }) {
            Group {
                if let image = qrImage {
                    Image(nsImage: image)
                        .interpolation(.none)
                        .resizable()
                        .scaledToFit()
                        .frame(width: 200, height: 200)
                } else {
                    Text("二维码生成失败")
                        .font(.caption)
                        .foregroundColor(.red)
                        .frame(width: 200, height: 200)
                }
            }
            .frame(maxWidth: .infinity)

            Text(urlString)
                .font(.system(.caption, design: .monospaced))
                .frame(maxWidth: .infinity)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }

    private var qrImage: NSImage? {
        guard let filter = CIFilter(name: "CIQRCodeGenerator") else { return nil }
        filter.setValue(Data(urlString.utf8), forKey: "inputMessage")
        filter.setValue("M", forKey: "inputCorrectionLevel")
        guard let output = filter.outputImage else { return nil }
        let scaled = output.transformed(by: CGAffineTransform(scaleX: 8, y: 8))
        let rep = NSCIImageRep(ciImage: scaled)
        let nsImage = NSImage(size: rep.size)
        nsImage.addRepresentation(rep)
        return nsImage
    }
}
