# Node Identity View (Apple Clients) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a device's own WireGuard public key and overlay IP in the iOS Settings screen, plus a manual "regenerate key" action for suspected key compromise.

**Architecture:** The Go engine already persists its WireGuard private key to disk (fixed earlier this session, commit `6c0de5ed`) but never exposes the derived public key or a way to discard that persisted key. This plan adds a `PublicKey()` getter and a package-level `ResetIdentity()` to the Go engine, threads both through the existing NE provider-message channel and `TunnelManager`, and surfaces them in a new "设备身份" Settings section. "Regenerate key" reuses the existing leave-and-rejoin flow with one added flag rather than building a new in-place rotation protocol, because the server already rejects a same-name registration under a different key by design.

**Tech Stack:** Go (gomobile-bound engine), Swift/SwiftUI (iOS app + Network Extension), NETunnelProviderSession provider-message channel for app↔extension IPC.

## Global Constraints

- Private key is never displayed or exportable anywhere in the UI (spec: "Private key" section).
- Scope is iOS UI only (`apple/Lattice/`); the Go/extension changes are shared with macOS but macOS UI wiring is out of scope for this plan.
- No new server-side changes — the backend's existing accept/reject behavior for key rotation is unchanged.
- No new NE provider-message type — identity fields fold into the existing `"peerStates"` response envelope (one round trip, not two).
- `saveJoin`'s new `resetIdentity` parameter must default to `false` and be omittable at every existing call site.

---

### Task 1: Go engine — expose public key, add identity reset

**Files:**
- Modify: `apple/engine/engine.go`
- Test: `apple/engine/engine_test.go` (new file)

**Interfaces:**
- Produces: `func (e *Engine) PublicKey() string` — returns `""` before a key is loaded, else the base64 WireGuard public key string derived from the engine's persisted/generated private key.
- Produces: `func ResetIdentity() error` (package-level, not a method) — deletes the persisted identity file if present; returns `nil` whether or not a file existed, non-nil only on a real filesystem error.
- Consumes: the existing `wgIdentityDir()` and `loadOrCreatePrivateKey()` functions already in `apple/engine/engine.go` (added in commit `6c0de5ed` — read the live file, this plan does not repeat their bodies).

- [ ] **Step 1: Read the current file**

Read `apple/engine/engine.go` in full before editing — this plan's line references assume the file as it exists after commit `6c0de5ed`. In particular confirm the `Engine` struct (currently `cfg`, `delegate`, `tun`, `mu`, `running`, `cancel`, `done`, `stopOnce`) and the `run()` function's key-loading line:

```go
	privKey, err := loadOrCreatePrivateKey()
	if err != nil {
		e.emitError(fmt.Errorf("generate key: %w", err))
		return
	}
```

- [ ] **Step 2: Write the failing tests**

Create `apple/engine/engine_test.go`:

```go
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

package engine

import (
	"os"
	"path/filepath"
	"testing"

	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestResetIdentity_RemovesExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CFFIXED_USER_HOME", dir)

	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	identityDir := wgIdentityDir()
	if err := os.MkdirAll(identityDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(identityDir, "wg-identity.key")
	if err := os.WriteFile(path, []byte(key.String()), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ResetIdentity(); err != nil {
		t.Fatalf("ResetIdentity() = %v, want nil", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected key file to be removed, stat err = %v", statErr)
	}
}

func TestResetIdentity_NoFilePresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CFFIXED_USER_HOME", dir)

	if err := ResetIdentity(); err != nil {
		t.Fatalf("ResetIdentity() with no file present = %v, want nil", err)
	}
}

func TestEnginePublicKey_EmptyBeforeKeyLoaded(t *testing.T) {
	e := &Engine{}
	if got := e.PublicKey(); got != "" {
		t.Fatalf("PublicKey() before any key is loaded = %q, want \"\"", got)
	}
}

func TestEnginePublicKey_ReturnsDerivedKey(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	e := &Engine{privKey: key}
	want := key.PublicKey().String()
	if got := e.PublicKey(); got != want {
		t.Fatalf("PublicKey() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail to compile**

Run: `cd /Users/francis/workspc/lattice && GOTOOLCHAIN=auto go test ./apple/engine/... -run TestResetIdentity -v`
Expected: FAIL — compile error, `ResetIdentity` and `Engine.privKey`/`Engine.PublicKey` undefined.

- [ ] **Step 4: Add the `privKey` field and store it in `run()`**

In `apple/engine/engine.go`, change the `Engine` struct to add one field:

```go
type Engine struct {
	cfg      engineConfig
	delegate EngineDelegate
	tun      *packetTUN
	privKey  wgtypes.Key

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}
```

In `run()`, immediately after the existing key-loading block (the `if err != nil { e.emitError...; return }` guard shown in Step 1), add:

```go
	e.mu.Lock()
	e.privKey = privKey
	e.mu.Unlock()
