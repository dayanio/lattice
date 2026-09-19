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
///   lattice://join?server=<url>&token=<token>[&name=<device name>]  → all fields
///   bare enrollment token                                            → token only
struct JoinPayload {
    var serverURL: String?
    var token: String?
    var name: String?

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
            let name = query.first { $0.name == "name" }?.value
            if server == nil && token == nil { return nil }
            self.serverURL = server
            self.token = token
            self.name = (name?.isEmpty == false) ? name : nil
            return
        }
        // Bare token: enrollment tokens are short opaque strings.
        guard !value.contains("://"), !value.contains(" "), value.count <= 64 else { return nil }
        self.serverURL = nil
        self.token = value
        self.name = nil
    }
}

/// The name the control plane will store for a device. Mirrors Go's
/// infra.NormalizeAppID: trim, then collapse every run of characters outside
/// [A-Za-z0-9._-] into "-". The server is authoritative; this is for showing
/// the user what their name becomes ("MacBook Pro" → "MacBook-Pro").
enum DeviceName {
    static func normalized(_ raw: String) -> String {
        raw.trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "[^A-Za-z0-9._-]+", with: "-", options: .regularExpression)
    }

    /// Non-nil when the stored name differs from what the user typed.
    static func preview(_ raw: String) -> String? {
        let n = normalized(raw)
        return (!n.isEmpty && n != raw) ? n : nil
    }
}

/// A join or connect failure in words a user can act on. The engine reports raw
/// errors ("enroll: NATS connect: ... i/o timeout", "token is invalid"); the
/// UI used to show them verbatim, or stay on "connecting".
struct JoinFailure: Equatable {
    let title: String
    let advice: String

    var display: String { advice.isEmpty ? title : "\(title)\n\(advice)" }

    static func classify(_ raw: String) -> JoinFailure {
        let e = raw.lowercased()
        func has(_ needles: String...) -> Bool { needles.contains { e.contains($0) } }

        if has("awaiting approval", "pending approval") {
            return JoinFailure(title: "等待管理员批准", advice: "管理员批准后会自动连接。")
        }
        if has("requires re-enrollment", "public key mismatch") {
            return JoinFailure(
                title: "这台设备的身份与服务器记录不一致",
                advice: "在设置里选择“重新入网”，或让管理员删除服务器上的旧设备记录。")
        }
        if has("usage limit") {
            return JoinFailure(title: "入网令牌的使用次数已用完", advice: "向管理员重新获取邀请链接。")
        }
        if has("enrollment token expired") {
            return JoinFailure(title: "入网令牌已过期", advice: "向管理员重新获取邀请链接。")
        }
        if has("token is invalid", "invalid token") {
            return JoinFailure(title: "入网令牌无效", advice: "检查令牌是否完整，或向管理员重新获取邀请链接。")
        }
        if has("exhausted") {
            return JoinFailure(title: "网络地址已分配完", advice: "联系管理员清理不用的节点或扩容。")
        }
        if has("record not found") {
            return JoinFailure(
                title: "服务器上没有这台设备的记录",
                advice: "尝试“重新入网”；如果设备名带空格，请更新到最新版本。")
        }
        if has("nats connect", ":4222") {
            return JoinFailure(
                title: "连不上信令端口（4222）",
                advice: "检查网络和代理设置；使用代理时请给服务器地址添加直连规则；确认服务器已放行 4222/tcp。")
        }
        if has("discover", "connection refused", "timed out", "i/o timeout", "could not connect",
               "not connected to the internet", "network connection was lost") {
            return JoinFailure(
                title: "连不上服务器",
                advice: "检查服务器地址和网络；使用代理时请给服务器地址添加直连规则。")
        }
        return JoinFailure(title: "连接失败", advice: raw)
    }
}
