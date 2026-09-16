// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI
import UIKit

/// 首页长按菜单与详情页共用的 peer 动作（spec §四：动作函数只写一处）。
enum PeerActions {
    static func copyToClipboard(_ text: String) {
        UIPasteboard.general.string = text
    }

    static func rename(_ peer: PeerNode, to newName: String) async throws {
        try await LatticeAPI.shared.renamePeer(peer.name, displayName: newName)
    }

    static func setDisabled(_ peer: PeerNode, _ disabled: Bool) async throws {
        try await LatticeAPI.shared.setPeerDisabled(peer.name, disabled)
    }

    /// 质量态 → pill 文案与颜色（自原 StatusView.qualityPill 迁移）。
    static func qualityPill(_ state: String) -> (text: String, color: Color)? {
        switch state {
        case "ice-ready": return ("直连", LatticePalette.online)
        case "lrp-ready": return ("经中继", LatticePalette.relay)
        case "probing", "created": return ("连接中", LatticePalette.neutral)
        case "failed": return ("失败", LatticePalette.blocked)
        default: return nil
        }
    }
}