```

- [ ] **Step 5: Add `PublicKey()` and `ResetIdentity()`**

Add these two functions near `wgIdentityDir()`/`loadOrCreatePrivateKey()` at the bottom of `apple/engine/engine.go`:

```go
// PublicKey returns this engine's current WireGuard public key as a base64
// string, or "" if the engine hasn't loaded/generated its identity yet
// (before run() reaches the key-loading step, or Start was never called).
func (e *Engine) PublicKey() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var zero wgtypes.Key
	if e.privKey == zero {
		return ""
	}
	return e.privKey.PublicKey().String()
}

// ResetIdentity deletes the persisted WireGuard identity file, if any, so
// the next engine Start generates and persists a brand-new one. It is a
// package-level function, not an Engine method, because it must be
// callable before any Engine exists — the Swift side calls this ahead of
// constructing a fresh Engine for a user-initiated identity reset (see
// PacketTunnelProvider.startTunnel's resetIdentity flag handling).
func ResetIdentity() error {
	dir := wgIdentityDir()
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "wg-identity.key")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd /Users/francis/workspc/lattice && GOTOOLCHAIN=auto go test ./apple/engine/... -v`
Expected: PASS — all four new tests plus every pre-existing test in the package (`TestPacketTUN_*`, `TestComputeExtraRoutes_*`).

- [ ] **Step 7: Build check**

Run: `cd /Users/francis/workspc/lattice && GOTOOLCHAIN=auto go build ./apple/engine/... && GOTOOLCHAIN=auto go vet ./apple/engine/...`
Expected: both exit 0, no output.

- [ ] **Step 8: Rebuild both gomobile frameworks and record the exact generated Swift symbol for `ResetIdentity`**

Run:
```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
grep -n "ResetIdentity" apple/Frameworks/iOS/LatticeCore.xcframework/ios-arm64/LatticeCore.framework/Headers/LatticeEngine.objc.h
```

Both `gomobile bind` commands must exit 0. The `grep` prints the generated
C declaration for the package-level `ResetIdentity` function — expected
shape (exact name may differ slightly; use whatever this grep actually
prints, not this example, when writing Task 2):

```
FOUNDATION_EXPORT BOOL LatticeEngineResetIdentity(NSError* _Nullable* _Nullable error);
```

Record this exact line in your task report — Task 2 needs it verbatim to
call the function correctly from Swift (a trailing `NSError**` parameter
like this is imported into Swift as a `throws` function, e.g.
`try LatticeEngineResetIdentity()` if the grep confirms that exact name).

- [ ] **Step 9: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/engine/engine.go apple/engine/engine_test.go
git commit -s -m "feat(apple): expose engine public key and identity reset"
git push
```

(Do not commit the rebuilt `.xcframework` output — check `git status` first;
if the frameworks directory is git-ignored, nothing to add there. If it is
tracked, add it too.)

---

### Task 2: Tunnel extensions — serve identity, honor reset flag

