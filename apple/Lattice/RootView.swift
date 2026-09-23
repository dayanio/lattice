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

/// App root: the connection page is always the landing tab. Joining and
/// admin login are optional flows reached from the overview's empty states
/// or Settings — never a launch-blocking gate.
struct RootView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @ObservedObject private var loginCoordinator = LoginCoordinator.shared

    var body: some View {
        TabView {
            OverviewView()
                .tabItem { Label("状态", systemImage: "network") }
            AIChatView()
                .tabItem { Label("AI", systemImage: "sparkles") }
            SettingsView()
                .tabItem { Label("设置", systemImage: "gearshape") }
        }
        .onAppear { tunnel.load() }
        // A management action that needs a login asks for one here and carries
        // on once it succeeds.
        .sheet(isPresented: Binding(
            get: { loginCoordinator.isPresenting },
            set: { if !$0 { loginCoordinator.finish(success: false) } }
        )) {
            LoginView { loginCoordinator.finish(success: true) }
        }
    }
}
