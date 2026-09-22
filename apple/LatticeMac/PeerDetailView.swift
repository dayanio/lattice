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

/// Device detail panel (UI mockup §01 right / §04): ACL verdicts for this
/// peer, an Intent AI box that turns a natural-language description into a
/// policy, and device operations. All read-only data comes from APIs that
/// already exist — the view composes them.
struct PeerDetailView: View {
    let peer: PeerNode
    var quality: String?
    /// Merged connection metrics from the tunnel poll (latency, counters).
    var stat: PeerStat?
    /// Wide layout (two-pane main window): metric cards and a taller chart.
    var wide: Bool = false
    var onBack: () -> Void
    var onRename: (String) -> Void
    var onSetEndpoint: (String) -> Void
    var onToggleDisabled: () -> Void
    var onDelete: () -> Void

    @ObservedObject private var tunnel = TunnelManager.shared
    @State private var rttSamples: [Double] = []
    @State private var prevRx: UInt64?
    @State private var prevTx: UInt64?
    @State private var rxRate: Double = 0
    @State private var txRate: Double = 0

    @State private var policies: [LatticePolicy] = []
    @State private var isLoadingPolicies = true
    @State private var policiesError = ""

    @State private var intentText = ""
    @State private var intentPlan: IntentPlanView?
    @State private var intentBusy = false
    @State private var intentMessage = ""
    @State private var intentSucceeded = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()

            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    connectionSection

                    publicKeyRow

                    Text("策略")
                        .font(.caption2.weight(.semibold))
                        .foregroundColor(.secondary)
                        .padding(.horizontal, 16)
                        .padding(.top, 10)

                    if isLoadingPolicies {
                        HStack {
                            Spacer()
                            ProgressView().controlSize(.small).padding(.vertical, 14)
                            Spacer()
                        }
                    } else if !policiesError.isEmpty {
                        Text(policiesError)
                            .font(.caption)
                            .foregroundColor(.red)
                            .padding(16)
                    } else if aclEntries.isEmpty {
                        Text("没有策略引用该节点。未命中任何 Allow 策略的流量默认拒绝。")
                            .font(.caption)
                            .foregroundColor(.secondary)
                            .padding(16)
                    } else {
                        ForEach(aclEntries) { entry in
                            ACLEntryRow(entry: entry)
                            Divider().padding(.leading, 42)
                        }
                    }

                    aiBox
                        .padding(.top, 12)

