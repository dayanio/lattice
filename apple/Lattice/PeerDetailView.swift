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

/// Read-only peer detail — no rename/disable/delete (device management is
/// explicitly out of scope for iOS v1, see
/// docs/superpowers/specs/2026-09-16-ios-app-design.md).
struct PeerDetailView: View {
    let peer: PeerNode
    var quality: String?
    @State private var copied = false

    var body: some View {
        List {
            Section {
                LabeledContent("名称", value: peer.shownName)
                LabeledContent("地址", value: peer.address)
                    .font(.system(.body, design: .monospaced))
                if let q = qualityLabel {
                    LabeledContent("连接质量") {
                        Text(q.text).foregroundColor(q.color)
                    }
                }
                LabeledContent("平台", value: peer.os.isEmpty ? "未知" : peer.os)
                if !peer.lastSeen.isEmpty {
                    LabeledContent("最近在线", value: relativeTime(peer.lastSeen))
                }
            }

            if !peer.appID.isEmpty {
                Section("设备标识") {
                    Button {
                        UIPasteboard.general.string = peer.appID
                        copied = true
                        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2) { copied = false }
                    } label: {
                        HStack {
                            Text(peer.appID)
                                .font(.system(.caption, design: .monospaced))
                                .foregroundColor(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer()
                            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                                .foregroundColor(copied ? .green : .secondary)
                        }
                    }
                }
            }
        }
        .navigationTitle(peer.shownName)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var qualityLabel: (text: String, color: Color)? {
        switch quality {
        case "ice-ready": return ("直连", .green)
        case "lrp-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
        default: return nil
        }
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
