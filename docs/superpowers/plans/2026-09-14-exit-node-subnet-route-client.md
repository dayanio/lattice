# Exit Node / Subnet Route macOS Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the already-shipped backend (`docs/superpowers/plans/2026-09-14-exit-node-subnet-route-backend.md`) usable end to end from the macOS client: peers with advertised routes show up as pickable Exit Node / subnet-route providers in `NetworkSettingsView`, and a selected route actually becomes a system route on the Mac.

**Architecture:** The Go engine embedded in `LatticeTunnelMac` already applies WireGuard `AllowedIPs` correctly once the backend expands them (that part needs no client change — `wireguard-go`'s own cryptokey routing already works off whatever `AllowedIPs` the netmap carries). What's missing is purely the OS-routing and UI layers: (1) the management API needs to expose which peers have advertised routes so the picker has something to show, (2) the Go engine needs to tell Swift which extra CIDRs are now relevant so it can install matching `NEIPv4Route`s (WireGuard accepting a peer's traffic and macOS actually routing packets to the tunnel interface are two independent layers — both must agree), (3) `NetworkSettingsView`'s existing disabled placeholder needs real data.

**Tech Stack:** Go (engine, gomobile-bound), Swift/SwiftUI (macOS client, no XCTest target in this project — Swift-side verification in this plan is build success + manual runtime checks, not automated tests), NetworkExtension.

## Global Constraints

- **`export GOTOOLCHAIN=auto`** before any `go build`/`go test`/`gomobile` command (go.mod requires go >= 1.26.0, installed go is 1.25.8; confirmed working for `go build`/`go test` in the backend plan, and now confirmed working for `gomobile bind` too — see Task 5).
- **`export PATH="$PATH:/Users/francis/go/bin"`** before any `gomobile` command (that's where it's installed on this machine).
- This project has **no XCTest target** (`find apple -iname "*Tests*"` finds nothing). Go-side changes (Tasks 1-2) get real `go test` TDD. Swift-side changes (Tasks 3-4-5) are verified by `xcodebuild build` succeeding plus a manual runtime check with exact expected observable output (log lines, `scutil`/`netstat` output) — do not invent a fake Swift test target to force TDD where the codebase has none.
- Community-only feature, standalone-mode only — nothing here touches the iOS target (`apple/LatticeTunnel/`) or K8s mode.
- Match existing code style: license header on every new/modified `.go`/`.swift` file's neighbors already have one (don't add one to Swift files — check: existing `.swift` files in this repo already start with the same Apache header block used in `.go` files, copy it verbatim), same JSON-in-text-column convention as `Peer.Labels`/`Peer.AdvertisedRoutes` for any new persisted field.
- Design reference for exact UI/behavior: `docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md` §六 (macOS 客户端行为) and its `〇、评审记录` section (documents the provider-side NAT/forwarding gap this plan does NOT close — see "What this plan does not cover" at the end).

---

### Task 1: Expose `AdvertisedRoutes` on the peer list API

**Files:**
- Modify: `internal/server/service/peer.go`
- Modify: `internal/server/vo/peer.go`
- Modify: `internal/server/service/peer_list_standalone_test.go`

**Interfaces:**
- Consumes: `models.Peer.AdvertisedRoutes string` (already exists, backend plan Task 1)
- Produces: `vo.PeerVo.AdvertisedRoutes []string` — populated for standalone-mode peers, always `nil` for K8s-mode peers (out of scope, `LatticePeer` CRD has no equivalent field yet)

Without this, the client has no way to know which peers in a workspace have declared a route — `NetworkSettingsView`'s picker would have nothing to list.

- [ ] **Step 1: Write the failing test**

Add this test to `internal/server/service/peer_list_standalone_test.go` (same file, same `newRegisterService`/`seedEnrollmentToken` helpers the existing tests in it already use):

```go
func TestPeerService_ListPeersStandalone_IncludesAdvertisedRoutes(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "gw", AppID: "gw-app", Token: "enr-test-token"})
	require.NoError(t, err)
	_, err = svc.Register(ctx, &dto.PeerDto{Name: "plain", AppID: "plain-app", Token: "enr-test-token"})
	require.NoError(t, err)

	wsCtx := context.WithValue(ctx, infra.WorkspaceKey, "ws1")
	require.NoError(t, svc.SetAdvertisedRoutes(wsCtx, "gw", []string{"0.0.0.0/0"}))

	page, err := svc.ListPeers(wsCtx, &dto.PageRequest{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.List, 2)

	byName := map[string]vo.PeerVo{}
	for _, pv := range page.List {
		byName[pv.Name] = pv
	}
	assert.Equal(t, []string{"0.0.0.0/0"}, byName["gw"].AdvertisedRoutes)
	assert.Empty(t, byName["plain"].AdvertisedRoutes)
}
```

Check the top of this test file — it should already import `"github.com/alatticeio/lattice/internal/server/vo"`; add it if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/francis/workspc/lattice && export GOTOOLCHAIN=auto && go test ./internal/server/service/... -run TestPeerService_ListPeersStandalone_IncludesAdvertisedRoutes -v`
Expected: FAIL — `byName["gw"].AdvertisedRoutes undefined` (field doesn't exist on `vo.PeerVo` yet)

- [ ] **Step 3: Add the field to `vo.PeerVo`**

In `internal/server/vo/peer.go`, add next to the existing `Labels map[string]string` field:

```go
	// AdvertisedRoutes lists the CIDRs this peer offers to route for others
	// (see docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md).
	// Empty for peers that haven't declared anything, and always empty in
	// K8s mode (not supported there yet).
	AdvertisedRoutes []string `json:"advertisedRoutes,omitempty"`
```

- [ ] **Step 4: Thread it through `peerItem` and the standalone list path**

In `internal/server/service/peer.go`, add a field to the `peerItem` struct (next to `labels`):

```go
	advertisedRoutes []string
```

In `listPeersStandalone` (the loop building `allPeers` from `rows`, around where `labels` is unmarshaled from `r.Labels`), add right after the existing `labels` unmarshal line:

```go
		var advertisedRoutes []string
		_ = json.Unmarshal([]byte(r.AdvertisedRoutes), &advertisedRoutes)
```

and add `advertisedRoutes: advertisedRoutes,` to that loop's `peerItem{...}` literal (the K8s-mode loop's `peerItem{...}` literal is left unchanged — it has no source field to read this from, so `advertisedRoutes` stays its zero value `nil` there, which is correct per this task's scope).

In `renderPeerPage`, add `AdvertisedRoutes: n.advertisedRoutes,` to the `vo.PeerVo{...}` literal.

- [ ] **Step 5: Run test to verify it passes**

Run: `export GOTOOLCHAIN=auto && go test ./internal/server/service/... -run TestPeerService_ListPeersStandalone_IncludesAdvertisedRoutes -v`
Expected: PASS

- [ ] **Step 6: Run the full package + build**

Run: `export GOTOOLCHAIN=auto && go test ./internal/server/service/... ./internal/server/vo/... -v && go build ./... && gofmt -l internal/server/service/peer.go internal/server/vo/peer.go internal/server/service/peer_list_standalone_test.go`
Expected: all PASS, `gofmt -l` prints nothing

- [ ] **Step 7: Commit**

```bash
git add internal/server/service/peer.go internal/server/vo/peer.go internal/server/service/peer_list_standalone_test.go
git commit -s -m "feat(standalone): expose AdvertisedRoutes on the peer list API"
```

---

### Task 2: Engine route computation + `OnRoutesChanged` delegate

**Files:**
- Modify: `apple/engine/engine.go`
- Create: `apple/engine/routes.go`
- Create: `apple/engine/routes_test.go`

**Interfaces:**
- Consumes: `internal/agent/infra.Peer.AllowedIPs string` (comma-separated CIDRs), `node.GetPeerManager().GetAll() []*infra.Peer` (`internal/agent/node.go:713`, `internal/agent/infra/peer.go:121`)
- Produces: `EngineDelegate.OnRoutesChanged(routesJSON string)` (gomobile-generated Swift protocol method — this is a NEW method on the delegate interface, additive, not a breaking signature change to `OnTunnelUp`/`OnEvent`/`OnPeerStates`), `computeExtraRoutes(peers []*infra.Peer) []string` (pure function, unit-testable without any NE/gomobile dependency)

The backend already expands a selected provider's `AllowedIPs` beyond its own `/32` (e.g. `"10.96.0.4/32,192.168.1.0/24"`). Once the agent applies that netmap, WireGuard's own cryptokey routing already handles encrypting matching traffic to the right peer — that part needs no engine change. What's missing is telling the *macOS routing table* to send matching packets into the tunnel interface at all. This task computes which extra CIDRs (beyond plain `/32` peer addresses, which are already covered by the existing `10.96.0.0/24` overlay route) need to become `NEIPv4Route`s, and reports them to Swift.

- [ ] **Step 1: Write the failing test**

```go
// apple/engine/routes_test.go
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
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

func addr(s string) *string { return &s }

func TestComputeExtraRoutes_SkipsPlainSlash32Peers(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "plain", Address: addr("10.96.0.2"), AllowedIPs: "10.96.0.2/32"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 0 {
		t.Fatalf("computeExtraRoutes() = %v, want empty (no extra routes)", got)
	}
}

func TestComputeExtraRoutes_IncludesExpandedRoutes(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "plain", Address: addr("10.96.0.2"), AllowedIPs: "10.96.0.2/32"},
		{Name: "gw", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,192.168.1.0/24"},
	}
	got := computeExtraRoutes(peers)
	want := []string{"192.168.1.0/24"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("computeExtraRoutes() = %v, want %v", got, want)
	}
}

