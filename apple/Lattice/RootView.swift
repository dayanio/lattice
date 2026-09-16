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

/// App root: a Status/Settings tab bar once joined + logged in, otherwise
/// the full-screen join flow (see JoinView, Task 7).
struct RootView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var needsJoin = true

    var body: some View {
        TabView {
            StatusView()
                .tabItem { Label("状态", systemImage: "network") }
            SettingsView()
                .tabItem { Label("设置", systemImage: "gearshape") }
        }
        .onAppear { tunnel.load { evaluateJoinState() } }
        .fullScreenCover(isPresented: $needsJoin) {
            JoinView(onFinished: { needsJoin = false })
        }
    }

    private func evaluateJoinState() {
        needsJoin = !tunnel.isConfigured || !LatticeAPI.shared.isLoggedIn
    }
}
