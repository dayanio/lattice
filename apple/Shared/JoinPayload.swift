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

/// Parses a scanned or pasted join payload.
///   lattice://join?server=<url>&token=<token>   → both fields
///   bare enrollment token                        → token only
struct JoinPayload {
    var serverURL: String?
    var token: String?

    init?(_ raw: String) {
        let value = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return nil }

        if value.lowercased().hasPrefix("lattice://join") {
            guard let url = URL(string: value),
                  let query = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else {
                return nil
            }
            let server = query.first { $0.name == "server" }?.value
            let token = query.first { $0.name == "token" }?.value
            if server == nil && token == nil { return nil }
            self.serverURL = server
            self.token = token
            return
        }
        // Bare token: enrollment tokens are short opaque strings.
        guard !value.contains("://"), !value.contains(" "), value.count <= 64 else { return nil }
        self.serverURL = nil
        self.token = value
    }
}