**Files:**
- Modify: `apple/LatticeTunnel/PacketTunnelProvider.swift` (iOS)
- Modify: `apple/LatticeTunnelMac/PacketTunnelProvider.swift` (macOS)

**Interfaces:**
- Consumes: `engine?.publicKey() -> String` (gomobile-exported Swift method on `LatticeEngineEngine`, mirroring the existing `engine?.start()`/`engine?.stop()`/`engine?.sendPacket()` calls already in both files) and the exact `ResetIdentity` Swift call recorded in Task 1 Step 8's report.
- Produces: `handleAppMessage`'s `"peerStates"` response now includes `"publicKey"` (String) and `"overlayIP"` (String) keys alongside the existing `"peerStates"`/`"lastError"` keys — Task 3's `ProviderSnapshot` decode depends on these exact key names.
- Produces: `startTunnel` accepts an optional `"resetIdentity"` boolean in `providerConfiguration` — Task 3's `saveJoin`/`createProfile` changes depend on this exact key name.

- [ ] **Step 1: Read both current files and Task 1's report**

Read `apple/LatticeTunnel/PacketTunnelProvider.swift`, `apple/LatticeTunnelMac/PacketTunnelProvider.swift`, and Task 1's report file for the exact `ResetIdentity` Swift call signature discovered in its Step 8. The two Swift files are structurally identical for the pieces this task touches (the macOS one additionally has `TunnelLog.write(...)` diagnostic calls the iOS one lacks — preserve that difference, don't add `TunnelLog` calls to the iOS file or remove them from the macOS file).

- [ ] **Step 2: Extend `handleAppMessage`'s `"peerStates"` response in both files**

In each file, find:

```swift
        if String(data: messageData, encoding: .utf8) == "peerStates" {
            // latestPeerStates 本身是 map 的 JSON 字符串——先解成对象再装进
            // 信封，避免把整个 map 当字符串二次编码（App 端会解码失败）。
            let states = (try? JSONSerialization.jsonObject(with: Data(latestPeerStates.utf8))) as? [String: String] ?? [:]
            let snapshot: [String: Any] = ["peerStates": states, "lastError": latestError]
            completionHandler?(try? JSONSerialization.data(withJSONObject: snapshot))
            return
        }
```

Replace the `snapshot` line with:

```swift
            let snapshot: [String: Any] = [
                "peerStates": states,
                "lastError": latestError,
                "publicKey": engine?.publicKey() ?? "",
                "overlayIP": currentOverlayIP,
            ]
```

Everything else in this block (the guard, the comment, `completionHandler?(...)`, `return`) stays exactly as-is.

- [ ] **Step 3: Honor `resetIdentity` in `startTunnel` in both files**

In each file's `startTunnel(options:completionHandler:)`, find the guard that extracts `serverURL`/`token`:

```swift
        guard let pc = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration,
              let serverURL = pc["serverURL"] as? String,
              let token = pc["token"] as? String else {
```

Immediately after that guard block closes (right before `let name = ...`), insert:

```swift
        if pc["resetIdentity"] as? Bool == true {
            _ = try? LatticeEngineResetIdentity()
        }
```

Use the exact function-call spelling from Task 1's report instead of
`LatticeEngineResetIdentity()` if the report recorded a different name.
The error is intentionally discarded — a missing key file is expected and
already treated as success on the Go side (`os.IsNotExist` → `nil`), so a
non-nil error here would only mean something unrelated went wrong and
should not block the join attempt itself.

- [ ] **Step 4: Build check — iOS**

Run:
```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build
```
Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 5: Build check — macOS**

Run:
```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug build
```
Expected: `** BUILD SUCCEEDED **`. If the macOS scheme name differs from
`LatticeMac`, run `xcodebuild -project LatticeApple.xcodeproj -list` first
to confirm the exact scheme name.