func TestComputeExtraRoutes_DedupesAcrossPeers(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "gw1", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,0.0.0.0/0"},
		{Name: "gw2", Address: addr("10.96.0.5"), AllowedIPs: "10.96.0.5/32,0.0.0.0/0"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Fatalf("computeExtraRoutes() = %v, want [0.0.0.0/0] deduped", got)
	}
}

func TestComputeExtraRoutes_EmptyInput(t *testing.T) {
	got := computeExtraRoutes(nil)
	if len(got) != 0 {
		t.Fatalf("computeExtraRoutes(nil) = %v, want empty", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/francis/workspc/lattice && export GOTOOLCHAIN=auto && go test ./apple/engine/... -run TestComputeExtraRoutes -v`
Expected: FAIL — `undefined: computeExtraRoutes`

- [ ] **Step 3: Implement `computeExtraRoutes`**

```go
// apple/engine/routes.go
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
	"sort"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

// computeExtraRoutes returns the deduped, sorted set of CIDRs across all
// peers' AllowedIPs that are NOT a peer's own /32 overlay address — i.e.
// the routes a selected Exit Node / subnet-route provider has expanded
// into this node's netmap (see netmap_builder.go's per-recipient
// expansion on the server side). Plain /32 peer addresses need no extra
// OS route: they're already covered by the base 10.96.0.0/24 overlay
// route every tunnel installs regardless of this feature.
func computeExtraRoutes(peers []*infra.Peer) []string {
	seen := make(map[string]struct{})
	for _, p := range peers {
		if p == nil || p.AllowedIPs == "" {
			continue
		}
		for _, cidr := range strings.Split(p.AllowedIPs, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" || strings.HasSuffix(cidr, "/32") {
				continue
			}
			seen[cidr] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for cidr := range seen {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `export GOTOOLCHAIN=auto && go test ./apple/engine/... -run TestComputeExtraRoutes -v`
Expected: PASS (all four cases)

- [ ] **Step 5: Wire it into the engine — add the delegate method and emit on tunnel-up + on change**

In `apple/engine/engine.go`, add to the `EngineDelegate` interface (next to `OnPeerStates`):

```go
	// OnRoutesChanged reports the current set of extra CIDRs (beyond the
	// base overlay /24) this node should route into the tunnel, as a JSON
	// array of strings, e.g. ["192.168.1.0/24"] or ["0.0.0.0/0"] for an
	// Exit Node. Emitted once when the tunnel comes up and again whenever
	// the set changes (a route was selected/deselected, or a selected
	// provider changed/cleared what it advertises).
	OnRoutesChanged(routesJSON string)
```

Add an emit helper next to `emitTunnelUp` (near the bottom of the file):

```go
func (e *Engine) emitRoutesChanged(routesJSON string) {
	if e.delegate != nil {
		e.delegate.OnRoutesChanged(routesJSON)
	}
}
```

In `run()`, right before the existing `e.emit(EventConnected)` / `e.emitTunnelUp(localIP)` pair (so the initial route set is already known to Swift by the time it applies network settings), add:

```go
	if blob, err := json.Marshal(computeExtraRoutes(node.GetPeerManager().GetAll())); err == nil {
		e.emitRoutesChanged(string(blob))
	}

	e.emit(EventConnected)
	e.emitTunnelUp(localIP)
```

Add a new poll loop next to `pollPeerStates`, started alongside it in `run()`:

```go
	go e.pollPeerStates(ctx, node)
	go e.pollRoutes(ctx, node)
```

```go
// pollRoutes watches the peer manager's AllowedIPs and pushes the extra-
// routes snapshot to Swift whenever it changes (a route selection changed,
// or RefreshConfig picked up a provider updating/clearing what it offers).
func (e *Engine) pollRoutes(ctx context.Context, node *latticeagent.Node) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			blob, err := json.Marshal(computeExtraRoutes(node.GetPeerManager().GetAll()))
			if err != nil {
				continue
			}
			if string(blob) == last {
				continue
			}
			last = string(blob)
			e.emitRoutesChanged(last)
		}
	}
}
```

(15 seconds matches `periodicRefresh`'s own cadence — routes only change after a `RefreshConfig` picks up a new netmap, so polling faster wouldn't observe anything new sooner.)

- [ ] **Step 6: Run the full package test + build**

Run:
```
export GOTOOLCHAIN=auto
go test ./apple/engine/... -v
go build ./apple/engine/...
go build ./...
gofmt -l apple/engine/engine.go apple/engine/routes.go apple/engine/routes_test.go
```
Expected: all PASS, `gofmt -l` prints nothing. `go build ./...` matters here because `EngineDelegate` gained a method — nothing in this Go codebase implements that interface directly (only gomobile-generated Swift glue does, which doesn't exist yet until Task 5's rebuild), so this should build clean with no other Go call site to fix.

- [ ] **Step 7: Commit**

```bash
git add apple/engine/engine.go apple/engine/routes.go apple/engine/routes_test.go
git commit -s -m "feat(apple): compute and report extra routes from the engine"
```

---

### Task 3: `PacketTunnelProvider` applies extra routes

**Files:**
- Modify: `apple/LatticeTunnelMac/PacketTunnelProvider.swift`

**Interfaces:**
- Consumes: `EngineDelegate.OnRoutesChanged` → gomobile generates this as a new required method on `LatticeEngineEngineDelegateProtocol` (Task 2). **This file will not compile until Task 5 regenerates `LatticeCore.xcframework`** — write this task's code now, but its build verification (Step 3 below) only actually passes after Task 5. Do the code change here; do the `xcodebuild` verification as part of Task 5's Step 4, not here.
- Produces: `makeSettings(overlayIP:extraRoutes:)` (signature change from the current `makeSettings(overlayIP:)` — its one call site in this same file is updated in this task)

This task makes the tunnel's installed routes track whatever the engine reports instead of only ever installing the fixed base overlay range.

- [ ] **Step 1: Store the latest routes and conform to the new delegate method**

In `apple/LatticeTunnelMac/PacketTunnelProvider.swift`, add a stored property next to `latestPeerStates`:

```swift
    /// Latest extra-routes snapshot from the engine (JSON array of CIDRs),
    /// applied as NEIPv4Routes once the tunnel is up. Empty until the first
    /// OnRoutesChanged call.
    private var latestExtraRoutes: [String] = []
```

Add the delegate method to the `LatticeEngineEngineDelegateProtocol` extension (next to `onPeerStates`):

```swift
    /// Extra CIDRs to route into the tunnel changed — reapply network
    /// settings if the tunnel is already up (first call, at startup, is a
    /// no-op here since onTunnelUp installs settings itself right after).
    func onRoutesChanged(_ routesJSON: String!) {
        guard let data = routesJSON?.data(using: .utf8),
              let routes = try? JSONDecoder().decode([String].self, from: data) else { return }
        latestExtraRoutes = routes
        guard pendingStart == nil else { return } // still starting up — onTunnelUp will apply this set
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: routes), completionHandler: nil)
    }
```

This references `currentOverlayIP`, which doesn't exist yet — add it as a stored property (next to `latestExtraRoutes`) and set it in `onTunnelUp`:

```swift
    private var currentOverlayIP = "10.96.0.1"
```

- [ ] **Step 2: Update `makeSettings` to take and apply extra routes**

Replace the existing `makeSettings` method:

```swift
    private func makeSettings(overlayIP: String) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        // Route the overlay range into the tunnel. No default route: Lattice
        // joins a mesh, it does not replace the uplink.
        ipv4.includedRoutes = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        settings.ipv4Settings = ipv4
        return settings
    }
```

with:

```swift
    private func makeSettings(overlayIP: String, extraRoutes: [String]) -> NEPacketTunnelNetworkSettings {
        let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: overlayIP)
        settings.mtu = 1280

        let ipv4 = NEIPv4Settings(addresses: [overlayIP], subnetMasks: ["255.255.255.255"])
        // Route the overlay range into the tunnel always. No default route
        // unless a selected Exit Node advertises 0.0.0.0/0 (handled below):
        // Lattice joins a mesh, it does not replace the uplink by default.
        var included = [NEIPv4Route(destinationAddress: "10.96.0.0", subnetMask: "255.255.255.0")]
        var excluded: [NEIPv4Route] = []

        for cidr in extraRoutes {
            guard let route = Self.ipv4Route(fromCIDR: cidr) else { continue }
            included.append(route)
        }

        // Exit Node (0.0.0.0/0): exclude this device's own control-plane
        // server from the tunnel, or every packet talking to it would loop
        // back through the tunnel it's trying to keep alive. The LRP relay
        // address isn't known on the Swift side yet — if traffic to it also
        // needs excluding, that's a follow-up once this is verified against
        // a real Exit Node (see the design doc's open item on this).
        if extraRoutes.contains("0.0.0.0/0"),
           let serverURL = (protocolConfiguration as? NETunnelProviderProtocol)?.providerConfiguration?["serverURL"] as? String,
           let host = URL(string: serverURL)?.host,
           let hostIP = Self.ipv4Route(fromCIDR: "\(host)/32") {
            excluded.append(hostIP)
        }

        ipv4.includedRoutes = included
        ipv4.excludedRoutes = excluded.isEmpty ? nil : excluded
        settings.ipv4Settings = ipv4
        return settings
    }

    /// Parses "a.b.c.d/n" into an NEIPv4Route. Returns nil for anything that
    /// isn't a plain dotted-quad CIDR (defense in depth — the engine already
    /// validates on the server side, but this is the last line before an OS
    /// API call that would otherwise silently no-op on a bad string).
    private static func ipv4Route(fromCIDR cidr: String) -> NEIPv4Route? {
        let parts = cidr.split(separator: "/")
        guard parts.count == 2, let prefixLen = UInt8(parts[1]), prefixLen <= 32 else { return nil }
        let address = String(parts[0])
        let mask = prefixLen == 0 ? "0.0.0.0" : ipv4SubnetMask(prefixLength: prefixLen)
        return NEIPv4Route(destinationAddress: address, subnetMask: mask)
    }

    private static func ipv4SubnetMask(prefixLength: UInt8) -> String {
        let mask: UInt32 = prefixLength == 0 ? 0 : ~UInt32(0) << (32 - prefixLength)
        return [24, 16, 8, 0].map { String((mask >> $0) & 0xFF) }.joined(separator: ".")
    }
```

- [ ] **Step 3: Update the `onTunnelUp` call site**

Replace:
```swift
    func onTunnelUp(_ overlayIP: String!) {
        NSLog("[Lattice] tunnel up, overlay IP \(overlayIP ?? "?")")
        setTunnelNetworkSettings(makeSettings(overlayIP: overlayIP ?? "10.96.0.1")) { [weak self] error in
```
with:
```swift
    func onTunnelUp(_ overlayIP: String!) {
        NSLog("[Lattice] tunnel up, overlay IP \(overlayIP ?? "?")")
        currentOverlayIP = overlayIP ?? "10.96.0.1"
        setTunnelNetworkSettings(makeSettings(overlayIP: currentOverlayIP, extraRoutes: latestExtraRoutes)) { [weak self] error in
```
(the rest of the closure body is unchanged).

- [ ] **Step 4: Defer build verification to Task 5**

This file references `onRoutesChanged` as a protocol conformance requirement that doesn't exist in the currently-checked-in `LatticeCore.xcframework` yet. Do not run `xcodebuild` for this task in isolation — it will fail with "does not conform to protocol" until Task 5 regenerates the framework from Task 2's updated `EngineDelegate`. Note this in your report; the actual build check happens in Task 5's Step 4.

- [ ] **Step 5: Commit**

```bash
git add apple/LatticeTunnelMac/PacketTunnelProvider.swift
git commit -s -m "feat(apple): apply extra routes reported by the engine"
```

---

### Task 4: `LatticeAPI` client methods + `PeerNode.advertisedRoutes`

**Files:**
- Modify: `apple/LatticeMac/LatticeMacApp.swift`
- Modify: `apple/Shared/TunnelCore.swift`

**Interfaces:**
- Consumes: `POST /api/v1/peers/{name}/advertised-routes`, `POST /api/v1/peers/{name}/route-selection`, `GET /api/v1/peers/{name}/route-selection` (backend plan Task 6), `vo.PeerVo.AdvertisedRoutes` (Task 1)
- Produces: `LatticeAPI.setAdvertisedRoutes(_:routes:)`, `LatticeAPI.setRouteSelection(consumer:provider:selected:)`, `LatticeAPI.listRouteSelections(_:) async throws -> [String]`, `PeerNode.advertisedRoutes: [String]`

This is pure client-side wiring — no backend change, no engine change. Buildable and verifiable in isolation (unlike Tasks 3/5), since it doesn't touch the NE extension or `LatticeCore`.

- [ ] **Step 1: Add `advertisedRoutes` to `PeerNode` and decode it**

In `apple/Shared/TunnelCore.swift`, add a field to `PeerNode` (next to `labels`):

```swift
    /// CIDRs this peer offers to route for others (Exit Node = ["0.0.0.0/0"]).
    var advertisedRoutes: [String] = []
```

In `apple/LatticeMac/LatticeMacApp.swift`, add a field to `PeerListResponse.PeerItem` (next to `labels`):

```swift
        let advertisedRoutes: [String]?
```

In `fetchPeers()`'s `PeerNode(...)` construction, add:

```swift
                advertisedRoutes: p.advertisedRoutes ?? []
```

- [ ] **Step 2: Add the three API methods**

In `LatticeAPI`, add next to `deletePeer`:

```swift
    /// Declares (or clears, if `routes` is empty) the CIDRs `name` offers to
    /// route for other peers in the workspace.
    func setAdvertisedRoutes(_ name: String, routes: [String]) async throws {
        try await request(method: "POST", path: "/api/v1/peers/\(encodePath(name))/advertised-routes",
                          body: ["routes": routes])
    }

    /// `consumer` opts in (selected: true) or out of `provider`'s advertised routes.
    func setRouteSelection(consumer: String, provider: String, selected: Bool) async throws {
        try await request(method: "POST", path: "/api/v1/peers/\(encodePath(consumer))/route-selection",
                          body: ["provider": provider, "selected": selected])
    }

    /// Provider names `consumer` currently has selected.
    func listRouteSelections(_ consumer: String) async throws -> [String] {
        let data = try await request(method: "GET", path: "/api/v1/peers/\(encodePath(consumer))/route-selection")
        struct Response: Codable { let data: [String]? }
        return try JSONDecoder().decode(Response.self, from: data).data ?? []
    }
```

(`encodePath` is the existing private helper already used by `setPeerDisabled`/`deletePeer` in this same class — reuse it, don't duplicate it.)

- [ ] **Step 3: Build and verify**

Run: `cd /Users/francis/workspc/lattice/apple && xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug -destination 'platform=macOS' build 2>&1 | tail -20`
Expected: `** BUILD SUCCEEDED **`. The `LatticeMac` target does link `LatticeCore.xcframework` in its build settings, but no `.swift` file in this target imports it or declares conformance to `LatticeEngineEngineDelegateProtocol` (only `PacketTunnelProvider.swift`, in the separate `LatticeTunnelMac` target, does that) — a protocol-conformance mismatch only breaks compilation for whichever file declares that conformance, so this target is unaffected by Task 3's pending-framework-rebuild issue and should build clean right now.

- [ ] **Step 4: Commit**

```bash
git add apple/LatticeMac/LatticeMacApp.swift apple/Shared/TunnelCore.swift
git commit -s -m "feat(apple): add advertised-routes/route-selection API client methods"
```

---

### Task 5: Rebuild `LatticeCore.xcframework` and verify the full app builds

**Files:**
- Regenerate (binary, already git-tracked): `apple/Frameworks/MacOS/LatticeCore.xcframework`

**Interfaces:**
- Consumes: Task 2's updated `EngineDelegate` (with `OnRoutesChanged`)
- Produces: an `LatticeCore.xcframework` whose generated Swift protocol includes `onRoutesChanged(_:)`, which Task 3's `PacketTunnelProvider` conformance needs to compile

This is the step that was blocked earlier by a Go toolchain mismatch; confirmed working now with the commands below (verified live against `apple/engine` during this plan's own research — real output, not assumed).

- [ ] **Step 1: Confirm the gomobile toolchain works**

```bash
export GOTOOLCHAIN=auto
export PATH="$PATH:/Users/francis/go/bin"
gomobile init
```
Expected: no output (success). If it errors, run `go install golang.org/x/mobile/cmd/gomobile@latest` first, then retry `gomobile init`.

- [ ] **Step 2: Rebuild the macOS xcframework**

```bash
cd /Users/francis/workspc/lattice
export GOTOOLCHAIN=auto
export PATH="$PATH:/Users/francis/go/bin"
rm -rf apple/Frameworks/MacOS/LatticeCore.xcframework
gomobile bind -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
```
Expected: the command completes (it can take a few minutes — this compiles the whole engine + wireguard-go + dependency tree for macOS) and `apple/Frameworks/MacOS/LatticeCore.xcframework/Info.plist` exists afterward.

- [ ] **Step 3: Confirm the new delegate method is actually in the generated bindings**

```bash
grep -rn "onRoutesChanged\|OnRoutesChanged" apple/Frameworks/MacOS/LatticeCore.xcframework/ 2>/dev/null | head -5
```
Expected: at least one match inside the generated Objective-C/Swift headers (proves the rebuild picked up Task 2's interface change, not a stale cached framework).

- [ ] **Step 4: Build the whole app and confirm Task 3 now compiles clean**

```bash
cd /Users/francis/workspc/lattice/apple
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug -destination 'platform=macOS' build 2>&1 | tail -40
```
Expected: `** BUILD SUCCEEDED **`, with no "does not conform to protocol LatticeEngineEngineDelegateProtocol" error (that error is exactly what would show up here if Task 3's `onRoutesChanged` implementation had a typo in its signature — gomobile-generated protocols require an exact match).

- [ ] **Step 5: Commit the rebuilt framework**

```bash
cd /Users/francis/workspc/lattice
git add apple/Frameworks/MacOS/LatticeCore.xcframework
git commit -s -m "chore(apple): rebuild LatticeCore.xcframework with OnRoutesChanged"
```

---

### Task 6: Wire `NetworkSettingsView` to real data

**Files:**
- Modify: `apple/LatticeMac/NetworkPages.swift`
- Modify: `apple/LatticeMac/ContentView.swift`

**Interfaces:**
- Consumes: `LatticeAPI.setAdvertisedRoutes`/`setRouteSelection`/`listRouteSelections` (Task 4), `PeerNode.advertisedRoutes` (Task 4), `TunnelManager.shared` (existing)
- Produces: `NetworkSettingsView` becomes a real, functioning settings screen instead of the disabled mockup shell

This is the last piece — the two `NetworkSettingsView` rows currently rendered disabled with `SoonBadge()` become real controls. MagicDNS stays disabled/`SoonBadge` (explicitly out of scope — separate roadmap item, no backend for it at all).

- [ ] **Step 1: Check how `NetworkSettingsView` currently receives its data**

Read `apple/LatticeMac/ContentView.swift` around both `UIState.shared.page = .networkSettings` call sites and the `NetworkSettingsView { ... }` construction (around line 68) to see exactly what's passed in today — the current constructor takes only `onBack: () -> Void`. This task changes that constructor's signature, so find every call site before editing (there should be exactly one construction site and the two places that trigger navigation to it — `grep -n "NetworkSettingsView" apple/LatticeMac/*.swift`).

- [ ] **Step 2: Turn `NetworkSettingsView` into a stateful view backed by real API calls**

Replace the `NetworkSettingsView` struct in `apple/LatticeMac/NetworkPages.swift` with:

```swift
struct NetworkSettingsView: View {
    var onBack: () -> Void

    @State private var candidates: [PeerNode] = []
    @State private var selectedProviders: Set<String> = []
    @State private var selfName: String = UserDefaults.standard.string(forKey: "lattice.deviceName") ?? (Host.current().localizedName ?? "")
    @State private var isLoading = true
    @State private var errorText = ""
    @State private var advertisingSubnet = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()

            settingsRow(
                title: "使用退出节点",
                desc: exitNodeDesc,
                trailing: { Text("›").font(.body).foregroundColor(.secondary) }
            )
            .onTapGesture { /* picker: see Step 3 */ }
            Divider().padding(.leading, 15)

            settingsRow(
                title: "广播子网路由",
                desc: "把本机所在局域网开放给 workspace 里的其它设备",
                trailing: {
                    Toggle("", isOn: Binding(
                        get: { advertisingSubnet },
                        set: { toggleAdvertiseSubnet($0) }
                    )).labelsHidden().toggleStyle(.switch).controlSize(.small)
                }
            )
            Divider().padding(.leading, 15)

            settingsRow(
                title: "MagicDNS",
                desc: "用节点名代替 overlay IP 互相访问",
                monoValue: "节点名.mac-demo.lattice.internal",
                trailing: { disabledToggle }
            )

            if !errorText.isEmpty {
                Text(errorText).font(.caption2).foregroundColor(.red)
                    .padding(.horizontal, 15).padding(.top, 6)
            }

            Spacer(minLength: 0)
            Divider()
            footerBar
        }
        .task { await load() }
    }

    private var exitNodeDesc: String {
        if let picked = selectedProviders.first(where: { candidates.first(where: { c in c.name == $0 })?.advertisedRoutes.contains("0.0.0.0/0") == true }) {
            return "当前：\(picked)"
        }
        return "全部流量经由所选节点转发 · 当前：无"
    }

    private func load() async {
        isLoading = true
        errorText = ""
        do {
            let peers = try await LatticeAPI.shared.listPeers()
            candidates = peers.filter { !$0.advertisedRoutes.isEmpty }
            let selected = try await LatticeAPI.shared.listRouteSelections(selfName)
            selectedProviders = Set(selected)
            if let mine = peers.first(where: { $0.name == selfName }) {
                advertisingSubnet = !mine.advertisedRoutes.isEmpty && !mine.advertisedRoutes.contains("0.0.0.0/0")
            }
        } catch {
            errorText = "加载失败: \(error.localizedDescription)"
        }
        isLoading = false
    }

    private func toggleAdvertiseSubnet(_ on: Bool) {
        advertisingSubnet = on
        Task {
            do {
                // MVP: hand-entered CIDR isn't collected by this pass — see
                // the design doc §6.2 note that auto-detecting the local
                // subnet is deferred. Advertise a placeholder-free empty
                // set when turning off; turning on with no real CIDR input
                // UI yet is intentionally a no-op beyond persisting the
                // toggle, until a CIDR entry field is added.
                if !on {
                    try await LatticeAPI.shared.setAdvertisedRoutes(selfName, routes: [])
                }
            } catch {
                errorText = "更新失败: \(error.localizedDescription)"
                advertisingSubnet = !on
            }
        }
    }

    // ... existing disabledToggle/header/settingsRow/footerBar unchanged from before
}
```

**Stop and re-read the design doc's §6.2 before continuing past this step.** The design explicitly scoped subnet-route advertising to "手填 CIDR, 先不做自动探测" (a hand-typed CIDR field, not auto-detection) — the code above intentionally leaves the "turn on" path as a no-op beyond the toggle state, because building a real CIDR-entry sheet is a UI task the design doc didn't fully specify (no mockup for a text-entry sheet exists). **Do not invent that sub-UI on your own** — if you reach this point, report DONE_WITH_CONCERNS and describe exactly what's missing (a CIDR input sheet for the "advertise subnet" flow) rather than guessing at its shape. The Exit Node *picker* (Step 3 below) is fully specified and should be completed normally.

- [ ] **Step 3: Add the Exit Node picker**

Add a `@State private var showingPicker = false` property and a `.sheet(isPresented: $showingPicker) { ... }` modifier on the root `VStack`, replacing the `.onTapGesture` placeholder from Step 2:

```swift
    .sheet(isPresented: $showingPicker) {
        exitNodePicker
    }
```

```swift
    private var exitNodePicker: some View {
        NavigationStack {
            List {
                Button("无（关闭）") { Task { await selectExitNode(nil) } }
                ForEach(candidates.filter { $0.advertisedRoutes.contains("0.0.0.0/0") }) { peer in
                    Button(peer.name) { Task { await selectExitNode(peer.name) } }
                }
            }
            .navigationTitle("选择退出节点")
        }
        .frame(width: 280, height: 320)
    }

    private func selectExitNode(_ name: String?) async {
        do {
            for provider in selectedProviders where candidates.first(where: { $0.name == provider })?.advertisedRoutes.contains("0.0.0.0/0") == true {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: provider, selected: false)
            }
            if let name {
                try await LatticeAPI.shared.setRouteSelection(consumer: selfName, provider: name, selected: true)
            }
            showingPicker = false
            await load()
        } catch {
            errorText = "选择失败: \(error.localizedDescription)"
        }
    }
```

And change the `.onTapGesture` on the "使用退出节点" row (from Step 2) to `{ showingPicker = true }`.

- [ ] **Step 4: Build and manually verify**

Run: `cd /Users/francis/workspc/lattice/apple && xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug -destination 'platform=macOS' build 2>&1 | tail -40`
Expected: `** BUILD SUCCEEDED **`

**Manual runtime verification** (no automated test exists for this — this is the actual proof the feature works, do not skip it or claim done without doing it):

1. Confirm `latticed --standalone` is running (`ps aux | grep latticed`) with at least two enrolled peers.
2. Via `curl` (same pattern as the backend plan's Task 6 live-verification step), make one existing peer advertise `0.0.0.0/0`: `curl -X POST -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WS" -d '{"routes":["0.0.0.0/0"]}' http://127.0.0.1:8080/api/v1/peers/<some-peer>/advertised-routes`.
3. Launch/reload `LatticeMac.app`, open the main window, go to 网络设置. Confirm the peer from step 2 appears as a pickable Exit Node candidate (not before this point — it shouldn't appear if you skip step 2).
4. Select it. Confirm no error banner appears.
5. Run `scutil --nc list` / `netstat -rn -f inet | grep default` — confirm a new default-route-shaped entry appears via the Lattice tunnel interface (or, if `0.0.0.0/0` handling needs the interface to already show a route change, check `ifconfig utun<N>` for the tunnel and `netstat -rn` for a route via it).
6. Open a real webpage in a browser. Confirm normal internet access still works (proves the `excludedRoutes` control-plane exclusion didn't create a routing deadlock) — if it hangs, that's the exact scenario the design doc flagged as needing packet-capture verification; report it as a concern rather than silently declaring success.
7. Deselect (choose "无（关闭）" in the picker). Confirm the route reverts (`netstat -rn` no longer shows it).

Record the actual output of steps 5-7 in your report — not just "worked", the real `netstat`/`scutil` lines.

- [ ] **Step 5: Commit**

```bash
git add apple/LatticeMac/NetworkPages.swift apple/LatticeMac/ContentView.swift
git commit -s -m "feat(apple): wire NetworkSettingsView to real Exit Node selection"
```

---

## What this plan does not cover

- **Provider-side NAT/forwarding.** Selecting an Exit Node makes the *consumer* route traffic into the tunnel, but nothing in this plan (or the backend plan) makes the *provider* peer actually forward and NAT that traffic out its own uplink. Without that, traffic reaches the provider and gets dropped. This is flagged explicitly in the design doc's `〇、评审记录` as a real gap — likely a `provision_linux.go`-level change (the existing masquerade rule is `-o wf0`, i.e. into the overlay, not out of it) plus an `ip_forward` sysctl toggle on the provider. Out of scope here; needs its own design pass.
- **Subnet-route CIDR entry UI.** Task 6 intentionally stops short of a full "type in your local subnet" sheet — flagged inline in that task.
- **MagicDNS.** Untouched, stays disabled — separate roadmap item with no backend at all yet.
- **iOS.** Not touched. `apple/LatticeTunnel/` isn't part of this plan.
