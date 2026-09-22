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

import Foundation

/// A secondary page of the main panel / main window. Pure Foundation so the
/// routing rules can be unit-checked without SwiftUI. Text-entry flows (join,
/// settings, cast pairing) are sheets routed to the main window instead —
/// see UIState.
enum PanelPage: String, CaseIterable, Equatable {
    case networkSettings
    case share
    case cast
    case ai
    case account

    var title: String {
        switch self {
        case .networkSettings: return "网络设置"
        case .share: return "共享本地服务"
        case .cast: return "投屏接收"
        case .ai: return "AI 助手"
        case .account: return "账号与网络"
        }
    }
}
