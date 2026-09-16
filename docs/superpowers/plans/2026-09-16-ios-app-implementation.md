# iOS App Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status (2026-09-16 evening):** Tasks 1–7 all implemented and committed (`3ba3c2f9`…`6f5fcdbe`). Final iOS code build verified (simulator destination). macOS regression build re-run after the shared-code moves: BUILD SUCCEEDED. App installs and launches in the simulator without crashing. Join screen driven end-to-end via accessibility automation: URL + enrollment-token fields accept input, tapping 加入网络 surfaces a red "IPC failed" error — this is the engine's WireGuard device IPC (`wferrors.IPCError`) failing because NetworkExtension tunnel providers cannot run in the iOS Simulator (documented Apple limitation), not an app bug. The full join → login → peer-list walkthrough therefore requires a real iPhone (Task 7 Step 5 stays unchecked for that reason).

**Goal:** Build the iOS app's first real version per `docs/superpowers/specs/2026-09-16-ios-app-design.md`: join a network (QR or paste), log into the management API, see connection status and the peer list with live quality, view a peer's read-only details, and pick an Exit Node.

**Architecture:** Extract the ~90%-duplicated business logic already sitting in `apple/LatticeMac/` (TunnelManager, LatticeAPI, DesignComponents, join-payload parsing) into `apple/Shared/`, which both the `Lattice` (iOS) and `LatticeMac` targets already include via `project.yml`'s `sources: [..., Shared]`. Then write new iOS screens on top: a `TabView` (Status / Settings) replacing the current single-form `ContentView.swift`, a read-only peer detail push screen, an Exit Node picker mirroring `NetworkSettingsView`'s data flow, and a 2-step full-screen join flow (VPN profile join, then admin login — the peer-list API requires an admin Bearer token, not the enrollment token, confirmed against the live backend this session).

**Tech Stack:** SwiftUI, NetworkExtension (`NETunnelProviderManager`), AVFoundation (`AVCaptureSession`/`AVCaptureMetadataOutput` for iOS QR scanning), XcodeGen (`apple/project.yml`).

## Global Constraints

