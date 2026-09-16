# Netmap Push Notification + Static Endpoint UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the control plane a way to proactively tell already-connected agents "something about your netmap changed, refresh now" over the existing NATS connection, instead of relying purely on the agent's 15s poll + 45s handshake-staleness fallback; and expose the already-implemented "operator-pinned peer endpoint" backend capability (`PUT /api/v1/peers/update` with an `endpoint` field) through both the Vue web console and the macOS app UI, since today it only exists as a raw API call nobody can reach from a UI.

**Architecture:** A new NATS subject per peer (`lattice.signals.peers.<appID>.netmap`, distinct from the existing ICE-signal subject `lattice.signals.peers.<appID>` so it never collides with `signal.SignalPacket` wire parsing) carries a tiny "something changed" ping. `peerService` (the same code path serving both standalone and K8s-mode `ListPeers`/`UpdatePeer`/etc., differing only by `p.client == nil`) publishes to this subject for every other currently-known peer in the workspace whenever a peer record or its advertised-routes/route-selection changes. Agents subscribe to their own `.netmap` subject once at startup and call the already-existing `Node.RefreshConfig(ctx)` on receipt — no new refresh logic needed, just a faster trigger for the one that exists. The endpoint field already round-trips correctly on write (`registerStandalone`/`updatePeerStandalone` already persist it); it is simply never read back out to `GET /api/v1/peers/list`, so the UI work is entirely about closing that read-path gap and adding a form field on both clients.

**Tech Stack:** Go (`github.com/nats-io/nats.go`), Vue 3 + Pinia + shadcn-vue (`frontend/`), SwiftUI (`apple/LatticeMac/`).

## Global Constraints

