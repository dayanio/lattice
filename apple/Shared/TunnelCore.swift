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

enum TunnelError: Error {
    case missingServerAddress
    case missingProviderConfiguration
}


// MARK: - Shared Models

struct PeerNode: Identifiable {
    let id = UUID()
    let name: String
    let address: String
    let online: Bool
    var displayName: String = ""
    var disabled: Bool = false
    var os: String = "macOS"
    var lastHandshake: String = "—"

    var shownName: String { displayName.isEmpty ? name : displayName }
}
