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

// Run with apple/Scripts/test_apple_logic.sh (no Xcode test target needed: the
// code under test is Foundation-only).

var failures = 0

func check(_ cond: Bool, _ msg: String, line: Int = #line) {
    if !cond {
        failures += 1
        print("FAIL (line \(line)): \(msg)")
    }
}

func eq<T: Equatable>(_ got: T, _ want: T, _ msg: String, line: Int = #line) {
    check(got == want, "\(msg): got \(got), want \(want)", line: line)
}

// MARK: JoinPayload

do {
    let p = JoinPayload("lattice://join?server=http://101.36.119.12:18090&token=abc123&name=MacBook%20Pro")
    eq(p?.serverURL, "http://101.36.119.12:18090", "server")
    eq(p?.token, "abc123", "token")
    eq(p?.name, "MacBook Pro", "name is percent-decoded")
}
do {
    let p = JoinPayload("lattice://join?server=http://h:1&token=t")
    eq(p?.name, nil, "name is optional")
}
do {
    let p = JoinPayload("  cloud-agent \n")
    eq(p?.token, "cloud-agent", "bare token is trimmed")
    eq(p?.serverURL, nil, "bare token has no server")
    eq(p?.name, nil, "bare token has no name")
}
check(JoinPayload("") == nil, "empty is not a payload")
check(JoinPayload("two words") == nil, "text with spaces is not a token")
check(JoinPayload("http://example.com") == nil, "a plain URL is not a token")
check(JoinPayload("lattice://join?foo=bar") == nil, "a join link with neither server nor token is rejected")
check(JoinPayload(String(repeating: "x", count: 65)) == nil, "an over-long bare string is not a token")
do {
    let p = JoinPayload("lattice://join?token=abc&name=")
    eq(p?.name, nil, "an empty name is treated as absent")
}

// MARK: DeviceName (must agree with Go's infra.NormalizeAppID)

eq(DeviceName.normalized("MacBook Pro"), "MacBook-Pro", "space")
eq(DeviceName.normalized("iPhone 15 Pro Max"), "iPhone-15-Pro-Max", "several spaces")
eq(DeviceName.normalized("  padded  "), "padded", "trim")
eq(DeviceName.normalized("a   b"), "a-b", "runs collapse")
eq(DeviceName.normalized("cloud-node_1.local"), "cloud-node_1.local", "safe characters kept")
eq(DeviceName.normalized("小明的 iPhone"), "-iPhone", "non-ASCII runs become one dash")
eq(DeviceName.preview("MacBook Pro"), "MacBook-Pro", "preview shows the stored name")
eq(DeviceName.preview("lattice-mac"), nil, "no preview when nothing changes")
eq(DeviceName.preview("   "), nil, "no preview for a blank name")

// MARK: JoinFailure

func title(_ raw: String) -> String { JoinFailure.classify(raw).title }

eq(title("enroll: NATS connect: nats connect: dial tcp 101.36.119.12:4222: i/o timeout"), "连不上信令端口（4222）", "nats timeout")
eq(title("enroll: discover NATS: Get \"http://x:18090/api/v1/discovery\": context deadline exceeded (Client.Timeout exceeded)"), "连不上服务器", "discovery")
eq(title("dial tcp 1.2.3.4:18090: connect: connection refused"), "连不上服务器", "refused")
eq(title("token is invalid"), "入网令牌无效", "invalid token")
eq(title("enrollment token expired"), "入网令牌已过期", "expired token")
eq(title("token usage limit reached (1)"), "入网令牌的使用次数已用完", "usage limit")
eq(title("public key mismatch for peer \"x\"; key rotation requires re-enrollment"), "这台设备的身份与服务器记录不一致", "key mismatch")
eq(title("start node: record not found"), "服务器上没有这台设备的记录", "record not found")
eq(title("address space 10.96.0.0/24 exhausted"), "网络地址已分配完", "exhausted")
eq(title("awaiting approval from the workspace administrator"), "等待管理员批准", "pending")
eq(title("something entirely new"), "连接失败", "fallback title")
eq(JoinFailure.classify("something entirely new").advice, "something entirely new", "fallback keeps the raw message")
eq(JoinFailure.classify("token is invalid").display, "入网令牌无效\n检查令牌是否完整，或向管理员重新获取邀请链接。", "display joins title and advice")
// Order matters: a NATS timeout must not be reported as a generic server failure.
eq(title("NATS connect: i/o timeout"), "连不上信令端口（4222）", "nats is checked before generic timeouts")
// A refresh-token message from the management API is not an enrollment token problem.
eq(title("refresh token has expired"), "连接失败", "management token expiry is not an enrollment error")
eq(title("refresh token has been revoked"), "连接失败", "management token revocation is not an enrollment error")

// MARK: Tunnel peers and the merged device list

do {
    // The shape Engine.Peers() emits.
    let json = #"[{"appId":"cloud-node-1","name":"cloud-node-1","address":"10.96.0.2","platform":"linux","state":"lrp-ready","online":true},{"appId":"iPhone15","name":"","address":"10.96.0.5","state":"probing","online":false}]"#
    let list = try? JSONDecoder().decode([TunnelPeer].self, from: Data(json.utf8))
    eq(list?.count, 2, "decodes the engine's peer array")
    eq(list?[0].node.name, "cloud-node-1", "node name")
    eq(list?[0].node.os, "linux", "platform becomes os")
    eq(list?[0].node.online, true, "online flag")
    eq(list?[1].node.name, "iPhone15", "a missing name falls back to the appId")
    eq(list?[1].node.os, "", "a missing platform is empty")
    eq(list?[1].node.online, false, "not online while probing")
}

do {
    let a = PeerNode(name: "cloud-node-1", address: "10.96.0.2", online: true, appID: "cloud-node-1")
    let noAppID = PeerNode(name: "legacy", address: "10.96.0.9", online: false)
    eq(a.id, "cloud-node-1", "id follows the appID")
    eq(noAppID.id, "legacy", "id falls back to the name")
    eq(a.id, PeerNode(name: "cloud-node-1", address: "10.96.0.2", online: false, appID: "cloud-node-1").id, "id is stable across rebuilds")
}

do {
    let tunnel = [
        PeerNode(name: "cloud-node-1", address: "10.96.0.2", online: true, appID: "cloud-node-1"),
        PeerNode(name: "iPhone15", address: "10.96.0.5", online: true, appID: "iPhone15"),
    ]
    eq(PeerListMerge.merged(api: [], tunnel: tunnel).count, 2, "without API data the tunnel's list stands alone")

    var apiPeer = PeerNode(name: "cloud-node-1", address: "10.96.0.2", online: false, appID: "cloud-node-1")
    apiPeer.displayName = "Cloud"
    let merged = PeerListMerge.merged(api: [apiPeer], tunnel: tunnel)
    eq(merged.count, 2, "a node both sides know appears once")
    eq(merged[0].displayName, "Cloud", "the API entry wins for what only it knows")
    eq(merged[0].online, false, "the API entry is kept as is")
    eq(merged[1].name, "iPhone15", "a tunnel-only node is appended")

    let byName = PeerListMerge.merged(api: [PeerNode(name: "iPhone15", address: "10.96.0.5", online: true)], tunnel: tunnel)
    eq(byName.count, 2, "an API entry without an appID still matches by name")
    eq(PeerListMerge.merged(api: [], tunnel: []).count, 0, "both empty")
}

if failures > 0 {
    print("\(failures) check(s) failed")
    exit(1)
}
print("apple logic: all checks passed")