- `GOTOOLCHAIN=auto` before any `go build`/`go test` — `go.mod` requires go >= 1.26.0, the machine's default `go` is 1.25.8.
- Lint with the local binary directly: `/Users/francis/go/bin/golangci-lint run ./...` (or `GOTOOLCHAIN=auto` prefixed if it complains about toolchain version) — do **not** run `make lint`, it tries to download a pinned version and this sandbox's network to that mirror is unreliable.
- Every Go file starts with the Apache 2.0 license header (copy from any existing file in the touched package).
- Commit convention: `git commit -s` (DCO sign-off), author identity from `git config user.name`/`user.email`, no `Co-Authored-By` trailers — except this session's actual attribution requirement below, which the harness adds automatically; don't fight it.
- This plan is executed **inline in the current session**, directly against the live `new_dev` branch (not a fresh worktree) — the working tree already has a running `latticed --standalone` (PID visible via `ps aux | grep latticed`), a Docker container peer under `lattice-run-test`, and a built `LatticeMac.app` under `~/Library/Developer/Xcode/DerivedData/LatticeApple-*/Build/Products/Debug/`. Re-verify these are still up before using them for manual testing; they may have been torn down between conversation turns.
- Scope boundary (confirmed with user): the K8s path only needs to cover changes made **through the management API** (i.e. `peerService`'s K8s-mode branch, `p.client != nil`) — a raw `kubectl edit`/`kubectl apply` on a `LatticePeer`/`LatticePolicy` CRD that bypasses the API entirely is a known, intentionally out-of-scope gap (the separate `PeerReconciler` process in `internal/agent/controller/run.go` has no NATS connection today, and wiring one up is a materially bigger task saved for later).

---

### Task 1: Expose the persisted `endpoint` field on the peer list read path

**Files:**
- Modify: `internal/server/service/peer.go:143-153` (`peerItem` struct)
- Modify: `internal/server/service/peer.go:180-195` (`ListPeers`, K8s branch — note: K8s `LatticePeer` CRD has no endpoint concept today, leave it empty there)
- Modify: `internal/server/service/peer.go:215-236` (`listPeersStandalone`)
- Modify: `internal/server/service/peer.go:241-` (`renderPeerPage`, wherever it builds each `vo.PeerVo` from a `peerItem`)
- Test: `internal/server/service/peer_test.go` (add a case if one doesn't already assert on `listPeersStandalone`'s output; otherwise extend the closest existing test)

**Interfaces:**
- Produces: `peerItem.endpoint string` field, populated in `listPeersStandalone` from `r.Endpoint` (the `models.Peer` DB row already has this column per Task I's earlier work — verify with `grep -n "Endpoint" internal/server/models/peer.go`).
- Consumes: `vo.PeerVo.Endpoint string` (already exists at `internal/server/vo/peer.go:19`, `json:"endpoint,omitempty"` — no VO change needed, just make sure it actually gets set).

- [ ] **Step 1: Add the field to `peerItem`**

In `internal/server/service/peer.go`, change:

```go
type peerItem struct {
	name             string
	displayName      string
	appId            string
	publicKey        string
	namespace        string
	address          *string
	labels           map[string]string
	advertisedRoutes []string
	disabled         bool
}
```

to:

```go
type peerItem struct {
	name             string
	displayName      string
	appId            string
	publicKey        string
	namespace        string
	address          *string
	labels           map[string]string
	advertisedRoutes []string
	disabled         bool
	endpoint         string
}
```

- [ ] **Step 2: Populate it in `listPeersStandalone`**

Find this block inside `listPeersStandalone`:

```go
		allPeers = append(allPeers, peerItem{
			name:             r.Name,
			displayName:      r.Description,
			appId:            r.AppID,
			publicKey:        r.PublicKey,
			namespace:        workspace.Namespace,
			address:          &address,
			labels:           labels,
			advertisedRoutes: advertisedRoutes,
			disabled:         r.Disabled,
		})
```

Add `endpoint: r.Endpoint,` as a new field:

```go
		allPeers = append(allPeers, peerItem{
			name:             r.Name,
			displayName:      r.Description,
			appId:            r.AppID,
			publicKey:        r.PublicKey,
			namespace:        workspace.Namespace,
			address:          &address,
			labels:           labels,
			advertisedRoutes: advertisedRoutes,
			disabled:         r.Disabled,
			endpoint:         r.Endpoint,
		})
```

- [ ] **Step 3: Copy it through in `renderPeerPage`**

`renderPeerPage` loops over `allPeers` (a `[]peerItem`) and builds `vo.PeerVo` values (look for the `vo.PeerVo{` literal inside the function — it currently sets `Name`, `DisplayName`, `AppID`, etc. from the same `peerItem`). Add `Endpoint: n.endpoint,` to that struct literal, using whatever the loop variable is named (read the surrounding 20 lines first to get the exact variable name — it's `n` in both `ListPeers` and `listPeersStandalone`'s call sites feeding into it, but confirm inside `renderPeerPage` itself before editing, since it may re-bind to a different loop variable name there).

- [ ] **Step 4: Verify by hand against the live standalone environment**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go build ./... 
```
Expected: no output, exit 0.

If `latticed` from earlier in this session is still running (`ps aux | grep latticed`), you can verify end-to-end without restarting anything — but the running binary is stale (built before this change), so restart it:

```bash
kill -9 $(pgrep -f "latticed --standalone") 2>/dev/null
cd /tmp/lattice-run-test
GOTOOLCHAIN=auto go build -o latticed /Users/francis/workspc/lattice/cmd/latticed
LATTICE_LISTEN="127.0.0.1:18090" LATTICE_SIGNALING_URL="nats://127.0.0.1:4222" LATTICE_RESYNC_INTERVAL=1s \
  ./latticed --standalone --config-dir /tmp/lattice-run-test/cfg > /tmp/lattice-run-test/latticed4.log 2>&1 &
sleep 3
TOKEN=$(curl -s -X POST http://127.0.0.1:18090/api/v1/users/login -H "Content-Type: application/json" -d '{"username":"admin","password":"123456"}' | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['token'])")
WSID=$(curl -s "http://127.0.0.1:18090/api/v1/workspaces/list?page=1&pageSize=1" -H "Authorization: Bearer $TOKEN" | python3 -c "import json,sys; print(json.load(sys.stdin)['data']['list'][0]['id'])")
curl -s "http://127.0.0.1:18090/api/v1/peers/list?page=1&pageSize=5" -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WSID" | python3 -m json.tool
```
Expected: peer objects in the response now include an `"endpoint"` key (empty string or omitted for peers that never had one pinned — that's fine, it's `omitempty`).

- [ ] **Step 5: Commit**

```bash
cd /Users/francis/workspc/lattice
git add internal/server/service/peer.go
git commit -s -m "feat(standalone): surface pinned peer endpoint on the peer list API"
```

---

### Task 2: NATS push-notification primitive (`Publish` on `infra.SignalService`)

**Files:**
- Modify: `internal/agent/infra/transport.go` (the `SignalService` interface)
- Modify: `internal/server/nats/nats.go` (`NatsSignalService` and `noopSignalService` implementations)
- Create: `internal/agent/infra/notify.go` (subject-naming + a small helper both the standalone service code and, later, K8s-mode code in the same `peerService` can call)
- Test: `internal/agent/infra/notify_test.go`

**Interfaces:**
- Produces: `infra.SignalService.Publish(ctx context.Context, subject string, data []byte) error` (new interface method); `infra.NetmapChangedSubject(appID string) string` returning `"lattice.signals.peers." + appID + ".netmap"`; `infra.PublishNetmapChanged(ctx context.Context, signal SignalService, appID string) error` which no-ops cleanly if `signal == nil` and otherwise calls `signal.Publish(ctx, NetmapChangedSubject(appID), []byte("changed"))`.
- Consumes: nothing new — `natsgo.Conn.Publish(subject string, data []byte) error`, already used at `internal/server/nats/nats.go:168` inside `Send`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/infra/notify_test.go`:

```go
package infra

import "testing"

func TestNetmapChangedSubject(t *testing.T) {
	got := NetmapChangedSubject("node-a")
	want := "lattice.signals.peers.node-a.netmap"
	if got != want {
		t.Fatalf("NetmapChangedSubject(%q) = %q, want %q", "node-a", got, want)
	}
}

type fakeSignalService struct {
	published []struct {
		subject string
		data    []byte
	}
}

func (f *fakeSignalService) Send(_ context.Context, _ PeerID, _ []byte) error { return nil }
func (f *fakeSignalService) Request(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeSignalService) Service(_, _ string, _ func([]byte) ([]byte, error)) {}
func (f *fakeSignalService) Flush() error                                       { return nil }
func (f *fakeSignalService) Close() error                                       { return nil }
func (f *fakeSignalService) Publish(_ context.Context, subject string, data []byte) error {
	f.published = append(f.published, struct {
		subject string
		data    []byte
	}{subject, data})
	return nil
}

func TestPublishNetmapChanged(t *testing.T) {
	f := &fakeSignalService{}
	if err := PublishNetmapChanged(context.Background(), f, "node-a"); err != nil {
		t.Fatalf("PublishNetmapChanged: %v", err)
	}
	if len(f.published) != 1 || f.published[0].subject != "lattice.signals.peers.node-a.netmap" {
		t.Fatalf("unexpected publish calls: %+v", f.published)
	}
}

func TestPublishNetmapChangedNilSignalIsNoop(t *testing.T) {
	if err := PublishNetmapChanged(context.Background(), nil, "node-a"); err != nil {
		t.Fatalf("PublishNetmapChanged with nil signal should no-op, got err: %v", err)
	}
}
```

(Add `"context"` to the import block — this file needs it for the fake's method signatures and the test bodies.)

- [ ] **Step 2: Run it to confirm it fails to compile** (the interface method and helper don't exist yet)

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go test ./internal/agent/infra/... 2>&1 | tail -20
```
Expected: compile error, something like `fakeSignalService does not implement SignalService (missing method Publish)` and `undefined: NetmapChangedSubject`.

- [ ] **Step 3: Add `Publish` to the interface**

In `internal/agent/infra/transport.go`, the interface currently reads:

```go
type SignalService interface {
	// Send routes a packet to the peer identified by PeerID (NATS subject level).
	// PeerID is sufficient here — full PeerIdentity is not needed for routing.
	Send(ctx context.Context, peerId PeerID, data []byte) error

	//req/resp
	Request(ctx context.Context, subject, method string, data []byte) ([]byte, error)

	// server service
	Service(subject, queue string, service func(data []byte) ([]byte, error))

	Flush() error

	// Close drains in-flight messages and closes the underlying connection.
	Close() error
}
```

Add a `Publish` method:

```go
type SignalService interface {
	// Send routes a packet to the peer identified by PeerID (NATS subject level).
	// PeerID is sufficient here — full PeerIdentity is not needed for routing.
	Send(ctx context.Context, peerId PeerID, data []byte) error

	// Publish sends a raw payload to an arbitrary subject, for control-plane-
	// initiated notifications that don't fit the peer-to-peer Send() pattern
	// (e.g. "your netmap changed, refresh now").
	Publish(ctx context.Context, subject string, data []byte) error

	//req/resp
	Request(ctx context.Context, subject, method string, data []byte) ([]byte, error)

	// server service
	Service(subject, queue string, service func(data []byte) ([]byte, error))

	Flush() error

	// Close drains in-flight messages and closes the underlying connection.
	Close() error
}
```

- [ ] **Step 4: Implement it on both concrete types**

In `internal/server/nats/nats.go`, add next to the existing `Send` method (around line 166-169):

```go
func (s *NatsSignalService) Publish(_ context.Context, subject string, data []byte) error {
	return s.nc.Publish(subject, data)
}
```

And next to `noopSignalService.Send` (around line 43-45):

```go
func (n *noopSignalService) Publish(_ context.Context, _ string, _ []byte) error {
	return nil
}
```

- [ ] **Step 5: Create `internal/agent/infra/notify.go`**

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

package infra

import "context"

// NetmapChangedSubject returns the NATS subject an agent with the given
// AppID subscribes to for "your netmap changed, refresh now" notifications.
// Deliberately a different subject than the per-peer ICE-signal channel
// (lattice.signals.peers.<appID>) so this never collides with
// signal.SignalPacket parsing on that channel.
func NetmapChangedSubject(appID string) string {
	return "lattice.signals.peers." + appID + ".netmap"
}

// PublishNetmapChanged notifies appID's agent that something in its netmap
// changed, so it should refresh sooner than its next poll/liveness cycle.
// The payload content doesn't matter today (the agent just re-fetches the
// full netmap on receipt) — a fixed "changed" body keeps this forward
// compatible if a reason ever needs to go in it later.
// A nil signal (e.g. a peerService instance constructed without one, see
// internal/server/service/token.go) is a silent no-op, not an error.
func PublishNetmapChanged(ctx context.Context, signal SignalService, appID string) error {
	if signal == nil {
		return nil
	}
	return signal.Publish(ctx, NetmapChangedSubject(appID), []byte("changed"))
}
```

- [ ] **Step 6: Run the test again to confirm it passes**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go test ./internal/agent/infra/... -run 'TestNetmapChangedSubject|TestPublishNetmapChanged' -v 2>&1 | tail -30
```
Expected: all three tests `PASS`.

- [ ] **Step 7: Full build + lint to catch any other type implementing `SignalService` that now needs the new method**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go build ./... 2>&1 | tail -40
```
If this fails with "does not implement SignalService (missing method Publish)" on some other type (search with `grep -rn "infra.SignalService = " --include=*.go .` to find all compile-time assertions first), add the same trivial `Publish` passthrough/no-op to that type too before moving on.

```bash
/Users/francis/go/bin/golangci-lint run ./internal/agent/infra/... ./internal/server/nats/... 2>&1 | tail -30
```
Expected: `0 issues.`

- [ ] **Step 8: Commit**

```bash
cd /Users/francis/workspc/lattice
git add internal/agent/infra/notify.go internal/agent/infra/notify_test.go internal/agent/infra/transport.go internal/server/nats/nats.go
git commit -s -m "feat(transport): add NATS Publish primitive for control-plane-initiated notifications"
```

---

### Task 3: Wire the notification into `peerService`'s mutation points

**Files:**
- Modify: `internal/server/service/peer.go` (struct fields, constructor, mutation methods)
- Modify: `internal/server/controller/peer.go` (`NewPeerController` signature)
- Modify: `internal/server/server/server.go:322` (the `NewPeerController(...)` call site)
- Modify: `internal/server/service/token.go:117` (the other `NewPeerService(...)` call site)
- Test: extend `internal/server/service/peer_test.go` with one case per notified mutation, asserting the fake signal service recorded a publish to the right subject(s)

**Interfaces:**
- Consumes: `infra.PublishNetmapChanged(ctx, signal, appID)` from Task 2.
- Produces: `peerService.signal infra.SignalService` field; `NewPeerService(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier, signal infra.SignalService) PeerService` (new 5th parameter); `NewPeerController(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier, signal infra.SignalService) PeerController` (same new parameter, threaded through).

- [ ] **Step 1: Add the field and thread it through the constructor**

In `internal/server/service/peer.go`, the struct:

```go
type peerService struct {
	logger          *log.Logger
	client          *resource.Client
	store           store.Store
	presence        *managementnats.NodePresenceStore
	licenseVerifier license.Verifier
	// netmapBuilder serves netmaps from the standalone DB registry when
	// no K8s client exists (client == nil).
	netmapBuilder *reconcilers.NetmapBuilder
}
```

becomes:

```go
type peerService struct {
	logger          *log.Logger
	client          *resource.Client
	store           store.Store
	presence        *managementnats.NodePresenceStore
	licenseVerifier license.Verifier
	// netmapBuilder serves netmaps from the standalone DB registry when
	// no K8s client exists (client == nil).
	netmapBuilder *reconcilers.NetmapBuilder
	// signal notifies already-connected peers to refresh sooner than their
	// next poll cycle when something in the workspace's netmap changes.
	// May be nil (e.g. NewPeerService called from token.go's internal use) —
	// infra.PublishNetmapChanged handles that as a no-op.
	signal infra.SignalService
}
```

Find `func NewPeerService(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier) PeerService` (line 344) and its body — add the parameter and field assignment:

```go
func NewPeerService(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier, signal infra.SignalService) PeerService {
	return &peerService{
		// ...(keep every existing field assignment exactly as-is)...
		signal: signal,
	}
}
```

(Read the existing function body first — this plan doesn't repeat every existing field since they're unchanged, but the new `signal: signal,` line must be added to whatever composite literal is already there.)

- [ ] **Step 2: Update both call sites to compile**

`internal/server/controller/peer.go`:

```go
func NewPeerController(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier, signal infra.SignalService) PeerController {
	return &peerController{
		peerService:   service.NewPeerService(client, st, presence, verifier, signal),
		policyService: service.NewPolicyService(client, st),
	}
}
```

`internal/server/server/server.go:322`, change:

```go
		peerController:                controller.NewPeerController(client, st, presence, lv),
```

to:

```go
		peerController:                controller.NewPeerController(client, st, presence, lv, signal),
```

(`signal` is already in scope at that point — it's the same variable assigned to `s.nats` two lines above it, at `nats: signal,`.)

`internal/server/service/token.go:117`, change:

```go
		peerService:   NewPeerService(client, st, nil, license.NewVerifier("pro")),
```

to:

```go
		peerService:   NewPeerService(client, st, nil, license.NewVerifier("pro"), nil),
```

(`nil` signal here is intentional — this instance is only used internally by `tokenService` for a helper call unrelated to peer mutation notifications; `PublishNetmapChanged` treats `nil` as a no-op per Task 2.)

- [ ] **Step 3: Confirm it compiles before wiring in the actual notify calls**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go build ./... 2>&1 | tail -40
```
Expected: no output. If `internal/server/controller/peer.go` doesn't already import `"github.com/alatticeio/lattice/internal/agent/infra"`, add it — it's already imported in that file for other types per the earlier `grep`, so this should already be satisfied.

- [ ] **Step 4: Write the failing tests for the notify wiring**

In `internal/server/service/peer_test.go`, find how existing tests construct a `peerService` for standalone-mode testing (there should be an existing helper or inline literal building one with a `netmapBuilder` — search `netmapBuilder:` in that file). Using the same pattern, add:

```go
func TestUpdatePeerStandalone_NotifiesOtherPeers(t *testing.T) {
	// Reuse whatever store/db setup the existing standalone tests in this
	// file use (in-memory sqlite via gormstore, or a shared test helper) —
	// read the top of an existing standalone test in this file first and
	// copy its setup verbatim rather than inventing a new one.
	svc, st, fake := newTestPeerServiceForNotify(t) // see Step 5

	ctx := context.Background()
	ws := mustCreateWorkspace(t, st) // use whichever existing helper the file already has for this
	registerTwoStandalonePeers(t, ctx, svc, ws.ID) // helper: register "peer-a" and "peer-b" via svc.Register or the store directly, matching existing test conventions in this file

	fake.published = nil // clear anything from registration itself

	_, err := svc.UpdatePeer(ctx, &dto.PeerDto{Name: "peer-a", DisplayName: "renamed"})
	if err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}

	if len(fake.published) == 0 {
		t.Fatal("expected UpdatePeer to publish at least one netmap-changed notification")
	}
	foundPeerB := false
	for _, p := range fake.published {
		if p.subject == infra.NetmapChangedSubject("peer-b") {
			foundPeerB = true
		}
	}
	if !foundPeerB {
		t.Fatalf("expected a notification to peer-b's subject, got: %+v", fake.published)
	}
}
```

This test references helpers (`newTestPeerServiceForNotify`, `mustCreateWorkspace`, `registerTwoStandalonePeers`) that almost certainly don't match this file's real existing helper names — **before writing this test for real, read `internal/server/service/peer_test.go` in full** and rewrite this test body using its actual existing setup helpers and fixtures. The plan's job here is to specify the *assertion* (an `UpdatePeer` call results in a `PublishNetmapChanged`-shaped call to every *other* peer in the same workspace) — adapt the arrange/act boilerplate to match the file's established patterns exactly.

- [ ] **Step 5: Run it to confirm it fails**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go test ./internal/server/service/... -run TestUpdatePeerStandalone_NotifiesOtherPeers -v 2>&1 | tail -30
```
Expected: FAIL (no notification published yet — the mutation methods don't call `PublishNetmapChanged` yet).

- [ ] **Step 6: Add the notify calls to the standalone mutation methods**

In `internal/server/service/peer.go`, each of these methods needs to, on success, notify every *other* online peer in the same workspace (not the peer that just changed — it already has the fresh data it just wrote). Fetch the peer list for the workspace via `p.store.Peers().ListByWorkspace(ctx, workspaceID)` (already used elsewhere in this file, e.g. inside `registerStandalone`) and loop, skipping the one that changed:

```go
func (p *peerService) notifyWorkspacePeers(ctx context.Context, workspaceID, exceptAppID string) {
	rows, err := p.store.Peers().ListByWorkspace(ctx, workspaceID)
	if err != nil {
		p.logger.Warn("notifyWorkspacePeers: list failed", "err", err)
		return
	}
	for _, r := range rows {
		if r.AppID == exceptAppID {
			continue
		}
		if err := infra.PublishNetmapChanged(ctx, p.signal, r.AppID); err != nil {
			p.logger.Warn("notifyWorkspacePeers: publish failed", "appID", r.AppID, "err", err)
		}
	}
}
```

Add this as a new method on `peerService`, then call it (with the appropriate `workspaceID`/`exceptAppID` in scope at each site — read each function to find the right variable names, e.g. `tok.WorkspaceID` inside `registerStandalone`, `peer.WorkspaceID` inside `updatePeerStandalone`) at the end of, on the success path only:

- `registerStandalone` — after the peer is persisted (a brand-new peer joining is exactly the kind of change others should hear about)
- `updatePeerStandalone` — after `p.store.Peers().Update(...)` succeeds
- `setPeerDisabledStandalone` — after the disabled flag is persisted
- `SetAdvertisedRoutes` (the standalone branch) — after the DB write succeeds
- `SetRouteSelection` (the standalone branch) — after the DB write succeeds

For the **K8s-mode branches** of `UpdatePeer`, `DisablePeer`, `EnablePeer` (the `p.client != nil` paths, which call `p.client.Update(ctx, &peer)` against the `LatticePeer` CRD instead of the DB) — add the same kind of call, but since there's no `t_peer` table to list from in K8s mode, use `p.client.GetAPIReader().List(ctx, &peerList, client.InNamespace(workspace.Namespace))` (the same call `ListPeers`'s K8s branch already makes) to get the peer set to notify, keyed by `n.Spec.AppId`.

- [ ] **Step 7: Run the test again to confirm it passes**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go test ./internal/server/service/... -run TestUpdatePeerStandalone_NotifiesOtherPeers -v 2>&1 | tail -30
```
Expected: PASS.

- [ ] **Step 8: Run the full package test suite + lint**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go test ./internal/server/... 2>&1 | grep -Ev "^ok|no test files"
```
Expected: no output (everything either `ok` or has no test files).

```bash
/Users/francis/go/bin/golangci-lint run ./internal/server/... 2>&1 | tail -30
```
Expected: `0 issues.`

- [ ] **Step 9: Commit**

```bash
cd /Users/francis/workspc/lattice
git add internal/server/service/peer.go internal/server/service/peer_test.go internal/server/controller/peer.go internal/server/server/server.go internal/server/service/token.go
git commit -s -m "feat(standalone): notify peers over NATS when the netmap changes"
```

---

### Task 4: Agent subscribes and refreshes on notification

**Files:**
- Modify: `internal/agent/node.go` (add the subscription near the existing one at line 483)
- Test: manual (this is glue code around an already-tested `RefreshConfig`; a unit test would need a fake NATS server, which is disproportionate for a five-line subscription — verify by hand in Step 3 instead)

**Interfaces:**
- Consumes: `infra.NetmapChangedSubject(appID string) string` from Task 2; the existing `(*nats.NatsSignalService).Subscribe(subject string, handler nats.SignalHandler) error` used at line 483 for a different subject; the existing `(*Node).RefreshConfig(ctx context.Context) error`.

- [ ] **Step 1: Add the subscription**

In `internal/agent/node.go`, right after the existing subscription block (around line 483-485):

```go
	if err = natsSignalService.Subscribe(fmt.Sprintf("%s.%s", "lattice.signals.peers", localIdentity), node.probeFactory.Handle); err != nil {
		return nil, err
	}
```

`Subscribe`'s handler type is `SignalHandler = func(ctx context.Context, peerId infra.PeerID, packet *signal.SignalPacket) error` (from `internal/server/nats/nats.go:66`) — it always tries to JSON-unmarshal the payload into a `signal.SignalPacket` before calling the handler (see `NatsSignalService.Subscribe`'s implementation), so the netmap-changed subject **cannot reuse this same `Subscribe` method** — a plain `"changed"` byte payload isn't valid JSON for that struct and would fail to unmarshal before your handler ever runs.

Instead, subscribe directly on the raw NATS connection. Check whether `*nats.NatsSignalService` exposes the underlying `*natsgo.Conn` (it does not appear to publicly — `nc` is unexported at `internal/server/nats/nats.go:70`). Add a small dedicated method to `NatsSignalService` in `internal/server/nats/nats.go`, next to the existing `Subscribe`:

```go
// SubscribeRaw subscribes to subject with no payload parsing — for simple
// control-plane notifications (like netmap-changed pings) that aren't
// signal.SignalPacket-shaped.
func (s *NatsSignalService) SubscribeRaw(subject string, onMessage func()) error {
	_, err := s.nc.Subscribe(subject, func(_ *natsgo.Msg) {
		onMessage()
	})
	return err
}
```

Then in `internal/agent/node.go`, after the existing `Subscribe` call:

```go
	netmapSubject := infra.NetmapChangedSubject(localIdentity)
	if err = natsSignalService.SubscribeRaw(netmapSubject, func() {
		refreshCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if rErr := node.RefreshConfig(refreshCtx); rErr != nil {
			node.logger.Warn("netmap-changed notification: refresh failed", "err", rErr)
		}
	}); err != nil {
		return nil, err
	}
```

Check the imports at the top of `node.go` already include `"context"` and `"time"` (both near-certainly already imported given the rest of the file) before assuming this compiles as-is.

- [ ] **Step 2: Build**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go build ./... 2>&1 | tail -40
```
Expected: no output.

- [ ] **Step 3: Manually verify against the live standalone environment**

This is the same environment used throughout this session. If it's been torn down, recreate it per Task 1 Step 4's recipe first, then:

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto go build -o /tmp/lattice-run-test/lattice-linux-notify-test ./cmd/lattice
```

Run a container agent (mirrors the pattern used earlier this session), watch its logs, and — from a second shell — trigger a change via the API (rename the peer via `PUT /peers/update`) and confirm the container's log shows a "config update received"/refresh happening within a second or two of the API call, not after waiting up to 15s:

```bash
# terminal A: tail the new container's logs after starting it (see Task 1 Step 4
# and this session's earlier docker run recipe for the full container setup)
docker logs -f lattice-container-node

# terminal B, once the container has joined and you have its AppID:
TOKEN=... ; WSID=...  # as established earlier
curl -s -X PUT "http://127.0.0.1:18090/api/v1/peers/update" -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WSID" -H "Content-Type: application/json" -d '{"name":"<some-other-peer-appid>","displayName":"push-test"}'
```
Expected: terminal A shows a "config update received"/`ApplyFullConfig` log line appear within ~1s of the curl in terminal B, rather than up to 15s later.

- [ ] **Step 4: Commit**

```bash
cd /Users/francis/workspc/lattice
git add internal/agent/node.go internal/server/nats/nats.go
git commit -s -m "feat(agent): subscribe to netmap-changed notifications and refresh immediately"
```

---

### Task 5: Web UI — editable static endpoint field

**Files:**
- Modify: `frontend/src/stores/peerPage.ts` (`selectedNode` type, `handleSave`)
- Modify: `frontend/src/pages/manage/nodes/index.vue` (edit drawer template)
- Modify: `frontend/src/locales/zh-CN/manage.json` and `frontend/src/locales/en/manage.json` (`manage.nodes.detail.*` keys)

**Interfaces:**
- Consumes: `updatePeer` from `frontend/src/api/user.ts:36` (`request.put('/peers/update', data)`, unchanged — it already forwards whatever shape you give it); the backend now returns `endpoint` per Task 1.

- [ ] **Step 1: Add `endpoint` to the store's `selectedNode` shape and save payload**

In `frontend/src/stores/peerPage.ts`, the `selectedNode` ref type:

```ts
    const selectedNode = ref<{
        appId: string
        name?: string
        displayName?: string
        publicKey: string
        region?: string
        namespace?: string
        workspaceDisplayName?: string
        address?: string
        network?: string
        status?: string
        lastSeen?: string
        labels: string[]
    }>({
```

add `endpoint?: string` to the type:

```ts
    const selectedNode = ref<{
        appId: string
        name?: string
        displayName?: string
        publicKey: string
        region?: string
        namespace?: string
        workspaceDisplayName?: string
        address?: string
        network?: string
        status?: string
        lastSeen?: string
        endpoint?: string
        labels: string[]
    }>({
```

(No change needed to the object literal below the type — it's initialized without `endpoint`, which is fine since it's optional; `openDrawer` overwrites `selectedNode.value` wholesale from the row data anyway.)

In `handleSave`, the `runUpdate` call:

```ts
                await runUpdate({
                    ...selectedNode.value,
                    labels: labelMap,
                    displayName: selectedNode.value.displayName ?? '',
                })
```

already spreads `...selectedNode.value`, which will include `endpoint` once it's part of the shape and the drawer form (Step 2) writes into it — no change needed here beyond the type addition above.

- [ ] **Step 2: Add the form field in the edit drawer**

In `frontend/src/pages/manage/nodes/index.vue`, find the `customName` block (around line 822-832):

```vue
        <div v-if="store.drawerType === 'edit'" class="space-y-1.5">
          <p class="text-[11px] font-medium text-muted-foreground flex items-center gap-1.5">
            <Pencil class="size-3" /> {{ t('manage.nodes.detail.customName') }}
          </p>
          <Input
            v-model="store.selectedNode.displayName"
            :placeholder="store.selectedNode.name || store.selectedNode.appId"
            class="h-8 text-xs"
          />
          <p class="text-[10px] text-muted-foreground/50">{{ t('manage.nodes.detail.nameHint') }}</p>
        </div>

        <Separator v-if="store.drawerType === 'edit'" />
```

Add a matching block for `endpoint` right after it, before the `Separator`:

```vue
        <div v-if="store.drawerType === 'edit'" class="space-y-1.5">
          <p class="text-[11px] font-medium text-muted-foreground flex items-center gap-1.5">
            <Network class="size-3" /> {{ t('manage.nodes.detail.staticEndpoint') }}
          </p>
          <Input
            v-model="store.selectedNode.endpoint"
            placeholder="203.0.113.5:51820"
            class="h-8 text-xs font-mono"
          />
          <p class="text-[10px] text-muted-foreground/50">{{ t('manage.nodes.detail.staticEndpointHint') }}</p>
        </div>

        <Separator v-if="store.drawerType === 'edit'" />
```

`Network` is already imported from `lucide-vue-next` at the top of this file (line 9) — no new import needed.

- [ ] **Step 3: Add the locale strings**

In `frontend/src/locales/zh-CN/manage.json`, inside the `"detail"` object under `nodes` (around line 410-425), add two keys after `"nameHint"`:

```json
      "nameHint": "留空则显示系统默认名称",
      "staticEndpoint": "静态地址（高级）",
      "staticEndpointHint": "手动指定该节点的真实可达地址（IP:端口），用于自动打洞失败的场景，如 NAT 网络中已发布端口的容器。留空则走自动探测。",
```

In `frontend/src/locales/en/manage.json`, find the matching `nodes.detail.nameHint` key and add its English counterparts in the same position:

```json
      "nameHint": "Leave blank to show the system default name",
      "staticEndpoint": "Static Endpoint (Advanced)",
      "staticEndpointHint": "Manually pin this node's real reachable address (IP:port) for cases where automatic hole-punching fails, e.g. a container with a published port behind NAT. Leave blank to use automatic discovery.",
```

(Read both files first to get the exact surrounding key order and indentation right — copy the style of the neighboring `nameHint` line exactly.)

- [ ] **Step 4: Build the frontend to catch type/template errors**

```bash
cd /Users/francis/workspc/lattice/frontend
pnpm install --frozen-lockfile 2>&1 | tail -10
pnpm build 2>&1 | tail -60
```
Expected: `✓ built in ...`, no TypeScript or Vue template errors.

- [ ] **Step 5: Manual smoke test**

```bash
cd /Users/francis/workspc/lattice/frontend
pnpm dev
```
Open the dev server URL, log in (`admin`/`123456` against the standalone `latticed` from this session if it's still running on `127.0.0.1:18090` — otherwise point `VITE_API_BASE`/whatever this frontend's dev-server proxy config uses at it), go to the nodes page, open a node's edit drawer, confirm the new "静态地址" field appears between the display-name field and the labels section, type a value, save, reopen the drawer and confirm the value persisted (proves the round trip through Task 1's now-populated `endpoint` field in the list response).

- [ ] **Step 6: Restore the build artifact state and commit**

The `pnpm build` in Step 4 writes into `internal/web/dist/` (git-ignored except `.gitkeep`) — confirm it didn't get accidentally staged:

```bash
cd /Users/francis/workspc/lattice
git status --short internal/web/dist/
```
Expected: no output (everything in there stays ignored). If `.gitkeep` shows as modified/deleted, restore it: `git checkout -- internal/web/dist/.gitkeep`.

```bash
git add frontend/src/stores/peerPage.ts frontend/src/pages/manage/nodes/index.vue frontend/src/locales/zh-CN/manage.json frontend/src/locales/en/manage.json
git commit -s -m "feat(web): expose operator-pinned static endpoint in the node edit drawer"
```

---

### Task 6: macOS UI — "Set Static Endpoint" action

**Files:**
- Modify: `apple/LatticeMac/LatticeMacApp.swift` (new API method)
- Modify: `apple/LatticeMac/ContentView.swift` (state, alert, context-menu entry, call site)
- Modify: `apple/LatticeMac/PeerDetailView.swift` (new callback parameter, button)

**Interfaces:**
- Consumes: `LatticeAPI.shared.renamePeer(_:displayName:)` at `apple/LatticeMac/LatticeMacApp.swift:196` as the pattern to mirror exactly (same endpoint, different field).
- Produces: `LatticeAPI.shared.setPeerEndpoint(_ name: String, endpoint: String) async throws`.

- [ ] **Step 1: Add the API method**

In `apple/LatticeMac/LatticeMacApp.swift`, right after `renamePeer` (around line 196-199):

```swift
    /// Renames a peer (display name only — the peer's WG identity never changes).
    func renamePeer(_ name: String, displayName: String) async throws {
        try await request(method: "PUT", path: "/api/v1/peers/update",
                          body: ["name": name, "displayName": displayName])
    }

    /// Pins a peer's real reachable address (IP:port), bypassing automatic
    /// discovery for topologies where it fails (e.g. a NATed container with
    /// a published port). An empty string clears the pin, reverting to
    /// automatic discovery.
    func setPeerEndpoint(_ name: String, endpoint: String) async throws {
        try await request(method: "PUT", path: "/api/v1/peers/update",
                          body: ["name": name, "endpoint": endpoint])
    }
```

- [ ] **Step 2: Add state + alert in `ContentView.swift`**

Next to the existing rename state (around line 37-38):

```swift
    @State private var renameTarget: PeerNode?
    @State private var renameText = ""
```

add:

```swift
    @State private var endpointTarget: PeerNode?
    @State private var endpointText = ""
```

Next to the existing rename `.alert` (around line 79-88):

```swift
        .alert("重命名节点", isPresented: Binding(
            get: { renameTarget != nil },
            set: { if !$0 { renameTarget = nil } }
        )) {
            TextField("显示名称", text: $renameText)
            Button("保存") { Task { await renamePeer() } }
            Button("取消", role: .cancel) { renameTarget = nil }
        } message: {
            Text("只改显示名称，不影响节点的网络身份。")
        }
```

add a matching one right after it:

```swift
        .alert("设置静态地址", isPresented: Binding(
            get: { endpointTarget != nil },
            set: { if !$0 { endpointTarget = nil } }
        )) {
            TextField("IP:端口，如 203.0.113.5:51820", text: $endpointText)
            Button("保存") { Task { await setEndpoint() } }
            Button("取消", role: .cancel) { endpointTarget = nil }
        } message: {
            Text("手动指定该节点的真实可达地址，跳过自动打洞。留空清除，恢复自动探测。")
        }
```

- [ ] **Step 3: Add the async action function**

Next to `renamePeer()` (around line 489-498):

```swift
    private func renamePeer() async {
        guard let target = renameTarget else { return }
        renameTarget = nil
        do {
            try await LatticeAPI.shared.renamePeer(target.name, displayName: renameText)
            await loadPeers()
        } catch {
            opError = "重命名失败: \(error.localizedDescription)"
        }
    }
```

add:

```swift
    private func setEndpoint() async {
        guard let target = endpointTarget else { return }
        endpointTarget = nil
        do {
            try await LatticeAPI.shared.setPeerEndpoint(target.name, endpoint: endpointText)
            await loadPeers()
        } catch {
            opError = "设置静态地址失败: \(error.localizedDescription)"
        }
    }
```

- [ ] **Step 4: Wire it into `PeerDetailView`'s callback and the context menu**

`PeerDetailView` currently takes `onRename: (String) -> Void` (`apple/LatticeMac/PeerDetailView.swift:25`) and calls it with `peer.name` somewhere around line 289. Read that file's full `onRename` usage (the button/row that triggers it) first, then add a parallel `onSetEndpoint: (String) -> Void` parameter and an equivalent button right next to wherever the rename button/row lives, following that file's exact existing visual style (same button/row component, just a different icon — reuse `Network` if this file already imports something from the same icon set the rest of the app uses, otherwise match whatever icon convention is already established there).

Where `ContentView.swift` constructs `PeerDetailView` (around line 51-66):

```swift
                PeerDetailView(
                    peer: detail,
                    quality: tunnel.peerStates[detail.name],
                    onBack: { detailPeer = nil },
                    onRename: { name in
                        renameText = peers.first { $0.name == name }?.displayName ?? ""
                        renameTarget = detailPeer
                    },
                    onToggleDisabled: {
                        Task {
                            await toggleDisabled(detail)
                            detailPeer = peers.first { $0.name == detail.name }
                        }
                    },
                    onDelete: { deleteTarget = detail }
                )
```

add the new callback:

```swift
                PeerDetailView(
                    peer: detail,
                    quality: tunnel.peerStates[detail.name],
                    onBack: { detailPeer = nil },
                    onRename: { name in
                        renameText = peers.first { $0.name == name }?.displayName ?? ""
                        renameTarget = detailPeer
                    },
                    onSetEndpoint: { name in
                        endpointText = ""
                        endpointTarget = detailPeer
                    },
                    onToggleDisabled: {
                        Task {
                            await toggleDisabled(detail)
                            detailPeer = peers.first { $0.name == detail.name }
                        }
                    },
                    onDelete: { deleteTarget = detail }
                )
```

There is a second `onRename` construction site in `ContentView.swift` around line 249-255 (the `PeerRow`'s own context menu, per the earlier `contextMenu`/`onRename` grep results at lines 618-622) — check whether that call site also needs the same treatment for consistency (a right-click "设置静态地址…" entry next to "重命名…" at line 622), and if so mirror the same pattern there (`onSetEndpoint` closure setting `endpointText`/`endpointTarget` instead of `renameText`/`renameTarget`).

- [ ] **Step 5: Build**

```bash
cd /Users/francis/workspc/lattice
GOTOOLCHAIN=auto gomobile bind -prefix Lattice -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
cd apple
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -destination 'platform=macOS' -configuration Debug build 2>&1 | tail -40
```
Expected: `** BUILD SUCCEEDED **`.

- [ ] **Step 6: Manual smoke test**

```bash
killall LatticeMac 2>/dev/null
open ~/Library/Developer/Xcode/DerivedData/LatticeApple-*/Build/Products/Debug/LatticeMac.app
```
Open a peer's detail view (or right-click a peer row), find the new "设置静态地址" action, set a value (e.g. `127.0.0.1:51820` if the container from this session's testing is still up and its port still published), save, and confirm no crash and the peer list still loads afterward (`opError` stays empty).

- [ ] **Step 7: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/LatticeMac/LatticeMacApp.swift apple/LatticeMac/ContentView.swift apple/LatticeMac/PeerDetailView.swift
git commit -s -m "feat(apple): add Set Static Endpoint action to peer detail/context menu"
```

---

## Self-Review

**Spec coverage:**
- Push notification, standalone path → Tasks 2-4.
- Push notification, K8s-via-API path → Task 3 Step 6's explicit instruction to also touch the K8s branches of `UpdatePeer`/`DisablePeer`/`EnablePeer`.
- Push notification, raw-kubectl-bypassing-the-API path → explicitly out of scope per Global Constraints (confirmed with user).
- Endpoint field readable from the API → Task 1.
- Endpoint field editable from the web UI → Task 5.
- Endpoint field editable from the macOS UI → Task 6.

**Placeholder scan:** Task 3 Step 4's test body intentionally references not-yet-real helper names and says so explicitly, instructing the implementer to read the real file and substitute its actual fixtures — this is a deliberate exception, not an oversight, because `peer_test.go`'s existing test setup wasn't read as part of this planning pass (avoided doing so to bound research time) and guessing exact helper names would produce fabricated code that looks complete but silently fails to compile. Every other step has concrete, complete code.

**Type consistency:** `infra.PublishNetmapChanged(ctx, signal, appID string)` and `infra.NetmapChangedSubject(appID string) string` are defined once in Task 2 and used with matching signatures in Tasks 3 and 4. `peerService.signal infra.SignalService` (Task 3) matches the `infra.SignalService` interface extended in Task 2. `NewPeerService`/`NewPeerController`'s new trailing `signal infra.SignalService` parameter is threaded consistently through both call sites in Task 3 Step 2.

**Known gap surfaced, not silently dropped:** direct-CRD-edit-bypassing-the-API in K8s mode (see Global Constraints) — flagged to the user during planning and explicitly deferred, not forgotten.