- [ ] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/LatticeTunnel/PacketTunnelProvider.swift apple/LatticeTunnelMac/PacketTunnelProvider.swift
git commit -s -m "feat(apple): serve identity over provider-message channel, honor reset flag"
git push
```

---

### Task 3: TunnelManager — poll identity, thread reset flag through saveJoin

**Files:**
- Modify: `apple/Shared/TunnelManager.swift`

**Interfaces:**
- Consumes: the extended `"peerStates"` response from Task 2 (`publicKey`, `overlayIP` string fields).
- Produces: `@Published private(set) var localPublicKey: String` and `@Published private(set) var localOverlayIP: String` on `TunnelManager` — Task 4's Settings UI reads these directly (`tunnel.localPublicKey`, `tunnel.localOverlayIP`).
- Produces: `func saveJoin(serverURL: String, token: String, name: String, resetIdentity: Bool = false, completion: ((String?) -> Void)? = nil)` — Task 4's reset flow calls this with `resetIdentity: true`; both existing call sites (`apple/Lattice/JoinView.swift:179`, `apple/LatticeMac/ContentView.swift:783`) are unaffected since they omit the new parameter and it defaults to `false`.

- [ ] **Step 1: Read the current file**

Read `apple/Shared/TunnelManager.swift` in full. This plan's snippets below assume the file exactly as shown in this session's most recent read (published properties block, `saveJoin`/`createProfile`, and the `ProviderSnapshot` struct + `pollPeerStates()`).

- [ ] **Step 2: Add the two new published properties**

Immediately after the existing:

```swift
    @Published private(set) var peerStates: [String: String] = [:]
```

add:

```swift
    /// This device's own WireGuard public key, polled from the running
    /// extension — "" until the engine has loaded/generated its identity.
    @Published private(set) var localPublicKey: String = ""
    /// This device's overlay IP, polled from the running extension.
    @Published private(set) var localOverlayIP: String = ""
```

- [ ] **Step 3: Extend `ProviderSnapshot` and the poll handler**

Find:

```swift
    private struct ProviderSnapshot: Codable {
        let peerStates: [String: String]
        let lastError: String?
    }
```

Change to:

```swift
    private struct ProviderSnapshot: Codable {
        let peerStates: [String: String]
        let lastError: String?
        let publicKey: String?
        let overlayIP: String?
    }
```

Find, inside `pollPeerStates()`:

```swift
                    guard let snap = try? JSONDecoder().decode(ProviderSnapshot.self, from: data) else { return }
                    self.peerStates = snap.peerStates
```

Add two lines right after `self.peerStates = snap.peerStates`:

```swift
                    if let publicKey = snap.publicKey, !publicKey.isEmpty {
                        self.localPublicKey = publicKey
                    }
                    if let overlayIP = snap.overlayIP, !overlayIP.isEmpty {
                        self.localOverlayIP = overlayIP
                    }
```

(Falling back to keeping the previous value rather than clobbering with
an empty string protects against a response from an older/mismatched
extension build during a rollout, or a transient poll where the engine
hasn't loaded its key yet.)

- [ ] **Step 4: Add `resetIdentity` to `saveJoin` and `createProfile`**

Find:

```swift
    func saveJoin(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            let stale = (managers ?? []).filter {
                ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
            }
            let group = DispatchGroup()
            for manager in stale {
                group.enter()
                manager.removeFromPreferences { _ in group.leave() }
            }
            group.notify(queue: .main) {
                self.createProfile(serverURL: serverURL, token: token, name: name, completion: completion)
            }
        }
    }
```

Replace with:

```swift
    func saveJoin(serverURL: String, token: String, name: String, resetIdentity: Bool = false, completion: ((String?) -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            let stale = (managers ?? []).filter {
                ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
            }
            let group = DispatchGroup()
            for manager in stale {
                group.enter()
                manager.removeFromPreferences { _ in group.leave() }
            }
            group.notify(queue: .main) {
                self.createProfile(serverURL: serverURL, token: token, name: name, resetIdentity: resetIdentity, completion: completion)
            }
        }
    }
