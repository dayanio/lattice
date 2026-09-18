// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

import SwiftUI

/// 收藏的 peer（按 name 持久化到 UserDefaults）。已注销网络的残留收藏
/// 无需清理：首页渲染按当前 peers 过滤，孤儿项自然不可见（spec §七）。
final class FavoritesStore: ObservableObject {
    @Published private(set) var names: Set<String> = []

    private static let key = "lattice.favoritePeers"

    static let shared = FavoritesStore()

    private init() { load() }

    func isFavorite(_ name: String) -> Bool { names.contains(name) }

    func toggle(_ name: String) {
        if names.contains(name) {
            names.remove(name)
        } else {
            names.insert(name)
        }
        save()
    }

    private func load() {
        guard let data = UserDefaults.standard.data(forKey: Self.key),
              let list = try? JSONDecoder().decode([String].self, from: data) else { return }
        names = Set(list)
    }

    private func save() {
        if let data = try? JSONEncoder().encode(names.sorted()) {
            UserDefaults.standard.set(data, forKey: Self.key)
        }
    }
}
