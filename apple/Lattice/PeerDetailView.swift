// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 只读 peer 详情 + 复制/收藏/重命名/停用动作（spec §四）。
struct PeerDetailView: View {
    let peer: PeerNode
    let quality: String?

    @ObservedObject private var favorites = FavoritesStore.shared
    @ObservedObject private var tunnel = TunnelManager.shared
    @State private var currentDisabled: Bool
    @State private var showingRename = false
    @State private var renameText = ""
    @State private var copied = false
    @State private var rttSamples: [Double] = []
    @State private var prevRx: UInt64?
    @State private var prevTx: UInt64?
    @State private var rxRate: Double = 0
    @State private var txRate: Double = 0

    init(peer: PeerNode, quality: String?) {
        self.peer = peer
        self.quality = quality
        _currentDisabled = State(initialValue: peer.disabled)
    }

    var body: some View {
        List {
            Section {
                VStack(spacing: 8) {
                    PlatformIcon(os: peer.os, size: 56)
                    Text(peer.shownName).font(.system(.title3, design: .rounded)).bold()
                    HStack(spacing: 6) {
                        HaloDot(color: peer.online ? LatticePalette.online : .secondary)
                        Text(peer.online ? "在线" : "离线")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
            }

            Section {
                HStack {
                    Text("IP 地址")
                    Spacer()
                    Text(peer.address)
                        .font(.system(.body, design: .monospaced))
                    Button {
                        PeerActions.copyToClipboard(peer.address)
                        copied = true
                        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
                    } label: {
                        Image(systemName: copied ? "checkmark" : "doc.on.doc")
                            .foregroundColor(LatticePalette.accent)
                    }
                    .buttonStyle(.plain)
                }
                LabeledContent("平台", value: peer.os.isEmpty ? "未知" : peer.os)
                LabeledContent("连接质量") {
                    if let quality, let pill = PeerActions.qualityPill(quality) {
                        QualityPill(text: pill.text, color: pill.color)
                    } else {
                        Text("—").foregroundColor(.secondary)
                    }
                }
                LabeledContent("最近握手", value: peer.lastHandshake)
            }

            Section {
                VStack(alignment: .leading, spacing: 10) {
                    HStack(spacing: 14) {
                        metric("延迟", delayText)
                        metric("↑ 速率", rateText(txRate))
                        metric("↓ 速率", rateText(rxRate))
                        Spacer()
                    }
                    rttSparkline
                    HStack(spacing: 14) {
                        metric("累计发送", totalText(currentStat?.tx))
                        metric("累计接收", totalText(currentStat?.rx))
                        Spacer()
                    }
                }
                .padding(.vertical, 2)
            } header: {
                Text("实时指标")
            } footer: {
                Text("WireGuard 层计数（含隧道开销），与应用层流量略有出入。")
            }

            Section {
                Button {
                    PeerActions.copyToClipboard(peer.address)
                    copied = true
                    DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { copied = false }
                } label: {
                    Label(copied ? "已复制" : "复制 IP 地址", systemImage: copied ? "checkmark" : "doc.on.doc")
                }
                Button { favorites.toggle(peer.name) } label: {
                    Label(favorites.isFavorite(peer.name) ? "取消收藏" : "收藏",
                          systemImage: favorites.isFavorite(peer.name) ? "star.fill" : "star")
                }
                Button { showingRename = true; renameText = peer.shownName } label: {
                    Label("重命名", systemImage: "pencil")
                }
                Button(role: .destructive) {
                    Task {
                        do {
                            try await PeerActions.setDisabled(peer, !currentDisabled)
                            currentDisabled.toggle()
                        } catch {
                            // 服务器未变更时保持原状态；与全 App 静默错误约定一致，不弹窗
                        }
                    }
                } label: {
                    Label(currentDisabled ? "启用" : "停用", systemImage: currentDisabled ? "checkmark.circle" : "nosign")
                }
            }
        }
        .navigationTitle(peer.shownName)
        .navigationBarTitleDisplayMode(.inline)
        .onReceive(tunnel.$peerStats) { _ in sample() }
        .alert("重命名设备", isPresented: $showingRename) {
            TextField("新名称", text: $renameText)
            Button("确定") {
                Task { try? await PeerActions.rename(peer, to: renameText) }
            }
            Button("取消", role: .cancel) {}
        }
    }

    // MARK: 实时指标

    private var currentStat: PeerStat? { tunnel.peerStats[peer.appID] }

    private var delayText: String {
        currentStat?.rtt.flatMap { $0 > 0 ? "\($0) ms" : nil } ?? "—"
    }

    private func metric(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label).font(.caption2).foregroundColor(.secondary)
            Text(value).font(.system(.caption, design: .monospaced))
        }
    }

    private func rateText(_ bytesPerSecond: Double) -> String {
        guard bytesPerSecond > 0 else { return "—" }
        let fmt = ByteCountFormatter()
        fmt.countStyle = .memory
        return fmt.string(fromByteCount: Int64(bytesPerSecond)) + "/s"
    }

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
            .stroke(LatticePalette.accent, lineWidth: 1.5)
        }
        .frame(height: 26)
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
}
