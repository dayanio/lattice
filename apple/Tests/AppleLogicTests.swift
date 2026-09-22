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
check(JoinFailure.classify("awaiting approval").isNotice, "waiting for approval is a notice, not an error")
check(!JoinFailure.classify("token is invalid").isNotice, "a real failure is not a notice")
eq(title("enroll: device access was revoked by the administrator"), "这台设备已被管理员停用", "revoked")
eq(title("something entirely new"), "连接失败", "fallback title")
eq(JoinFailure.classify("something entirely new").advice, "something entirely new", "fallback keeps the raw message")
eq(JoinFailure.classify("token is invalid").display, "入网令牌无效\n检查令牌是否完整，或向管理员重新获取邀请链接。", "display joins title and advice")
// Order matters: a NATS timeout must not be reported as a generic server failure.
eq(title("NATS connect: i/o timeout"), "连不上信令端口（4222）", "nats is checked before generic timeouts")
eq(title("Insufficient permissions"), "这个账号没有权限为设备签发入网令牌", "forbidden to issue tokens")
eq(JoinFailure.loginFailed("invalid username or password").title, "登录失败", "login failure")
eq(JoinFailure.loginFailed("invalid username or password").advice, "invalid username or password", "login failure keeps the server's message")
eq(JoinFailure.tokenNotIssued("Insufficient permissions").title, "这个账号没有权限为设备签发入网令牌", "a known cause is explained")
eq(JoinFailure.tokenNotIssued("boom").title, "没能为这台设备签发入网令牌", "an unknown cause still says what failed")
eq(JoinFailure.tokenNotIssued("boom").advice, "boom", "and keeps the raw message")
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

// MARK: AuthTokenStore (management token: Keychain first, migrate from UserDefaults)

final class FakeSecrets: SecretStoring {
    var values: [String: String] = [:]
    var failWrites = false
    func secret(_ key: String) -> String? { values[key] }
    func setSecret(_ value: String, forKey key: String) { if !failWrites { values[key] = value } }
    func deleteSecret(_ key: String) { values[key] = nil }
}

final class FakePlain: PlainStoring {
    var values: [String: String] = [:]
    func string(forKey key: String) -> String? { values[key] }
    func removeObject(forKey key: String) { values[key] = nil }
}

do {
    let secrets = FakeSecrets(), plain = FakePlain()
    let store = AuthTokenStore(secrets: secrets, legacy: plain)
    eq(store.read(), "", "nothing stored")

    plain.values[AuthTokenStore.key] = "old-token"
    eq(store.read(), "old-token", "a legacy token is still returned")
    eq(secrets.values[AuthTokenStore.key], "old-token", "and moved into the secret store")
    eq(plain.values[AuthTokenStore.key], nil, "the plain copy is removed after the move")
    eq(store.read(), "old-token", "later reads come from the secret store")
}
do {
    let secrets = FakeSecrets(), plain = FakePlain()
    secrets.failWrites = true
    plain.values[AuthTokenStore.key] = "old-token"
    let store = AuthTokenStore(secrets: secrets, legacy: plain)
    eq(store.read(), "old-token", "still usable when the Keychain write fails")
    eq(plain.values[AuthTokenStore.key], "old-token", "the plain copy is kept when the move did not stick")
}
do {
    let secrets = FakeSecrets(), plain = FakePlain()
    let store = AuthTokenStore(secrets: secrets, legacy: plain)
    store.write("new-token")
    eq(secrets.values[AuthTokenStore.key], "new-token", "write goes to the secret store")
    eq(plain.values[AuthTokenStore.key], nil, "write never leaves a plain copy")
    plain.values[AuthTokenStore.key] = "stale"
    eq(store.read(), "new-token", "the secret store wins over a stale plain value")
    eq(plain.values[AuthTokenStore.key], nil, "and the stale plain value is cleaned up")
    store.clear()
    eq(store.read(), "", "clear removes it everywhere")
}

// MARK: LoginCoordinator

@MainActor
func runLoginCoordinatorChecks() async {
    let c = LoginCoordinator()
    check(!c.isPresenting, "idle at start")

    // Two actions ask at once; both resume with the login's outcome.
    async let a = c.requestLogin()
    async let b = c.requestLogin()
    while !c.isPresenting { await Task.yield() }
    await Task.yield()
    c.finish(success: true)
    let (ra, rb) = await (a, b)
    check(ra && rb, "every waiting action resumes with success")
    check(!c.isPresenting, "the sheet closes when the login finishes")

    async let cancelled = c.requestLogin()
    while !c.isPresenting { await Task.yield() }
    await Task.yield()
    c.finish(success: false)
    let rc = await cancelled
    check(!rc, "a cancelled login resumes the action with false")

    // A dismissal after the result must not resume anything twice.
    c.finish(success: false)
    check(!c.isPresenting, "a late finish is harmless")
}

// MARK: SubnetRoutes

do {
    eq(SubnetRoute.normalized("192.168.1.5/24"), "192.168.1.0/24", "host bits are masked")
    eq(SubnetRoute.normalized(" 10.0.0.0/8 "), "10.0.0.0/8", "trimmed")
    eq(SubnetRoute.normalized("172.16.0.0/12"), "172.16.0.0/12", "rfc1918 kept")
    eq(SubnetRoute.normalized("fd00::/8"), "fd00::/8", "ipv6 kept")
    check(SubnetRoute.normalized("192.168.1.5") == nil, "a bare address is not a route")
    check(SubnetRoute.normalized("192.168.1.5/33") == nil, "prefix > 32 rejected")
    check(SubnetRoute.normalized("abc/24") == nil, "garbage rejected")
    check(SubnetRoute.normalized("0.0.0.0/0") == nil, "default route is the exit node's business")
    check(SubnetRoute.normalized("::/0") == nil, "v6 default rejected too")
}

// MARK: PanelPage

do {
    eq(PanelPage(rawValue: "networkSettings"), PanelPage.networkSettings, "raw values are stable")
    eq(PanelPage(rawValue: "share"), PanelPage.share, "raw values are stable")
    eq(PanelPage(rawValue: "cast"), PanelPage.cast, "raw values are stable")
    eq(PanelPage(rawValue: "ai"), PanelPage.ai, "raw values are stable")
    check(PanelPage(rawValue: "castPairing") == nil, "pairing is a sheet, not a page")
    check(PanelPage(rawValue: "nope") == nil, "an unknown raw value is rejected")
    for page in PanelPage.allCases {
        check(!page.title.isEmpty, "\(page.rawValue) has a title")
    }
    eq(Set(PanelPage.allCases.map(\.title)).count, PanelPage.allCases.count, "titles are unique")
}

var coordinatorDone = false
Task { @MainActor in
    await runLoginCoordinatorChecks()
    coordinatorDone = true
}
let deadline = Date().addingTimeInterval(5)
while !coordinatorDone && Date() < deadline {
    RunLoop.main.run(until: Date().addingTimeInterval(0.01))
}
check(coordinatorDone, "the coordinator checks finished")

if failures > 0 {
    print("\(failures) check(s) failed")
    exit(1)
}
print("apple logic: all checks passed")