                    Text("操作")
                        .font(.caption2.weight(.semibold))
                        .foregroundColor(.secondary)
                        .padding(.horizontal, 16)
                        .padding(.top, 14)
                }
            }

            actionRows
        }
    }

    // MARK: Header

    private var header: some View {
        VStack(alignment: .leading, spacing: 3) {
            Button {
                onBack()
            } label: {
                Text("‹ 返回设备列表")
                    .font(.caption)
                    .foregroundColor(.accentColor)
            }
            .buttonStyle(.plain)

            HStack(spacing: 6) {
                Text(peer.shownName)
                    .font(.system(.headline, design: .rounded))
                if peer.isAgent {
                    Text("AI")
                        .font(.caption2.weight(.heavy))
                        .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                        .padding(.horizontal, 5)
                        .padding(.vertical, 1)
                        .background(Color(red: 0.49, green: 0.48, blue: 1.0).opacity(0.16))
                        .cornerRadius(4)
                }
                if peer.isAgent, let sandbox = peer.sandbox, sandbox != "none", !sandbox.isEmpty {
                    Text("🔒 沙盒隔离")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                }
            }
            HStack(spacing: 6) {
                Text(peer.address)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.secondary)
                if let q = qualityLabel {
                    Text("· \(q.text)")
                        .font(.caption)
                        .foregroundColor(q.color)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .task {
            await loadPolicies()
        }
    }

    private var qualityLabel: (text: String, color: Color)? {
        switch quality {
        case "ice-ready": return ("直连", .green)
        case "relay-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
        case "closed": return ("不可达", .secondary)
        default: return nil
        }
    }

    // MARK: Connection quality & traffic

    private var currentStat: PeerStat? { stat ?? tunnel.peerStats[peer.appID] }

    private var connectionSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("连接质量")
                .font(.caption2.weight(.semibold))
                .foregroundColor(.secondary)
            if wide {
                HStack(spacing: 10) {
                    metricCard("延迟", delayText)
                    metricCard("最近握手", handshakeText)
                    metricCard("↑ 速率", rateText(txRate))
                    metricCard("↓ 速率", rateText(rxRate))
                }
            } else {
                HStack(spacing: 14) {
                    metric("延迟", delayText)
                    metric("最近握手", handshakeText)
                    metric("↑ 速率", rateText(txRate))
                    metric("↓ 速率", rateText(rxRate))
                    Spacer()
                }
            }
            rttSparkline
            if wide {
                HStack(spacing: 10) {
                    metricCard("累计发送", totalText(currentStat?.tx))
                    metricCard("累计接收", totalText(currentStat?.rx))
                }
            } else {
                HStack(spacing: 14) {
                    metric("累计发送", totalText(currentStat?.tx))
                    metric("累计接收", totalText(currentStat?.rx))
                    Spacer()
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 4)
        .onReceive(tunnel.$peerStats) { _ in sample() }
    }

    private var delayText: String {
        currentStat?.rtt.flatMap { $0 > 0 ? "\($0) ms" : nil } ?? "—"
    }

    /// 节点的 WireGuard 公钥：截断展示，一键复制全文。
    private var publicKeyRow: some View {
        Group {
            if !peer.publicKey.isEmpty {
                HStack(spacing: 6) {
                    Text("公钥")
                        .font(.caption2)
                        .foregroundColor(.secondary)
                    Text("\(peer.publicKey.prefix(10))…\(peer.publicKey.suffix(6))")
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundColor(.secondary)
                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(peer.publicKey, forType: .string)
                    } label: {
                        Image(systemName: "doc.on.doc")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                    .buttonStyle(.plain)
                    .help("复制公钥")
                }
                .padding(.horizontal, 16)
                .padding(.bottom, 4)
            }
        }
    }

    private func metric(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption2).foregroundColor(.secondary)
            Text(value).font(.system(.caption, design: .monospaced))
        }
    }

    private func metricCard(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.caption2).foregroundColor(.secondary)
            Text(value)
                .font(.system(.body, design: .rounded).weight(.semibold))
                .lineLimit(1)
                .minimumScaleFactor(0.6)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 9).fill(Color.primary.opacity(0.04)))
        .overlay(RoundedRectangle(cornerRadius: 9).strokeBorder(Color.primary.opacity(0.08)))
    }

    private var handshakeText: String {
        guard let ago = currentStat?.handshakeAgo, ago >= 0 else { return "—" }
        return ago < 60 ? "\(ago)s 前" : "\(ago / 60)m 前"
    }

    private func rateText(_ bytesPerSecond: Double) -> String {
        guard bytesPerSecond > 0 else { return "—" }
        let fmt = ByteCountFormatter()
        fmt.countStyle = .memory
        return fmt.string(fromByteCount: Int64(bytesPerSecond)) + "/s"
    }

    /// WireGuard 层计数（含隧道开销），与应用层流量略有出入。
    private func totalText(_ bytes: UInt64?) -> String {
        guard let bytes, bytes > 0 else { return "—" }
        let fmt = ByteCountFormatter()
        fmt.countStyle = .file
        return fmt.string(fromByteCount: Int64(bytes))
    }

    /// 60 样本 × 2s 的 RTT 走势；平线段表示该样本未测得（probing/中继）。
    private var rttSparkline: some View {
        GeometryReader { geo in
            Path { p in
                let values = rttSamples
                guard values.count > 1, values.max() ?? 0 > 0 else { return }
                let maxV = max(values.max() ?? 1, 1)
                for (i, v) in values.enumerated() {
                    let x = geo.size.width * CGFloat(i) / CGFloat(values.count - 1)
                    let y = geo.size.height * (1 - CGFloat(v / maxV))
                    if i == 0 {
                        p.move(to: CGPoint(x: x, y: y))
                    } else {
                        p.addLine(to: CGPoint(x: x, y: y))
                    }
                }
            }
            .stroke(Color.accentColor, lineWidth: 1.5)
        }
        .frame(height: wide ? 36 : 26)
    }

    /// 由最新快照采样：RTT 进环形缓冲，计数器差分出瞬时速率（2s 间隔）。
    private func sample() {
        guard let s = tunnel.peerStats[peer.appID] else { return }
        rttSamples.append(Double(s.rtt ?? 0))
        if rttSamples.count > 60 {
            rttSamples.removeFirst(rttSamples.count - 60)
        }
        if let rx = s.rx, let tx = s.tx {
            if let prx = prevRx, let ptx = prevTx {
                rxRate = max(0, Double(rx &- prx) / 2)
                txRate = max(0, Double(tx &- ptx) / 2)
            }
            prevRx = rx
            prevTx = tx
        }
    }

    // MARK: ACL

    private var aclEntries: [ACLEntry] {
        policies
            .filter { $0.status == nil || $0.status == "active" }
            .flatMap { $0.aclEntries(peer: peer) }
    }

    private func loadPolicies() async {
        isLoadingPolicies = true
        policiesError = ""
        defer { isLoadingPolicies = false }
        do {
            policies = try await LatticeAPI.shared.listPolicies()
        } catch {
            policiesError = "策略加载失败: \(error.localizedDescription)"
        }
    }

    // MARK: Intent AI box

    private var aiBox: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("✦ 用自然语言描述策略")
                .font(.caption.weight(.bold))
                .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))

            TextField("例如：只允许 \(peer.shownName) 访问其它节点的 80 端口", text: $intentText)
                .textFieldStyle(.roundedBorder)
                .font(.system(.caption, design: .default))

            if let plan = intentPlan {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(plan.changes ?? [], id: \.name) { change in
                        HStack(spacing: 5) {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundColor(.green)
                                .font(.caption2)
                            Text("将\(change.action == "delete" ? "删除" : "创建")策略 \(change.name ?? "")")
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                    if let risk = plan.riskLevel {
                        Text("风险等级：\(risk)")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                    Button("应用此策略") {
                        Task { await applyPlan() }
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                }
                .padding(.top, 2)
            }

            if !intentMessage.isEmpty {
                Text(intentMessage)
                    .font(.caption2)
                    .foregroundColor(intentSucceeded ? .green : .red)
            }

            HStack {
                Spacer()
                if intentBusy {
                    ProgressView().controlSize(.small)
                } else {
                    Button("生成策略") { Task { await generateIntent() } }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(intentText.isEmpty)
                }
            }
        }
        .padding(11)
        .background(Color(red: 0.49, green: 0.48, blue: 1.0).opacity(0.08))
        .overlay(
            RoundedRectangle(cornerRadius: 9)
                .strokeBorder(Color(red: 0.49, green: 0.48, blue: 1.0), style: StrokeStyle(lineWidth: 1, dash: [4]))
        )
        .cornerRadius(9)
        .padding(.horizontal, 15)
    }

    private func generateIntent() async {
        intentBusy = true
        intentMessage = ""
        intentPlan = nil
        intentSucceeded = false
        defer { intentBusy = false }
        do {
            let plan = try await LatticeAPI.shared.planIntent(intentText)
            intentPlan = plan
        } catch {
            intentMessage = "AI 生成失败（控制面可能未配置 AI）：\(error.localizedDescription)"
        }
    }

    private func applyPlan() async {
        guard let plan = intentPlan else { return }
        intentBusy = true
        intentMessage = ""
        defer { intentBusy = false }
        do {
            try await LatticeAPI.shared.applyIntent(planID: plan.id)
            intentSucceeded = true
            intentMessage = "策略已应用"
            intentPlan = nil
            intentText = ""
            await loadPolicies()
        } catch {
            intentMessage = "应用失败: \(error.localizedDescription)"
        }
    }

    // MARK: Actions

    private var actionRows: some View {
        VStack(spacing: 0) {
            Divider()
            NavRow(
                icon: "arrow.up.right",
                iconColor: Color(nsColor: .systemGray),
                title: "发送文件到此设备",
                showsChevron: true,
                soon: true
            )
            Divider().padding(.leading, 42)
            NavRow(
                icon: "clock",
                iconColor: .orange,
                title: "密钥管理",
                showsChevron: true,
                soon: true
            )
            Divider().padding(.leading, 42)

            Button {
                onRename(peer.name)
            } label: {
                actionLabel(icon: "pencil", color: .gray, title: "重命名")
            }
            .buttonStyle(.plain)
            Divider().padding(.leading, 42)
            Button {
                onSetEndpoint(peer.name)
            } label: {
                actionLabel(icon: "network", color: .blue, title: "设置静态地址")
            }
            .buttonStyle(.plain)
            Divider().padding(.leading, 42)

            Button {
                onToggleDisabled()
            } label: {
                actionLabel(icon: peer.disabled ? "checkmark.circle" : "pause.circle",
                            color: .orange,
                            title: peer.disabled ? "上线" : "下线")
            }
            .buttonStyle(.plain)
            Divider().padding(.leading, 42)

            Button {
                onDelete()
            } label: {
                actionLabel(icon: "power", color: .red, title: "移除此设备", danger: true)
            }
            .buttonStyle(.plain)

            Divider()
            HStack {
                Text(peer.name)
                Spacer()
                if !peer.lastSeen.isEmpty {
                    Text("最近在线 \(relativeTime(peer.lastSeen))")
                }
            }
            .font(.caption2)
            .foregroundColor(.secondary)
            .padding(.horizontal, 16)
            .padding(.vertical, 9)
        }
    }

    private func actionLabel(icon: String, color: Color, title: String, danger: Bool = false) -> some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .font(.caption)
                .foregroundColor(.white)
                .frame(width: 22, height: 22)
                .background(danger ? Color.red : color.opacity(0.8))
                .cornerRadius(6)
            Text(title)
                .font(.system(.body))
                .foregroundColor(danger ? .red : .primary)
            Spacer()
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .contentShape(Rectangle())
    }

    private func relativeTime(_ rfc3339: String) -> String {
        let formatter = ISO8601DateFormatter()
        guard let date = formatter.date(from: rfc3339) else {
            return rfc3339
        }
        let formatter2 = RelativeDateTimeFormatter()
        formatter2.locale = Locale(identifier: "zh_CN")
        return formatter2.localizedString(for: date, relativeTo: Date())
    }
}

struct ACLEntryRow: View {
    let entry: ACLEntry

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            ZStack {
                Circle()
                    .fill(entry.allow ? Color.green : Color.red)
                    .frame(width: 18, height: 18)
                Text(entry.allow ? "✓" : "✕")
                    .font(.caption2.weight(.heavy))
                    .foregroundColor(.white)
            }

            VStack(alignment: .leading, spacing: 1) {
                Text("\(entry.allow ? "允许" : "拦截") · \(entry.direction)")
                    .font(.system(size: 12.5, weight: .semibold))
                HStack(spacing: 3) {
                    Text("策略")
                        .font(.caption)
                        .foregroundColor(.secondary)
                    Text(entry.policy)
                        .font(.system(size: 10.5, design: .monospaced))
                        .padding(.horizontal, 4)
                        .padding(.vertical, 1)
                        .background(Color.primary.opacity(0.08))
                        .cornerRadius(4)
                    if !entry.matched {
                        Text("（未直接引用该节点）")
                            .font(.caption2)
                            .foregroundColor(.secondary)
                    }
                }
            }
            Spacer()
        }
        .padding(.horizontal, 15)
        .padding(.vertical, 8)
    }
}
