// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

@main
struct LatticeApp: App {
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue

    var body: some Scene {
        WindowGroup {
            RootView()
                .preferredColorScheme(LatticeTheme(rawValue: theme)?.colorScheme)
        }
    }
}