```

Find:

```swift
    private func createProfile(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil) {
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = serverURL
        proto.providerConfiguration = [
            "serverURL": serverURL,
            "token": token,
            "name": name,
        ]
```

Replace with:

```swift
    private func createProfile(serverURL: String, token: String, name: String, resetIdentity: Bool = false, completion: ((String?) -> Void)? = nil) {
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = serverURL
        var config: [String: Any] = [
            "serverURL": serverURL,
            "token": token,
            "name": name,
        ]
        if resetIdentity {
            config["resetIdentity"] = true
        }
        proto.providerConfiguration = config
```

(The rest of `createProfile`'s body — building `mgr`, `saveToPreferences`,
etc. — is unchanged; only the `providerConfiguration` construction and the
function signature change.)

- [ ] **Step 5: Build check — iOS and macOS**

Run:
```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug build
```
Expected: `** BUILD SUCCEEDED **` for both. This also verifies the two
existing `saveJoin(...)` call sites in `JoinView.swift`/`ContentView.swift`
still compile unchanged.

- [ ] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Shared/TunnelManager.swift
git commit -s -m "feat(apple): poll local identity, thread resetIdentity through saveJoin"
git push
```

---

### Task 4: Settings UI — identity display and regenerate action

**Files:**
- Modify: `apple/Lattice/JoinView.swift`
- Modify: `apple/Lattice/SettingsView.swift`

**Interfaces:**
- Consumes: `tunnel.localPublicKey: String`, `tunnel.localOverlayIP: String` (Task 3), `TunnelManager.shared.saveJoin(serverURL:token:name:resetIdentity:completion:)` (Task 3), `PeerActions.copyToClipboard(_ text: String)` (already exists in `apple/Lattice/PeerActions.swift`), `JoinMode` enum and `JoinView`'s existing `init(onFinished:mode:)` (already exists in `apple/Lattice/JoinView.swift`).
- Produces: `JoinView.init(onFinished:mode:initialResetIdentity:)` — a new `initialResetIdentity: Bool = false` parameter, defaulting to `false` so every other `JoinView(...)` call site in the codebase (`OverviewView.swift`, `SettingsView.swift`'s existing join sheet) is unaffected.

- [ ] **Step 1: Read the current files**

Read `apple/Lattice/JoinView.swift` and `apple/Lattice/SettingsView.swift` in full — this plan's snippets assume both exactly as they exist after commit `ee519ef3` (JoinView's `JoinMode: Identifiable` + `init(onFinished:mode:)`) and the current `SettingsView.swift` (with its existing "网络"/"退出网络" sections and `leaveNetwork()` method).

- [ ] **Step 2: Add `initialResetIdentity` to `JoinView`**

Find:

```swift
struct JoinView: View {
    var onFinished: () -> Void
    var mode: JoinMode = .manual

    @State private var useScanner: Bool
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var networkError = ""
    @State private var scannerError = ""

    init(onFinished: @escaping () -> Void, mode: JoinMode = .manual) {
        self.onFinished = onFinished
        self.mode = mode
        _useScanner = State(initialValue: mode == .scan)
    }
```

Replace with:

```swift
struct JoinView: View {
    var onFinished: () -> Void
    var mode: JoinMode = .manual
    /// When true, a successful join also discards this device's persisted
    /// WireGuard identity first (see SettingsView's "重新生成密钥" action).
    /// Every other call site omits this and gets ordinary rejoin behavior.
    var initialResetIdentity: Bool = false

    @State private var useScanner: Bool
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var networkError = ""
    @State private var scannerError = ""

    init(onFinished: @escaping () -> Void, mode: JoinMode = .manual, initialResetIdentity: Bool = false) {
        self.onFinished = onFinished
        self.mode = mode
        self.initialResetIdentity = initialResetIdentity
        _useScanner = State(initialValue: mode == .scan)
    }
```

- [ ] **Step 3: Thread `initialResetIdentity` into `saveAndConnect()`**

Find:

```swift
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: joinToken, name: deviceName) { err in
```

Replace with:

```swift
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: joinToken, name: deviceName, resetIdentity: initialResetIdentity) { err in
```

- [ ] **Step 4: Build check — JoinView compiles standalone**

Run:
```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build
```
Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 5: Commit JoinView change**

```bash
cd /Users/francis/workspc/lattice
git add apple/Lattice/JoinView.swift
git commit -s -m "feat(apple): JoinView accepts an initial identity-reset flag"
git push
```

- [ ] **Step 6: Add the "设备身份" section to SettingsView**

Find:

```swift
struct SettingsView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var showingLeaveConfirm = false
    @State private var showingLogin = false
    @State private var showingJoin = false
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue
    @AppStorage("lattice.authToken") private var authToken = ""
```

Replace with:

```swift
struct SettingsView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var showingLeaveConfirm = false
    @State private var showingLogin = false
    @State private var showingJoin = false
    @State private var pendingIdentityReset = false
    @State private var showingResetIdentityConfirm = false
    @State private var showedCopiedFeedback = false
    @AppStorage("lattice.theme") private var theme = LatticeTheme.system.rawValue
    @AppStorage("lattice.authToken") private var authToken = ""

    private var truncatedPublicKey: String {
        let key = tunnel.localPublicKey
        guard key.count > 16 else { return key }
        return "\(key.prefix(8))…\(key.suffix(8))"
    }
```

Find:

```swift
                Section("网络") {
                    if tunnel.isConfigured {
                        NavigationLink("退出节点") { ExitNodeView() }
                        LabeledContent("本机节点", value: UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "—")
                    } else {
                        Button { showingJoin = true } label: {
                            Label("加入网络", systemImage: "qrcode.viewfinder")
                        }
                    }
                }

                Section("偏好") {
```

Replace with (inserting the new section; the existing join button is
untouched — this file's only join entry point already always uses
`mode: .scan` in its sheet, so unlike `OverviewView` there is no second
mode value in play here and nothing to make atomic):

```swift
                Section("网络") {
                    if tunnel.isConfigured {
                        NavigationLink("退出节点") { ExitNodeView() }
                        LabeledContent("本机节点", value: UserDefaults.standard.string(forKey: "lattice.nodeName") ?? "—")
                    } else {
                        Button { showingJoin = true } label: {
                            Label("加入网络", systemImage: "qrcode.viewfinder")
                        }
                    }
                }

                if tunnel.isConfigured {
                    Section("设备身份") {
                        Button {
                            PeerActions.copyToClipboard(tunnel.localPublicKey)
                            showedCopiedFeedback = true
                            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
                                showedCopiedFeedback = false
                            }
                        } label: {
                            HStack {
                                Text("公钥")
                                    .foregroundColor(.primary)
                                Spacer()
                                Text(showedCopiedFeedback ? "已复制" : truncatedPublicKey)
                                    .font(.system(.body, design: .monospaced))
                                    .foregroundColor(.secondary)
                            }
                        }
                        LabeledContent("Overlay IP", value: tunnel.localOverlayIP)
                        Text("私钥已在本机安全存储，不会显示或导出。")
                            .font(.caption)
                            .foregroundColor(.secondary)
                        Button("重新生成密钥", role: .destructive) {
                            showingResetIdentityConfirm = true
                        }
                    }
                }

                Section("偏好") {
```

- [ ] **Step 7: Add the confirmation dialog and `startIdentityReset()`**

Find the existing `leaveNetwork()`-related `.confirmationDialog` on the
main `List`:

```swift
            .confirmationDialog(
                "退出网络？",
                isPresented: $showingLeaveConfirm,
                titleVisibility: .visible
            ) {
                Button("退出网络", role: .destructive) { leaveNetwork() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("需要重新扫码或输入 token 才能再次加入。")
            }
        }
    }
```

Replace with (adding a second `.confirmationDialog` modifier alongside the
existing one):

```swift
            .confirmationDialog(
                "退出网络？",
                isPresented: $showingLeaveConfirm,
                titleVisibility: .visible
            ) {
                Button("退出网络", role: .destructive) { leaveNetwork() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("需要重新扫码或输入 token 才能再次加入。")
            }
            .confirmationDialog(
                "重新生成密钥？",
                isPresented: $showingResetIdentityConfirm,
                titleVisibility: .visible
            ) {
                Button("重新生成", role: .destructive) { startIdentityReset() }
                Button("取消", role: .cancel) {}
            } message: {
                Text("这会清除当前设备身份，需要重新扫码或输入令牌入网。仅在怀疑密钥泄露时使用。")
            }
        }
    }
```

Then find the existing `leaveNetwork()` method:

```swift
    private func leaveNetwork() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            UserDefaults.standard.removeObject(forKey: "lattice.authToken")
            UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
            UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
            KeychainStore.delete("lattice.password")
            tunnel.load()
        }
    }
}
```

Add a new method right after it (before the closing `}` of the struct):

```swift
    private func leaveNetwork() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            UserDefaults.standard.removeObject(forKey: "lattice.authToken")
            UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
            UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
            KeychainStore.delete("lattice.password")
            tunnel.load()
        }
    }

    /// 重新生成密钥：清空当前入网状态后弹出加入页面，这一次的加入请求会带上
    /// resetIdentity，让隧道进程先删掉本地持久化的私钥文件再重新注册。
    private func startIdentityReset() {
        tunnel.disconnect()
        tunnel.removeProfile {
            UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
            UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
            UserDefaults.standard.removeObject(forKey: "lattice.authToken")
            UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
            UserDefaults.standard.removeObject(forKey: "lattice.workspaceId")
            KeychainStore.delete("lattice.password")
            tunnel.load()
            pendingIdentityReset = true
            showingJoin = true
        }
    }
}
```

- [ ] **Step 8: Pass `initialResetIdentity` into the join sheet**

Find:

```swift
            .sheet(isPresented: $showingJoin) {
                JoinView(onFinished: { showingJoin = false }, mode: .scan)
            }
```

Replace with:

```swift
            .sheet(isPresented: $showingJoin) {
                JoinView(onFinished: {
                    showingJoin = false
                    pendingIdentityReset = false
                }, mode: .scan, initialResetIdentity: pendingIdentityReset)
            }
```

- [ ] **Step 9: Build check — iOS**

Run:
```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build
```
Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 10: Manual on-device verification**

No XCTest UI harness exists in this project — verify by running on-device
(this session already has working tooling for this: `xcrun devicectl
device process launch --console` to watch NSLog/`onEvent` output,
`idevicesyslog` for extension-launch traces, and direct `sqlite3` queries
against the standalone test `latticed` instance's DB at
`/tmp/lattice-run-test/lattice.db` to cross-check the registered public
key — pick whichever combination is useful in the moment, this plan
doesn't prescribe exact commands here):

1. Build and install to device. Open Settings while joined — confirm
   "设备身份" section shows a plausible 44-character public key
   (truncated) and a `10.96.0.x` overlay IP.
2. Tap the public-key row — confirm it briefly shows "已复制" and that
   pasting elsewhere yields the full untruncated key.
3. Cross-check: query `select public_key from t_peer where app_id =
   '<device name>';` against the test `lattice.db` and confirm it matches
   the full key you just copied (strip the `…` truncation logic mentally,
   or compare prefix/suffix).
4. Tap "重新生成密钥", confirm, complete the rejoin with a valid
   server+token. Confirm the app ends up joined again, and that the newly
   displayed public key differs from the one recorded in step 3 (rotation
   actually happened, not a no-op).

- [ ] **Step 11: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Lattice/SettingsView.swift
git commit -s -m "feat(apple): show device identity in Settings, add regenerate-key action"
git push
```