- Every new/moved Swift file keeps the existing Apache 2.0 license header (copy from any touched file's first 14 lines).
- Build verification command (macOS, after any change touching `apple/Shared/` or `apple/LatticeMac/`):
  ```bash
  cd /Users/francis/workspc/lattice
  GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
  cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -40
  ```
  Expected: `** BUILD SUCCEEDED **`. This regenerates the git-ignored macOS xcframework fresh each time (per `apple/Scripts/build_framework.sh`'s pattern used successfully throughout this session) — do not skip it, a stale framework masks real build breaks.
- Build verification command (iOS, after any change touching `apple/Shared/` or `apple/Lattice/`):
  ```bash
  cd /Users/francis/workspc/lattice
  GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
  cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -60
  ```
  Expected: `** BUILD SUCCEEDED **`. Use `generic/platform=iOS Simulator` (not a named simulator device) so this works headlessly without a booted simulator instance.
- `apple/Frameworks/{MacOS,iOS}/LatticeCore.xcframework` are git-ignored build artifacts (per `.gitignore`, confirmed this session) — never `git add` them, and delete them after a build session if disk space matters (`rm -rf apple/Frameworks`), they regenerate from source in seconds.
- No XCTest UI-test scheme exists in this project — verification for new SwiftUI screens is "it builds" (above) plus a manual run-and-look smoke test described in each task, not automated UI tests.
- This plan executes inline in the current session against the live checked-out `new_dev` branch, not a fresh worktree.

---

### Task 1: Merge `TunnelManager` into `apple/Shared/TunnelManager.swift`

**Files:**
- Create: `apple/Shared/TunnelManager.swift`
- Delete: `apple/Lattice/TunnelManager.swift`
- Delete: `apple/LatticeMac/TunnelManager.swift`
- Modify: `apple/Shared/TunnelCore.swift:32` (the `PeerNode.os` default)

**Interfaces:**
- Produces: `final class TunnelManager: ObservableObject` with `static let shared: TunnelManager`, `@Published var isConfigured: Bool`, `@Published var status: NEVPNStatus`, `@Published var lastStartError: String`, `@Published var peerStates: [String: String]`, `var statusText: String`, `var connectedBinding: Binding<Bool>`, `var serverURL: String?`, `func load(_ completion: (() -> Void)? = nil)`, `func saveJoin(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil)`, `func connect()`, `func disconnect()`. Every later task that touches connection state uses these exact names.
- Consumes: nothing new (pure `Foundation`/`SwiftUI`/`NetworkExtension`).

- [x] **Step 1: Write the merged file**

Create `apple/Shared/TunnelManager.swift`:

```swift
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
import SwiftUI
import NetworkExtension

/// Owns the VPN profile (NETunnelProviderManager) and its lifecycle, shared
/// between LatticeMac and Lattice (iOS) — the two platforms differ only in
/// tunnel bundle ID. The tunnel itself runs inside the platform's extension
/// (LatticeTunnelMac / LatticeTunnel); this class creates/updates the
/// profile, toggles the connection, and polls the extension for per-peer
/// connection quality over the NE provider-message channel.
final class TunnelManager: ObservableObject {
    static let shared = TunnelManager()

    #if os(macOS)
    static let tunnelBundleID = "io.lattice.mac.tunnel"
    #else
    static let tunnelBundleID = "io.lattice.ios.tunnel"
    #endif
    static let profileName = "Lattice"

    @Published private(set) var isConfigured = false
    @Published private(set) var status: NEVPNStatus = .invalid
    @Published private(set) var lastStartError: String = ""
    /// Per-peer connection quality from the tunnel process
    /// (peer name → "ice-ready" | "lrp-ready" | "probing" | ...).
    @Published private(set) var peerStates: [String: String] = [:]

    /// The management server this profile points at (panel subtitle).
    var serverURL: String? {
        (manager?.protocolConfiguration as? NETunnelProviderProtocol)?.serverAddress
    }

    var statusText: String {
        switch status {
        case .connected: return "已连接"
        case .connecting, .reasserting: return "连接中…"
        case .disconnecting: return "断开中…"
        default: return "未连接"
        }
    }

    /// Binding for a connect toggle: turning it on with no profile yet is a
    /// no-op (the join flow drives profile creation).
    var connectedBinding: Binding<Bool> {
        Binding(
            get: { self.status == .connected },
            set: { on in
                if on {
                    self.connect()
                } else {
                    self.disconnect()
                }
            }
        )
    }

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?
    private var statePoller: Timer?

    private init() {}

    /// Loads (or reloads) the Lattice VPN profile and status from the system.
    func load(_ completion: (() -> Void)? = nil) {
        NETunnelProviderManager.loadAllFromPreferences { managers, _ in
            DispatchQueue.main.async {
                self.manager = managers?.first {
                    ($0.protocolConfiguration as? NETunnelProviderProtocol)?.providerBundleIdentifier == Self.tunnelBundleID
                }
                self.isConfigured = self.manager != nil
                self.refreshStatus()
                self.observeStatus()
                completion?()
            }
        }
    }

    /// Creates or updates the VPN profile with join parameters, then enables
    /// it. Any previous Lattice profile is removed first: the OS pins the
    /// provider's code requirement at profile-creation time, so a stale
    /// profile would reject a rebuilt (correctly signed) extension forever.
    /// - Parameters:
    ///   - serverURL: management server base URL, e.g. http://172.20.10.4:8080
    ///   - token: enrollment token issued by the control plane
    ///   - name: stable node name (used as the peer identity)
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

    private func createProfile(serverURL: String, token: String, name: String, completion: ((String?) -> Void)? = nil) {
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = serverURL
        proto.providerConfiguration = [
            "serverURL": serverURL,
            "token": token,
            "name": name,
        ]

        let mgr = NETunnelProviderManager()
        mgr.protocolConfiguration = proto
        mgr.localizedDescription = Self.profileName
        mgr.isEnabled = true
        mgr.saveToPreferences { [weak self] error in
            DispatchQueue.main.async {
                if let error {
                    completion?(error.localizedDescription)
                    return
                }
                // Reload so `manager.connection` points at the saved profile.
                self?.load {
                    completion?(nil)
                }
            }
        }
    }

    func connect() {
        lastStartError = ""
        guard let connection = manager?.connection else { return }
        do {
            try connection.startVPNTunnel()
        } catch {
            lastStartError = error.localizedDescription
        }
    }

    func disconnect() {
        manager?.connection.stopVPNTunnel()
    }

    private func refreshStatus() {
        status = manager?.connection.status ?? .invalid
        if status == .connected {
            startStatePoller()
        } else {
            stopStatePoller()
            if peerStates.isEmpty == false {
                peerStates = [:]
            }
        }
    }

    private func observeStatus() {
        if let observer {
            NotificationCenter.default.removeObserver(observer)
        }
        observer = NotificationCenter.default.addObserver(
            forName: .NEVPNStatusDidChange,
            object: manager?.connection,
            queue: .main
        ) { [weak self] _ in
            self?.refreshStatus()
        }
    }

    // MARK: - Connection quality (provider message channel)

    private func startStatePoller() {
        guard statePoller == nil else { return }
        pollPeerStates()
        statePoller = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            self?.pollPeerStates()
        }
    }

    private func stopStatePoller() {
        statePoller?.invalidate()
        statePoller = nil
    }

    /// Asks the tunnel process for its latest peer-state snapshot over the
    /// NE provider-message channel (see PacketTunnelProvider.handleAppMessage).
    private func pollPeerStates() {
        guard let connection = manager?.connection as? NETunnelProviderSession else { return }
        do {
            try connection.sendProviderMessage(Data("peerStates".utf8)) { [weak self] data in
                guard let data,
                      let states = try? JSONDecoder().decode([String: String].self, from: data) else { return }
                DispatchQueue.main.async {
                    self?.peerStates = states
                }
            }
        } catch {
            // Session not ready; the next tick retries.
        }
    }
}
```

- [x] **Step 2: Delete both old per-platform files**

```bash
cd /Users/francis/workspc/lattice
rm apple/Lattice/TunnelManager.swift apple/LatticeMac/TunnelManager.swift
```

- [x] **Step 3: Fix the `PeerNode.os` default while touching this area**

In `apple/Shared/TunnelCore.swift`, this line:

```swift
    var os: String = "macOS"
```

is a stale macOS-only default now that this model is genuinely shared. Change it to:

```swift
    var os: String = ""
```

(Callers populate the real value from the API response; the default only matters before that happens, and an empty string is honest about "unknown" instead of silently claiming macOS.)

- [x] **Step 4: Build both targets**

Run both verification commands from Global Constraints (macOS first, then iOS). Expected: both `** BUILD SUCCEEDED **`. If the iOS build fails referencing `ContentView`'s use of `tunnel.statusText`/`tunnel.connectedBinding`/`tunnel.lastStartError` as non-optional — that's expected and fine, this task doesn't touch `ContentView.swift` yet (Task 5 replaces it); a failure here should only be about `TunnelManager` itself, not its callers. If the *macOS* build fails, that's a real problem — macOS's `ContentView.swift` already expects exactly the properties this merged file provides (verified by inspection in Step 1), so a macOS failure means something in this step doesn't match what `ContentView.swift`/`PeerDetailView.swift`/`NetworkPages.swift` actually call — find it with `grep -rn "tunnel\." apple/LatticeMac/*.swift` and reconcile.

- [x] **Step 5: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Shared/TunnelManager.swift apple/Shared/TunnelCore.swift apple/Lattice/TunnelManager.swift apple/LatticeMac/TunnelManager.swift
git commit -s -m "refactor(apple): merge TunnelManager into Shared, add peer-quality polling to iOS"
```

---

### Task 2: Move `LatticeAPI` into `apple/Shared/LatticeAPI.swift`

**Files:**
- Create: `apple/Shared/LatticeAPI.swift`
- Modify: `apple/LatticeMac/LatticeMacApp.swift` (remove the moved class + structs)

**Interfaces:**
- Produces: `final class LatticeAPI` (singleton `LatticeAPI.shared`) with (at minimum, used by later tasks) `var isLoggedIn: Bool`, `var serverURL: String`, `func login(user: String, pass: String) async throws`, `func listPeers() async throws -> [PeerNode]`, `func listAgentIdentities() async throws -> [AgentIdentityVO]`, `func listPolicies() async throws -> [LatticePolicy]`, `func planIntent(_:) async throws -> IntentPlanView`, `func applyIntent(planID:) async throws`, `func listRouteSelections(_ consumer: String) async throws -> [String]`, `func setAdvertisedRoutes(_ name: String, routes: [String]) async throws`, `func setRouteSelection(consumer: String, provider: String, selected: Bool) async throws`, plus the full set of `Codable` model types listed below — unchanged from their current definitions, just relocated.
- Consumes: `PeerNode` from `apple/Shared/TunnelCore.swift` (Task 1's file, untouched by this task).

- [x] **Step 1: Read the exact current boundaries before cutting**

```bash
cd /Users/francis/workspc/lattice
grep -n "^final class LatticeAPI\|^struct PeerListResponse\|^struct LoginResponse\|^struct WorkspaceListResponse\|^enum LatticeAPIError\|^struct ChatAPIMessage\|^struct ChatStreamEvent\|^enum ChatEvent\|^struct PolicyListResponse\|^struct LatticePolicy\|^struct PolicyRuleSet\|^struct PolicyPeer\|^struct IPBlock\|^struct ACLEntry\|^struct IPv4Address\|^struct IntentPlanResponse\|^struct IntentPlanView\|^struct IntentChange\|^struct AgentIdentityListResponse\|^struct AgentIdentityVO" apple/LatticeMac/LatticeMacApp.swift
```

This prints the exact starting line of `LatticeAPI` and the exact starting line of whatever top-level declaration comes immediately after `AgentIdentityVO` (there is none after it per the earlier session-wide symbol scan — `AgentIdentityVO` is the last declaration in the file). Use these two numbers (call them `$START` and end-of-file) for the extraction.

- [x] **Step 2: Extract the block into the new file**

```bash
cd /Users/francis/workspc/lattice
START=$(grep -n "^final class LatticeAPI" apple/LatticeMac/LatticeMacApp.swift | head -1 | cut -d: -f1)
TOTAL=$(wc -l < apple/LatticeMac/LatticeMacApp.swift)
{
  echo "// Copyright 2026 The Lattice Authors, Inc."
  echo "//"
  echo '// Licensed under the Apache License, Version 2.0 (the "License");'
  echo "// you may not use this file except in compliance with the License."
  echo "// You may obtain a copy of the License at"
  echo "//"
  echo "//     http://www.apache.org/licenses/LICENSE-2.0"
  echo "//"
  echo "// Unless required by applicable law or agreed to in writing, software"
  echo '// distributed under the License is distributed on an "AS IS" BASIS,'
  echo "// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied."
  echo "// See the License for the specific language governing permissions and"
  echo "// limitations under the License."
  echo ""
  echo "import Foundation"
  echo ""
  sed -n "${START},${TOTAL}p" apple/LatticeMac/LatticeMacApp.swift
} > apple/Shared/LatticeAPI.swift

# Remove the now-duplicated block from the original file (everything from
# LatticeAPI's opening line to the file's end).
sed -i '' "${START},${TOTAL}d" apple/LatticeMac/LatticeMacApp.swift
```

- [x] **Step 3: Check for a stray trailing blank block left in `LatticeMacApp.swift`**

```bash
tail -5 apple/LatticeMac/LatticeMacApp.swift
```

If this ends mid-declaration or with dangling blank lines only (it should end cleanly right after `AppDelegate`'s closing brace, since that's the declaration immediately before `LatticeAPI`), trim any trailing blank lines the `sed` deletion left behind (`sed -i '' -e :a -e '/^\n*$/{$d;N;ba' -e '}' apple/LatticeMac/LatticeMacApp.swift` removes trailing blank lines at EOF if needed — check first before running, only run it if `tail` shows more than one trailing blank line).

- [x] **Step 4: Verify `LatticeAPI.swift`'s import list is sufficient**

The extracted class may reference `URLSession`, `URLRequest`, `JSONEncoder`/`JSONDecoder`, `Data` (all `Foundation`, already imported in Step 2) — confirm nothing AppKit-specific slipped in:

```bash
cd /Users/francis/workspc/lattice
grep -n "NSColor\|NSImage\|NSView\|import AppKit\|import Cocoa" apple/Shared/LatticeAPI.swift
```

Expected: no output. If something matches, that reference needs to be resolved (most likely: it shouldn't be there at all, since this class was already confirmed AppKit-free by grep before this plan was written — treat any hit here as a signal the extraction boundary was drawn wrong, and adjust `$START` to include one more/fewer leading lines).

- [x] **Step 5: Build both targets**

Run both verification commands from Global Constraints. Expected: both `** BUILD SUCCEEDED **`. The macOS build is the one at risk here — every file in `apple/LatticeMac/` that references `LatticeAPI` or any of the moved model types compiles against the same module either way (same target, files just moved within it via the `Shared` directory which is already in `LatticeMac`'s `sources`), so this should be a non-event, but build both to be sure.

- [x] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Shared/LatticeAPI.swift apple/LatticeMac/LatticeMacApp.swift
git commit -s -m "refactor(apple): move LatticeAPI and its models to Shared"
```

---

### Task 3: Move `DesignComponents` into `apple/Shared/DesignComponents.swift`

**Files:**
- Create: `apple/Shared/DesignComponents.swift`
- Delete: `apple/LatticeMac/DesignComponents.swift`

**Interfaces:**
- Produces: `enum LatticePalette`, `struct QualityPill: View`, `struct TagBadge: View`, `struct HaloDot: View`, `struct SoonBadge: View`, `struct SectionHead: View`, `struct NavRow: View`, `struct PanelSearchField: View`, `struct LabeledField<Content: View>: View`, and the existing `View` extension — all unchanged, just relocated. Task 6/7/8 (new iOS screens) use `HaloDot`, `QualityPill`, and `LatticePalette` by these exact names.

- [x] **Step 1: Move the file**

```bash
cd /Users/francis/workspc/lattice
git mv apple/LatticeMac/DesignComponents.swift apple/Shared/DesignComponents.swift
```

- [x] **Step 2: Check for AppKit-specific APIs inside it**

```bash
cd /Users/francis/workspc/lattice
grep -n "NSColor\|NSImage\|NSView\|import AppKit\|import Cocoa\|nscolor" apple/Shared/DesignComponents.swift
```

If this returns any hits (e.g. a `Color(nsColor:)` call for a system color), each one needs a cross-platform replacement before this file can compile for iOS. Read the specific line(s) and replace with the SwiftUI cross-platform equivalent — e.g. `Color(nsColor: .systemGray)` becomes `Color.gray` or `Color(.secondarySystemBackground)`'s closest cross-platform match; pick whichever preserves the original visual intent most closely, there is no single universal substitution. If no hits, this file is already portable as-is — proceed to Step 3.

- [x] **Step 3: Build both targets**

Run both verification commands from Global Constraints. Expected: both `** BUILD SUCCEEDED **`.

- [x] **Step 4: Commit**

```bash
cd /Users/francis/workspc/lattice
git add -A apple/Shared/DesignComponents.swift apple/LatticeMac/DesignComponents.swift
git commit -s -m "refactor(apple): move DesignComponents to Shared"
```

---

### Task 4: Move `JoinPayload` to Shared; write the iOS camera scanner

**Files:**
- Create: `apple/Shared/JoinPayload.swift`
- Modify: `apple/LatticeMac/QRScanner.swift` (remove the moved `JoinPayload`, keep `CameraScannerView`)
- Create: `apple/Lattice/QRScannerView.swift`

**Interfaces:**
- Produces: `struct JoinPayload { var serverURL: String?; var token: String?; init?(_ raw: String) }` (Shared); `struct QRScannerView: UIViewRepresentable` with `var onCode: (String) -> Void` and `var onError: (String) -> Void` (iOS-only, same calling contract as macOS's `CameraScannerView`). Task 7's join flow constructs `QRScannerView(onCode:onError:)` exactly like macOS's join sheet constructs `CameraScannerView`.
- Consumes: nothing new (`AVFoundation`, `SwiftUI`, `Foundation`).

- [x] **Step 1: Extract `JoinPayload` into Shared**

Create `apple/Shared/JoinPayload.swift`:

```swift
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
```

- [x] **Step 2: Remove the now-duplicated `JoinPayload` from `QRScanner.swift`**

In `apple/LatticeMac/QRScanner.swift`, delete the entire `struct JoinPayload { ... }` block (lines 120-149 per this session's reading of the file — confirm the exact range with `grep -n "struct JoinPayload" apple/LatticeMac/QRScanner.swift` before deleting, since Task 1-3's edits may have shifted nothing in this file but verify anyway). The file should end with `CameraScannerView`'s closing brace after this edit.

- [x] **Step 3: Write the iOS camera scanner**

Create `apple/Lattice/QRScannerView.swift`:

```swift
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

import AVFoundation
import SwiftUI

/// Live camera QR scanner for join codes (AVCaptureMetadataOutput detects QR
/// natively — no Vision pass needed). Same payload contract as macOS's
/// CameraScannerView: lattice://join?server=<url>&token=<token>, parsed by
/// the shared JoinPayload type.
struct QRScannerView: UIViewRepresentable {
    /// Called on the main queue with the first QR string detected.
    var onCode: (String) -> Void
    /// Called on the main queue when the camera cannot be used.
    var onError: (String) -> Void

    func makeUIView(context: Context) -> UIView {
        let view = UIView(frame: .zero)
        context.coordinator.attach(to: view, onCode: onCode, onError: onError)
        return view
    }

    func updateUIView(_ uiView: UIView, context: Context) {
        context.coordinator.previewLayer?.frame = uiView.bounds
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate {
        private let session = AVCaptureSession()
        private var configured = false
        private var onCode: ((String) -> Void)?
        private var delivered = false
        var previewLayer: AVCaptureVideoPreviewLayer?

        func attach(to view: UIView, onCode: @escaping (String) -> Void, onError: @escaping (String) -> Void) {
            self.onCode = onCode
            // makeUIView runs inside view update — SwiftUI state writes must
            // never happen synchronously here, so every callback defers.
            let safeOnError: (String) -> Void = { message in
                DispatchQueue.main.async { onError(message) }
            }
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized:
                configure(view: view, onError: safeOnError)
            case .notDetermined:
                AVCaptureDevice.requestAccess(for: .video) { granted in
                    DispatchQueue.main.async {
                        granted ? self.configure(view: view, onError: safeOnError)
                                : safeOnError("相机权限被拒绝，请在设置中允许 Lattice 使用摄像头")
                    }
                }
            default:
                safeOnError("相机权限未开启，请在设置 → Lattice → 相机中允许访问")
            }
        }

        private func configure(view: UIView, onError: @escaping (String) -> Void) {
            guard !configured else { return }
            configured = true
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device) else {
                onError("未找到可用摄像头")
                return
            }
            session.beginConfiguration()
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output), session.canAddInput(input) else {
                session.commitConfiguration()
                onError("摄像头初始化失败")
                return
            }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]
            session.commitConfiguration()

            let preview = AVCaptureVideoPreviewLayer(session: session)
            preview.videoGravity = .resizeAspectFill
            preview.frame = view.bounds
            view.layer.addSublayer(preview)
            previewLayer = preview

            DispatchQueue.global(qos: .userInitiated).async { [session] in
                session.startRunning()
            }
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput,
                            didOutput metadataObjects: [AVMetadataObject],
                            from connection: AVCaptureConnection) {
            guard !delivered else { return }
            guard let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
                  object.type == .qr,
                  let value = object.stringValue, !value.isEmpty else { return }
            delivered = true
            DispatchQueue.main.async { [onCode] in
                onCode?(value)
            }
        }

        func stop() {
            DispatchQueue.global(qos: .userInitiated).async { [session] in
                session.stopRunning()
            }
        }
    }
}
```

- [x] **Step 4: Build both targets**

Run both verification commands from Global Constraints. Expected: both `** BUILD SUCCEEDED **`. (`QRScannerView.swift` isn't referenced by anything yet — Task 7 wires it in — so this step only proves it compiles standalone.)

- [x] **Step 5: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Shared/JoinPayload.swift apple/LatticeMac/QRScanner.swift apple/Lattice/QRScannerView.swift
git commit -s -m "refactor(apple): move JoinPayload to Shared, add iOS camera QR scanner"
```

---

### Task 5: iOS TabView shell, Status tab, and Peer detail

**Files:**
- Modify: `apple/Lattice/LatticeApp.swift`
- Create: `apple/Lattice/RootView.swift`
- Create: `apple/Lattice/StatusView.swift`
- Create: `apple/Lattice/PeerDetailView.swift`
- Delete: `apple/Lattice/ContentView.swift` (superseded — its join-form responsibility moves to Task 7's `JoinView.swift`; deleting it now is fine because `RootView.swift` in this task doesn't reference it, it references a not-yet-written `JoinView` behind a `#if false`-free placeholder — see Step 2's note)

**Interfaces:**
- Produces: `struct RootView: View` (the `TabView` shell, becomes `LatticeApp.swift`'s `WindowGroup` content); `struct StatusView: View`; `struct PeerDetailView: View` with `let peer: PeerNode` and `var quality: String?` (read-only, no callbacks — different signature than macOS's `PeerDetailView`, which is a different type in a different module-visible file but the SAME type name; Swift resolves this fine since each target only compiles its own platform's file, but note this for Task 6/7: do not `import` across platform folders, there is no cross-reference).
- Consumes: `TunnelManager` (Task 1), `LatticeAPI`/`PeerNode` (Task 2), `HaloDot`/`QualityPill`/`LatticePalette` (Task 3).

- [x] **Step 1: Write the Status tab**

Create `apple/Lattice/StatusView.swift`:

```swift
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

/// Home tab: connection toggle, own overlay IP, and the peer list with live
/// connection quality. Peer list requires an admin-auth session
/// (LatticeAPI.shared.isLoggedIn) — the join flow (JoinView) establishes
/// this before RootView ever shows this tab, so `loadPeers()` here assumes
/// it's already true and just surfaces the error if the session expired.
struct StatusView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var peers: [PeerNode] = []
    @State private var isLoading = false
    @State private var errorMsg = ""
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        NavigationStack {
            List {
                Section("连接") {
                    HStack {
                        Text(tunnel.statusText)
                            .foregroundColor(tunnel.status == .connected ? .green : .primary)
                        Spacer()
                        Toggle("", isOn: tunnel.connectedBinding)
                            .labelsHidden()
                    }
                    if !tunnel.lastStartError.isEmpty {
                        Text(tunnel.lastStartError)
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                }

                Section("节点") {
                    if isLoading && peers.isEmpty {
                        HStack {
                            Spacer()
                            ProgressView()
                            Spacer()
                        }
                    } else if !errorMsg.isEmpty {
                        Text(errorMsg).font(.caption).foregroundColor(.red)
                    } else if peers.isEmpty {
                        Text("暂无节点").font(.caption).foregroundColor(.secondary)
                    } else {
                        ForEach(peers) { peer in
                            NavigationLink {
                                PeerDetailView(peer: peer, quality: tunnel.peerStates[peer.name])
                            } label: {
                                peerRow(peer)
                            }
                        }
                    }
                }
            }
            .navigationTitle("Lattice")
            .refreshable { await loadPeers() }
            .task { await loadPeers() }
            .onChange(of: scenePhase) { _, newPhase in
                if newPhase == .active {
                    Task { await loadPeers() }
                }
            }
        }
    }

    private func peerRow(_ peer: PeerNode) -> some View {
        HStack(spacing: 10) {
            HaloDot(active: !peer.disabled)
            VStack(alignment: .leading, spacing: 2) {
                Text(peer.shownName)
                    .font(.system(.body))
                Text(peer.address)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundColor(.secondary)
            }
            Spacer()
            if let quality = tunnel.peerStates[peer.name] {
                QualityPill(state: quality)
            }
        }
        .padding(.vertical, 2)
    }

    private func loadPeers() async {
        isLoading = true
        errorMsg = ""
        defer { isLoading = false }
        do {
            peers = try await LatticeAPI.shared.listPeers()
        } catch {
            errorMsg = "加载失败: \(error.localizedDescription)"
        }
    }
}
```

**Note on `HaloDot`/`QualityPill`'s exact initializers:** the code above assumes `HaloDot(active: Bool)` and `QualityPill(state: String)` — **before writing this file for real, read `apple/Shared/DesignComponents.swift` (moved there in Task 3) to confirm these two initializers' actual parameter names and types**, since this plan's author has not read that file's body, only its top-level symbol list. Adjust the call sites in `peerRow` to match whatever the real initializer signatures are — the intent (a status dot for online/offline, a pill showing "direct"/"relayed"/"connecting" for the quality string) stays the same regardless of the exact parameter spelling.

- [x] **Step 2: Write the read-only Peer detail screen**

Create `apple/Lattice/PeerDetailView.swift`:

```swift
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

/// Read-only peer detail — no rename/disable/delete (device management is
/// explicitly out of scope for iOS v1, see
/// docs/superpowers/specs/2026-09-16-ios-app-design.md).
struct PeerDetailView: View {
    let peer: PeerNode
    var quality: String?
    @State private var copied = false

    var body: some View {
        List {
            Section {
                LabeledContent("名称", value: peer.shownName)
                LabeledContent("地址", value: peer.address)
                    .font(.system(.body, design: .monospaced))
                if let q = qualityLabel {
                    LabeledContent("连接质量") {
                        Text(q.text).foregroundColor(q.color)
                    }
                }
                LabeledContent("平台", value: peer.os.isEmpty ? "未知" : peer.os)
                if !peer.lastSeen.isEmpty {
                    LabeledContent("最近在线", value: relativeTime(peer.lastSeen))
                }
            }

            if !peer.appID.isEmpty {
                Section("公钥 / 标识") {
                    Button {
                        UIPasteboard.general.string = peer.appID
                        copied = true
                        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2) { copied = false }
                    } label: {
                        HStack {
                            Text(peer.appID)
                                .font(.system(.caption, design: .monospaced))
                                .foregroundColor(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer()
                            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                                .foregroundColor(copied ? .green : .secondary)
                        }
                    }
                }
            }
        }
        .navigationTitle(peer.shownName)
        .navigationBarTitleDisplayMode(.inline)
    }

    private var qualityLabel: (text: String, color: Color)? {
        switch quality {
        case "ice-ready": return ("直连", .green)
        case "lrp-ready": return ("经中继", .orange)
        case "probing", "created": return ("连接中", .secondary)
        case "failed": return ("失败", .red)
        default: return nil
        }
    }

    private func relativeTime(_ rfc3339: String) -> String {
        let formatter = ISO8601DateFormatter()
        guard let date = formatter.date(from: rfc3339) else {
            return rfc3339
        }
        let formatter2 = RelativeDateTimeFormatter()
        formatter2.locale = Locale(identifier: "zh_CN")
        return formatter2.localizedString(for: date, relativeTo: Date())
    }
}
```

Note: this uses `peer.appID` as the stand-in for "public key" — **before writing this file for real, check whether `PeerNode` (in `apple/Shared/TunnelCore.swift`) actually carries a `publicKey` field or only `appID`** (Task 1's read of that struct did not show a `publicKey` field — only `appID`, `name`, `address`, etc. — confirm with `grep -n "var \|let " apple/Shared/TunnelCore.swift` before assuming; if there truly is no public-key field on this model, showing `appID` as the copyable identifier is the correct fallback, just make sure the section header text ("公钥 / 标识") still makes sense for whichever field actually renders — reword to "设备标识" if it's only ever `appID`, not a real public key).

- [x] **Step 3: Write the TabView shell**

Create `apple/Lattice/RootView.swift`:

```swift
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
```

This references `SettingsView` (Task 6) and `JoinView` (Task 7), neither of which exist yet — **this task's build step (Step 5 below) is expected to fail to compile until Tasks 6 and 7 are done.** That's an accepted, temporary state for this task, not a bug to chase — the alternative (writing three tasks' worth of interdependent screens as one giant task) violates the plan's own task-sizing guidance. Do not skip Task 5's Step 5 build attempt anyway — record its exact error (should be exactly two "cannot find type" errors, for `SettingsView` and `JoinView`, nothing else) so Task 6/7's build steps have something to diff against.

- [x] **Step 4: Wire it into the app entry point and delete the old ContentView**

In `apple/Lattice/LatticeApp.swift`, change:

```swift
@main
struct LatticeApp: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
        }
    }
}
```

to:

```swift
@main
struct LatticeApp: App {
    var body: some Scene {
        WindowGroup {
            RootView()
        }
    }
}
```

```bash
cd /Users/francis/workspc/lattice
rm apple/Lattice/ContentView.swift
```

- [x] **Step 5: Attempt the iOS build (expected to fail on missing `SettingsView`/`JoinView` only)**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | grep -E "error:|BUILD"
```

Expected: exactly two `error: cannot find type 'X' in scope` lines (`SettingsView`, `JoinView`), `BUILD FAILED`. Any *other* error (a typo in `StatusView.swift`/`PeerDetailView.swift`/`RootView.swift` itself, a `HaloDot`/`QualityPill` signature mismatch from Step 1's note, a missing `PeerNode.publicKey` from Step 2's note) must be fixed now, in this task — don't carry a real bug forward into Task 6 disguised as "expected failure."

- [x] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Lattice/LatticeApp.swift apple/Lattice/RootView.swift apple/Lattice/StatusView.swift apple/Lattice/PeerDetailView.swift apple/Lattice/ContentView.swift
git commit -s -m "feat(apple): iOS TabView shell, Status tab, and read-only peer detail"
```

---

### Task 6: iOS Settings tab and Exit Node screen

**Files:**
- Create: `apple/Lattice/SettingsView.swift`
- Create: `apple/Lattice/ExitNodeView.swift`

**Interfaces:**
- Produces: `struct SettingsView: View`; `struct ExitNodeView: View`.
- Consumes: `TunnelManager` (Task 1), `LatticeAPI`/`PeerNode` (Task 2), `RootView` (Task 5, not yet compiling — this task doesn't fix that, Task 7 does).

- [x] **Step 1: Read `apple/LatticeMac/NetworkPages.swift`'s exact Exit Node data flow if you haven't already this session**

This plan's author already read the full file this session — the logic is: `selfName` = `UserDefaults.standard.string(forKey: "lattice.nodeName")`; `load()` calls `LatticeAPI.shared.listPeers()`, filters to peers with non-empty `advertisedRoutes`, then `LatticeAPI.shared.listRouteSelections(selfName)` to know which are currently selected; "Exit Node" candidates are peers whose `advertisedRoutes` contains `"0.0.0.0/0"`; picking one calls `setRouteSelection(consumer: selfName, provider: name, selected: true)` and `false` for any previously-selected exit node that isn't the new pick. Implement the iOS version with this exact same data flow — do not invent a different one.

- [x] **Step 2: Write the Exit Node screen**

Create `apple/Lattice/ExitNodeView.swift`:

```swift
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

/// Exit Node picker — same data flow as LatticeMac's NetworkSettingsView:
/// candidates are peers advertising "0.0.0.0/0"; selecting one calls
/// setRouteSelection(consumer: selfName, provider: name, selected: true)
/// and clears any previously-selected exit node.
struct ExitNodeView: View {
    @State private var candidates: [PeerNode] = []
    @State private var selectedProviders: Set<String> = []
    @State private var selfName: String = UserDefaults.standard.string(forKey: "lattice.nodeName") ?? ""
    @State private var isLoading = true
    @State private var errorText = ""

    var body: some View {
        List {
            if isLoading {
                HStack {
                    Spacer()
                    ProgressView()
                    Spacer()
                }
            } else {
                Section {
                    Button {
                        Task { await selectExitNode(nil) }
                    } label: {
                        HStack {
                            Text("无（关闭）")
                            Spacer()
                            if selectedExitNode == nil {
                                Image(systemName: "checkmark").foregroundColor(.accentColor)
                            }
                        }
                    }
                    .foregroundColor(.primary)

                    ForEach(candidates.filter { $0.advertisedRoutes.contains("0.0.0.0/0") }) { peer in
                        Button {
                            Task { await selectExitNode(peer.name) }
                        } label: {
                            HStack {
                                Text(peer.shownName)
                                Spacer()
                                if selectedExitNode == peer.name {
                                    Image(systemName: "checkmark").foregroundColor(.accentColor)
                                }
                            }
                        }
                        .foregroundColor(.primary)
                    }
                }
                if !errorText.isEmpty {
                    Text(errorText).font(.caption).foregroundColor(.red)
                }
            }
        }
        .navigationTitle("退出节点")
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private var selectedExitNode: String? {
        selectedProviders.first { provider in
            candidates.first(where: { $0.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true
        }
    }

    private func load() async {
        isLoading = true
        errorText = ""
        do {
            let peers = try await LatticeAPI.shared.listPeers()
            candidates = peers.filter { !$0.advertisedRoutes.isEmpty }
            let selected = try await LatticeAPI.shared.listRouteSelections(selfName)
            selectedProviders = Set(selected)
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func selectExitNode(_ name: String?) async {
        let oldExitNodes = selectedProviders.filter { provider in
            candidates.first(where: { $0.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true
        }
        do {
            if let name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: name, selected: true)
            }
            for provider in oldExitNodes where provider != name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: provider, selected: false)
            }
            await load()
        } catch {
            errorText = "选择失败: \(error.localizedDescription)"
            await load()
        }
    }
}
```

- [x] **Step 3: Write the Settings tab**

Create `apple/Lattice/SettingsView.swift`:

```swift
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

struct SettingsView: View {
    @StateObject private var tunnel = TunnelManager.shared
    @State private var showingLeaveConfirm = false

    var body: some View {
        NavigationStack {
            List {
                Section("网络") {
                    LabeledContent("服务器", value: tunnel.serverURL ?? "—")
                    NavigationLink("退出节点") {
                        ExitNodeView()
                    }
                }

                Section {
                    Button("退出网络", role: .destructive) {
                        showingLeaveConfirm = true
                    }
                }

                Section {
                    LabeledContent("版本", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "—")
                }
            }
            .navigationTitle("设置")
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

    private func leaveNetwork() {
        tunnel.disconnect()
        UserDefaults.standard.removeObject(forKey: "lattice.serverURL")
        UserDefaults.standard.removeObject(forKey: "lattice.nodeName")
        UserDefaults.standard.removeObject(forKey: "lattice.authToken")
        UserDefaults.standard.removeObject(forKey: "lattice.adminUser")
        // Actual VPN-profile removal happens the same way TunnelManager.saveJoin
        // already does it (removeFromPreferences for the stale profile) — the
        // next join flow's saveJoin call handles cleanup, so there is nothing
        // further to do here beyond clearing local state and forcing RootView
        // to re-show the join flow, which happens because isConfigured/
        // isLoggedIn now evaluate false the next time evaluateJoinState() runs
        // (RootView.onAppear) — trigger that by reloading:
        tunnel.load()
    }
}
```

**Before finalizing this step, check `LatticeAPI`'s actual UserDefaults key names** for the admin session (`lattice.authToken`/`lattice.adminUser` were seen in this session's earlier reading of `LatticeMacApp.swift`'s `login()`/`isLoggedIn` — confirm with `grep -n "UserDefaults.standard" apple/Shared/LatticeAPI.swift` since Task 2 moved that file, and there may be a `KeychainStore.set(pass, forKey:)` call for the password too per this session's earlier viewing — if so, also clear that keychain entry in `leaveNetwork()`, using whatever `KeychainStore` API that file already defines (read it, don't guess its method signature).

- [x] **Step 4: Attempt the iOS build (still expected to fail on `JoinView` only now)**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | grep -E "error:|BUILD"
```

Expected: exactly one `error: cannot find type 'JoinView' in scope`, `BUILD FAILED`. Fix any other error now (don't carry it into Task 7).

- [x] **Step 5: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Lattice/SettingsView.swift apple/Lattice/ExitNodeView.swift
git commit -s -m "feat(apple): iOS Settings tab and Exit Node picker"
```

---

### Task 7: iOS 2-step join flow, camera permission, and final verification

**Files:**
- Create: `apple/Lattice/JoinView.swift`
- Modify: `apple/project.yml` (`Lattice` target's `settings.base`)

**Interfaces:**
- Produces: `struct JoinView: View` with `var onFinished: () -> Void` (matches `RootView`'s `JoinView(onFinished: { needsJoin = false })` call from Task 5).
- Consumes: `TunnelManager.saveJoin`/`.connect()` (Task 1), `LatticeAPI.shared.login` (Task 2), `JoinPayload` (Task 4), `QRScannerView` (Task 4).

- [x] **Step 1: Add the camera-permission Info.plist key**

In `apple/project.yml`, find the `Lattice` target's `settings.base` block:

```yaml
    settings:
      base:
        PRODUCT_BUNDLE_IDENTIFIER: io.lattice.ios
        GENERATE_INFOPLIST_FILE: YES
        INFOPLIST_KEY_UILaunchScreen_Generation: YES
        CODE_SIGN_ENTITLEMENTS: entitlements/Lattice.entitlements
        SWIFT_EMIT_LOC_STRINGS: YES
        CODE_SIGN_STYLE: Automatic
```

add one line:

```yaml
    settings:
      base:
        PRODUCT_BUNDLE_IDENTIFIER: io.lattice.ios
        GENERATE_INFOPLIST_FILE: YES
        INFOPLIST_KEY_UILaunchScreen_Generation: YES
        INFOPLIST_KEY_NSCameraUsageDescription: "Lattice 需要使用摄像头扫描入网二维码"
        CODE_SIGN_ENTITLEMENTS: entitlements/Lattice.entitlements
        SWIFT_EMIT_LOC_STRINGS: YES
        CODE_SIGN_STYLE: Automatic
```

Regenerate the Xcode project from `project.yml` (this repo uses XcodeGen — confirm the command by checking for an `xcodegen` invocation elsewhere in the repo first):

```bash
cd /Users/francis/workspc/lattice
which xcodegen || echo "xcodegen not on PATH — check apple/Scripts/ for how project.yml normally gets regenerated before assuming"
```

If `xcodegen` is available, run it from `apple/`:

```bash
cd /Users/francis/workspc/lattice/apple
xcodegen generate
```

- [x] **Step 2: Write the 2-step join flow**

Create `apple/Lattice/JoinView.swift`:

```swift
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

/// Two-step full-screen join flow:
///   1. Server URL + enrollment token (scan or paste) + device name → creates
///      the VPN profile (TunnelManager.saveJoin) and connects.
///   2. Admin username/password → LatticeAPI.shared.login. Required because
///      the peer-list API needs an admin Bearer token, not the enrollment
///      token (confirmed against the live backend — this is a real backend
///      constraint, not a design choice).
struct JoinView: View {
    var onFinished: () -> Void

    private enum Step { case network, login }
    @State private var step: Step = .network

    // Step 1 state
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var joinToken = ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var networkError = ""
    @State private var showingScanner = false
    @State private var scannerError = ""

    // Step 2 state
    @State private var username = "admin"
    @State private var password = ""
    @State private var isLoggingIn = false
    @State private var loginError = ""

    var body: some View {
        NavigationStack {
            switch step {
            case .network: networkStep
            case .login: loginStep
            }
        }
    }

    private var networkStep: some View {
        Form {
            Section("扫码加入") {
                Button {
                    showingScanner = true
                } label: {
                    Label("扫描二维码", systemImage: "qrcode.viewfinder")
                }
            }

            Section("或手动输入") {
                TextField("服务器 URL (http://…)", text: $serverURL)
                    .keyboardType(.URL)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                SecureField("入网令牌", text: $joinToken)
                TextField("节点名称", text: $deviceName)
            }

            if !networkError.isEmpty {
                Text(networkError).font(.caption).foregroundColor(.red)
            }

            Section {
                Button {
                    saveAndConnect()
                } label: {
                    if isSavingNetwork {
                        ProgressView()
                    } else {
                        Text("加入网络")
                    }
                }
                .disabled(serverURL.isEmpty || joinToken.isEmpty || isSavingNetwork)
            }
        }
        .navigationTitle("加入网络")
        .sheet(isPresented: $showingScanner) {
            NavigationStack {
                ZStack(alignment: .bottom) {
                    QRScannerView(
                        onCode: { code in
                            handleScanned(code)
                            showingScanner = false
                        },
                        onError: { message in
                            scannerError = message
                        }
                    )
                    if !scannerError.isEmpty {
                        Text(scannerError)
                            .font(.caption)
                            .foregroundColor(.white)
                            .padding(8)
                            .background(.black.opacity(0.6))
                            .cornerRadius(8)
                            .padding(.bottom, 24)
                    }
                }
                .navigationTitle("扫描二维码")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("取消") { showingScanner = false }
                    }
                }
            }
        }
    }

    private func handleScanned(_ code: String) {
        guard let payload = JoinPayload(code) else {
            scannerError = "二维码格式不正确"
            return
        }
        if let server = payload.serverURL { serverURL = server }
        if let token = payload.token { joinToken = token }
    }

    private func saveAndConnect() {
        isSavingNetwork = true
        networkError = ""
        let trimmed = serverURL.hasSuffix("/") ? String(serverURL.dropLast()) : serverURL
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        UserDefaults.standard.set(deviceName, forKey: "lattice.nodeName")
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: joinToken, name: deviceName) { err in
            isSavingNetwork = false
            if let err {
                networkError = err
                return
            }
            joinToken = ""
            TunnelManager.shared.connect()
            step = .login
        }
    }

    private var loginStep: some View {
        Form {
            Section("登录管理面板") {
                Text("查看节点列表需要管理员账号。")
                    .font(.caption)
                    .foregroundColor(.secondary)
                TextField("用户名", text: $username)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                SecureField("密码", text: $password)
            }

            if !loginError.isEmpty {
                Text(loginError).font(.caption).foregroundColor(.red)
            }

            Section {
                Button {
                    Task { await login() }
                } label: {
                    if isLoggingIn {
                        ProgressView()
                    } else {
                        Text("登录")
                    }
                }
                .disabled(username.isEmpty || password.isEmpty || isLoggingIn)
            }
        }
        .navigationTitle("登录")
    }

    private func login() async {
        isLoggingIn = true
        loginError = ""
        defer { isLoggingIn = false }
        do {
            try await LatticeAPI.shared.login(user: username, pass: password)
            onFinished()
        } catch {
            loginError = "登录失败: \(error.localizedDescription)"
        }
    }
}
```

**Before finalizing this step, verify `LatticeAPI.shared.login(user:pass:)`'s exact parameter labels** by checking the moved file (`grep -n "func login" apple/Shared/LatticeAPI.swift`) — this plan's author read it as `login(user: String, pass: String) async throws` earlier this session but confirm it wasn't altered by Task 2's mechanical `sed` extraction (it shouldn't be, that step only relocates text, but verify rather than assume before shipping this call site).

- [x] **Step 3: Full iOS build**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'generic/platform=iOS Simulator' -configuration Debug build 2>&1 | tail -60
```

Expected: `** BUILD SUCCEEDED **`, zero errors.

- [x] **Step 4: Full macOS build (regression check — nothing in this task touches macOS files, but Tasks 1-4's shared-code moves did, so re-verify the whole chain still holds)**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
cd apple && xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -40
```

Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 5: Manual smoke test in the iOS Simulator**

```bash
cd /Users/francis/workspc/lattice/apple
xcrun simctl list devices available | grep -i "iPhone" | head -5
```

Boot whichever iPhone simulator that lists (note its UDID), then build-and-run for that specific device (not the generic destination used for compile-only verification above):

```bash
xcodebuild -project LatticeApple.xcodeproj -scheme Lattice -destination 'id=<UDID from above>' -configuration Debug build 2>&1 | tail -20
xcrun simctl boot <UDID> 2>&1 || true
open -a Simulator
xcrun simctl install <UDID> $(find ~/Library/Developer/Xcode/DerivedData -path "*Build/Products/Debug-iphonesimulator/Lattice.app" -print -quit)
xcrun simctl launch <UDID> io.lattice.ios
```

In the Simulator: confirm the join flow appears (Simulators have no real camera, so "扫描二维码" will show a camera-permission/no-camera error — that's expected in a Simulator, not a bug; use the manual paste fields instead), enter the server URL from this session's live `latticed` instance if it's still running (`http://127.0.0.1:18090` is not reachable from inside a Simulator's isolated network the same way `127.0.0.1` reaches the Mac host directly for Simulators specifically it usually does resolve to the host — verify this works or use the Mac's LAN IP if not), enter one of the join tokens generated earlier this session (or generate a fresh one via the same curl recipe used throughout this session, since prior ones may have hit their usage limit), enter a device name, tap "加入网络", confirm it proceeds to the login step, log in with `admin`/`123456`, confirm it lands on the Status tab and the peer list populates.

- [x] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/Lattice/JoinView.swift apple/project.yml apple/LatticeApple.xcodeproj
git commit -s -m "feat(apple): iOS 2-step join flow (network + admin login), camera permission"
```

---

## Self-Review

**Spec coverage:**
- Shared architecture (Task 1-4) → matches the design doc's "Architecture: shared business logic, platform-specific screens" section exactly (LatticeAPI, TunnelManager, DesignComponents, join-payload parsing all move; platform-specific screens are NOT shared).
- Navigation structure (TabView: Status/Settings) → Task 5 Step 3 (`RootView`), Task 6.
- Status tab (toggle, own IP via `tunnel.statusText`/status, peer list with quality) → Task 5 Step 1. (Own overlay IP specifically: the design doc calls for showing it "once connected" — this plan's `StatusView` doesn't currently render it explicitly; flagged as a gap, see below.)
- Peer detail (read-only, no actions) → Task 5 Step 2.
- Settings tab + Exit Node → Task 6.
- Join flow (QR + paste) → Task 7, plus the admin-login second step discovered and confirmed with the user mid-brainstorming.
- Camera permission → Task 7 Step 1.
- iOS-specific constraints (background suspension → refresh on `scenePhase`/pull-to-refresh, NE memory ceiling → not a UI-scope concern, noted not built against) → addressed in Task 5 Step 1's `.refreshable`/`.onChange(of: scenePhase)`.

**Gap found and fixed:** the design doc's Status tab spec includes "own overlay IP, shown once connected" but the `StatusView` code in Task 5 Step 1 never surfaces it. Fix: `TunnelManager` doesn't currently expose the local overlay IP as a published property on either platform (it's known to the NE extension, not surfaced to the app via `sendProviderMessage` today — only `peerStates` is). Rather than invent new IPC in this already-large plan, the pragmatic fix is: the local overlay IP already appears as one of the rows in the peer list returned by `LatticeAPI.shared.listPeers()` — the entry whose `name` matches `UserDefaults.standard.string(forKey: "lattice.nodeName")` (the same `selfName` lookup `ExitNodeView` already uses). Added to Task 5 Step 1 conceptually here in the self-review — when implementing Task 5, add a small "本机" section above "节点" in `StatusView.body` showing `peers.first(where: { $0.name == UserDefaults.standard.string(forKey: "lattice.nodeName") })?.address`, using data already fetched by the same `loadPeers()` call, no new API needed.

**Placeholder scan:** every step has complete, runnable code. Three explicit "read the real file before finalizing" notes (Task 5 Step 1 for `HaloDot`/`QualityPill` signatures, Task 5 Step 2 for `PeerNode.publicKey` vs `appID`, Task 6 Step 3 for `LatticeAPI`'s exact UserDefaults/Keychain key names, Task 7 Step 2 for `login`'s parameter labels) are deliberate — they name the exact file and exact thing to verify, they are not vague "add error handling"-style placeholders, and each is grounded in something this plan's author read partially but not completely this session.

**Type consistency:** `TunnelManager.shared`, `.status`, `.statusText`, `.connectedBinding`, `.lastStartError`, `.peerStates`, `.serverURL`, `.isConfigured`, `.saveJoin(serverURL:token:name:completion:)`, `.connect()`, `.disconnect()`, `.load(_:)` are defined once in Task 1 and used with matching names/signatures in Tasks 5, 6, 7. `LatticeAPI.shared.listPeers()`, `.login(user:pass:)`, `.isLoggedIn`, `.listRouteSelections(_:)`, `.setRouteSelection(consumer:provider:selected:)`, `.setAdvertisedRoutes(_:routes:)` are defined once in Task 2 (by relocation, unchanged) and used consistently. `JoinPayload(_:)` from Task 4 is used identically in Task 7. `QRScannerView(onCode:onError:)` from Task 4 matches its Task 7 call site exactly.
