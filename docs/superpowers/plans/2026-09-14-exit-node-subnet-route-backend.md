# Exit Node / Subnet Route Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a peer declare "I can route CIDR X" (subnet route, or `0.0.0.0/0` for exit-node) and let other peers individually opt in, so only the opting-in peer's `AllowedIPs` gets expanded — never a network-wide broadcast.

**Architecture:** Two new pieces of state (`Peer.AdvertisedRoutes`, a new `t_peer_route_selection` table) feed `NetmapBuilder.BuildForPeer`, which already computes one netmap per recipient — the only change is that a provider's `AllowedIPs` gets expanded in the recipient's copy only when that recipient has selected it. Three new HTTP endpoints expose declare/select/list-selections on top of the existing standalone `peerService` branch pattern (`if p.netmapBuilder != nil { ... }`).

**Tech Stack:** Go, GORM (SQLite/MySQL via `internal/db/gormstore`, `AutoMigrate` — no manual migration files), Gin, testify.

## Global Constraints

- **`export GOTOOLCHAIN=auto` before any `go build`/`go test` command.** `go.mod` requires `go >= 1.26.0`; the machine's installed `go` binary is 1.25.8. With `GOTOOLCHAIN=auto`, the toolchain auto-downloads and switches to 1.26.0 transparently (confirmed working — `go version` then reports `go1.26.0`). Without it every command fails with `go: go.mod requires go >= 1.26.0 (running go 1.25.8; GOTOOLCHAIN=local)`.
- Standalone mode only this pass — do not touch `internal/agent/controller/peering_controller.go` / `LatticeNetworkPeering` (K8s cross-network peering; explicitly out of scope per the design doc).
- No network-wide broadcast: a provider's advertised route only appears in a consumer's `AllowedIPs` after that specific consumer selects it (`docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md` §三).
- Follow the existing `if p.netmapBuilder != nil { ...standalone... } else { ...K8s... }` branch pattern already used throughout `internal/server/service/peer.go` for every new `PeerService` method.
- New DB fields/tables go through `internal/db/gormstore/migrate.go`'s `AutoMigrate` list — never write a hand-rolled SQL migration.
- Match existing code style exactly: license header on every new `.go` file (copy from any existing file in this repo), table names via `func (T) TableName() string`.

---

### Task 1: `AdvertisedRoutes` field + `PeerRouteSelection` model

**Files:**
- Modify: `internal/server/models/peer.go`
- Create: `internal/server/models/peer_route_selection.go`
- Create: `internal/server/models/peer_route_selection_test.go`
- Modify: `internal/db/gormstore/migrate.go`

**Interfaces:**
- Produces: `models.Peer.AdvertisedRoutes string` (JSON array of CIDR strings, same convention as the existing `Labels` field), `models.PeerRouteSelection{Model, WorkspaceID, ConsumerPeerID, ProviderPeerID string}` with `TableName() string { return "t_peer_route_selection" }`.

- [ ] **Step 1: Write the failing test**

```go
// internal/server/models/peer_route_selection_test.go
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

package models_test

import (
	"testing"

	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPeerRouteSelection_TableNameAndMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Peer{}, &models.PeerRouteSelection{}))

	require.NoError(t, db.Create(&models.Peer{
		Model:       models.Model{ID: "p1"},
		WorkspaceID: "ws1", Name: "gw", AdvertisedRoutes: `["0.0.0.0/0"]`,
	}).Error)

	var got models.Peer
	require.NoError(t, db.First(&got, "id = ?", "p1").Error)
	require.Equal(t, `["0.0.0.0/0"]`, got.AdvertisedRoutes)

	sel := &models.PeerRouteSelection{
		Model:          models.Model{ID: "sel1"},
		WorkspaceID:    "ws1",
		ConsumerPeerID: "consumer1",
		ProviderPeerID: "p1",
	}
	require.NoError(t, db.Create(sel).Error)

	var gotSel models.PeerRouteSelection
	require.NoError(t, db.First(&gotSel, "id = ?", "sel1").Error)
	require.Equal(t, "consumer1", gotSel.ConsumerPeerID)
	require.Equal(t, "t_peer_route_selection", models.PeerRouteSelection{}.TableName())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/models/... -run TestPeerRouteSelection_TableNameAndMigrate -v`
Expected: FAIL — `undefined: models.PeerRouteSelection` (and `AdvertisedRoutes` unknown field on `models.Peer`)

- [ ] **Step 3: Add `AdvertisedRoutes` to `Peer`**

In `internal/server/models/peer.go`, add this field to the `Peer` struct (next to the existing `Labels` field, same pattern):

```go
	// AdvertisedRoutes is a JSON array of CIDRs this peer offers to route
	// for other peers, e.g. ["0.0.0.0/0"] for exit-node, ["192.168.1.0/24"]
	// for a subnet route. Empty/absent means this peer offers nothing.
	// Consumers only get this expanded into their own AllowedIPs after
	// opting in via PeerRouteSelection — see netmap_builder.go.
	AdvertisedRoutes string `gorm:"type:text" json:"advertised_routes,omitempty"`
```

- [ ] **Step 4: Create the `PeerRouteSelection` model**

```go
// internal/server/models/peer_route_selection.go
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

package models

// PeerRouteSelection records that ConsumerPeerID has opted to accept
// ProviderPeerID's AdvertisedRoutes. One row per (consumer, provider) pair;
// a consumer may select more than one provider (e.g. a subnet route from
// one peer and an exit node from another).
type PeerRouteSelection struct {
	Model

	WorkspaceID    string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ConsumerPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ProviderPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
}

func (PeerRouteSelection) TableName() string { return "t_peer_route_selection" }
```

- [ ] **Step 5: Register it in `AutoMigrate`**

In `internal/db/gormstore/migrate.go`, add `&models.PeerRouteSelection{}` to the `db.AutoMigrate(...)` call (right after `&models.Peer{}`):

```go
		&models.Peer{},
		&models.PeerRouteSelection{},
		&models.EnrollmentToken{},
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/server/models/... -run TestPeerRouteSelection_TableNameAndMigrate -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/server/models/peer.go internal/server/models/peer_route_selection.go \
        internal/server/models/peer_route_selection_test.go internal/db/gormstore/migrate.go
git commit -s -m "feat(standalone): add AdvertisedRoutes field and PeerRouteSelection model"
```

---

### Task 2: `PeerRouteSelectionRepository` (store layer)

**Files:**
- Modify: `internal/agent/store/store.go`
- Create: `internal/db/gormstore/peer_route_selection.go`
- Create: `internal/db/gormstore/peer_route_selection_test.go`
- Modify: `internal/db/gormstore/store.go`

**Interfaces:**
- Consumes: `models.PeerRouteSelection` (Task 1)
- Produces: `store.PeerRouteSelectionRepository` interface with `Create(ctx, *models.PeerRouteSelection) error`, `Delete(ctx, workspaceID, consumerPeerID, providerPeerID string) error`, `ListProviderIDsForConsumer(ctx, workspaceID, consumerPeerID string) ([]string, error)`; `store.Store.RouteSelections() PeerRouteSelectionRepository`.

- [ ] **Step 1: Write the failing test**

```go
// internal/db/gormstore/peer_route_selection_test.go
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

package gormstore_test

import (
	"context"
	"testing"

	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPeerRouteSelectionRepo_CreateListDelete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel1"}, WorkspaceID: "ws1",
		ConsumerPeerID: "mac", ProviderPeerID: "gw1",
	}))
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel2"}, WorkspaceID: "ws1",
		ConsumerPeerID: "mac", ProviderPeerID: "gw2",
	}))

	ids, err := st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"gw1", "gw2"}, ids)

	require.NoError(t, st.RouteSelections().Delete(ctx, "ws1", "mac", "gw1"))
	ids, err = st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw2"}, ids)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/gormstore/... -run TestPeerRouteSelectionRepo_CreateListDelete -v`
Expected: FAIL — `st.RouteSelections undefined`

- [ ] **Step 3: Add the interface to `store.Store`**

In `internal/agent/store/store.go`, add this method to the `Store` interface (next to `Peers() PeerRepository`):

```go
	Peers() PeerRepository
	RouteSelections() PeerRouteSelectionRepository
```

Add the interface definition (next to `PeerRepository`'s definition):

```go
// PeerRouteSelectionRepository manages t_peer_route_selection: which peer
// (consumer) has opted to accept another peer's (provider) advertised routes.
type PeerRouteSelectionRepository interface {
	Create(ctx context.Context, m *models.PeerRouteSelection) error
	// Delete removes the (consumer, provider) selection row, if present.
	// Not finding one is not an error — deselecting an unselected provider
	// is a no-op.
	Delete(ctx context.Context, workspaceID, consumerPeerID, providerPeerID string) error
	// ListProviderIDsForConsumer returns the provider peer IDs consumerPeerID
	// has opted into, for expanding AllowedIPs in that consumer's netmap.
	ListProviderIDsForConsumer(ctx context.Context, workspaceID, consumerPeerID string) ([]string, error)
}
```

- [ ] **Step 4: Implement the gormstore repo**

```go
// internal/db/gormstore/peer_route_selection.go
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

package gormstore

import (
	"context"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"gorm.io/gorm"
)

type peerRouteSelectionRepo struct {
	db *gorm.DB
}

func newPeerRouteSelectionRepo(db *gorm.DB) *peerRouteSelectionRepo {
	return &peerRouteSelectionRepo{db: db}
}

func (r *peerRouteSelectionRepo) Create(ctx context.Context, m *models.PeerRouteSelection) error {
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *peerRouteSelectionRepo) Delete(ctx context.Context, workspaceID, consumerPeerID, providerPeerID string) error {
	return r.db.WithContext(ctx).
		Where("workspace_id = ? AND consumer_peer_id = ? AND provider_peer_id = ?", workspaceID, consumerPeerID, providerPeerID).
		Delete(&models.PeerRouteSelection{}).Error
}

func (r *peerRouteSelectionRepo) ListProviderIDsForConsumer(ctx context.Context, workspaceID, consumerPeerID string) ([]string, error) {
	var rows []models.PeerRouteSelection
	if err := r.db.WithContext(ctx).
		Where("workspace_id = ? AND consumer_peer_id = ?", workspaceID, consumerPeerID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ProviderPeerID
	}
	return ids, nil
}

var _ store.PeerRouteSelectionRepository = (*peerRouteSelectionRepo)(nil)
```

- [ ] **Step 5: Wire it into `GormStore`**

In `internal/db/gormstore/store.go`:

1. Add a field to the `GormStore` struct: `routeSelections store.PeerRouteSelectionRepository` (next to `peers store.PeerRepository`)
2. In `newStore(db)`, add: `routeSelections: newPeerRouteSelectionRepo(db),` (next to `peers: newPeerRepo(db),`)
3. Add the accessor method (next to `func (s *GormStore) Peers() ...`):

```go
func (s *GormStore) RouteSelections() store.PeerRouteSelectionRepository { return s.routeSelections }
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/db/gormstore/... -run TestPeerRouteSelectionRepo_CreateListDelete -v`
Expected: PASS

- [ ] **Step 7: Run the full package tests to check nothing else broke**

Run: `go build ./... && go test ./internal/agent/store/... ./internal/db/gormstore/... -v`
Expected: PASS (any other type implementing `store.Store` — check for a mock in tests — must also gain a `RouteSelections()` method; if `go build` fails on a missing method, add the trivial accessor there too before proceeding)

- [ ] **Step 8: Commit**

```bash
git add internal/agent/store/store.go internal/db/gormstore/store.go \
        internal/db/gormstore/peer_route_selection.go internal/db/gormstore/peer_route_selection_test.go
git commit -s -m "feat(standalone): add PeerRouteSelectionRepository"
```

---

### Task 3: Per-recipient `AllowedIPs` expansion in `NetmapBuilder`

**Files:**
- Modify: `internal/server/reconcilers/netmap_builder.go`
- Modify: `internal/server/reconcilers/netmap_builder_test.go`
- Modify: `internal/server/service/peer.go:343` (the one non-test `NewNetmapBuilder` call site)

**Interfaces:**
- Consumes: `store.PeerRouteSelectionRepository.ListProviderIDsForConsumer` (Task 2), `models.Peer.AdvertisedRoutes` (Task 1)
- Produces: `NewNetmapBuilder(peers, policies, identities, routeSelections store.PeerRouteSelectionRepository) *NetmapBuilder` (signature changes — every call site must be updated in this task)

- [ ] **Step 1: Write the failing test**

Add this test to `internal/server/reconcilers/netmap_builder_test.go` (same file, same `newTestStore(t)` helper the existing tests use):

```go
func TestNetmapBuilder_ExpandsAllowedIPsOnlyForConsumerThatSelected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer1"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Token: "tk-mac", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer2"}, WorkspaceID: "ws1", Name: "other",
		AppID: "other-app", Token: "tk-other", Address: "10.96.0.3", PublicKey: "kother",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider1"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Token: "tk-gw", Address: "10.96.0.4", PublicKey: "kgw",
		AdvertisedRoutes: `["192.168.1.0/24"]`,
	}))
	// Only "mac" has opted into gw's route.
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel1"}, WorkspaceID: "ws1",
		ConsumerPeerID: "consumer1", ProviderPeerID: "provider1",
	}))

	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())

	macPeer, err := st.Peers().GetByID(ctx, "consumer1")
	require.NoError(t, err)
	msg, err := builder.BuildForPeer(ctx, macPeer)
	require.NoError(t, err)
	var gwForMac *infra.Peer
	for _, p := range msg.Network.Peers {
		if p.Name == "gw" {
			gwForMac = p
		}
	}
	require.NotNil(t, gwForMac)
	assert.Equal(t, "10.96.0.4/32,192.168.1.0/24", gwForMac.AllowedIPs)

	otherPeer, err := st.Peers().GetByID(ctx, "consumer2")
	require.NoError(t, err)
	msg2, err := builder.BuildForPeer(ctx, otherPeer)
	require.NoError(t, err)
	var gwForOther *infra.Peer
	for _, p := range msg2.Network.Peers {
		if p.Name == "gw" {
			gwForOther = p
		}
	}
	require.NotNil(t, gwForOther)
	assert.Equal(t, "10.96.0.4/32", gwForOther.AllowedIPs)
}

func TestNetmapBuilder_SelectedProviderClearingRoutesFallsBackToSlash32(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer1"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Token: "tk-mac", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider1"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Token: "tk-gw", Address: "10.96.0.4", PublicKey: "kgw",
		AdvertisedRoutes: `["192.168.1.0/24"]`,
	}))
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel1"}, WorkspaceID: "ws1",
		ConsumerPeerID: "consumer1", ProviderPeerID: "provider1",
	}))

	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	macPeer, err := st.Peers().GetByID(ctx, "consumer1")
	require.NoError(t, err)

	// Before: selection is still in effect, route is expanded.
	msg, err := builder.BuildForPeer(ctx, macPeer)
	require.NoError(t, err)
	allowedIPsFor := func(msg *infra.Message, name string) string {
		for _, p := range msg.Network.Peers {
			if p.Name == name {
				return p.AllowedIPs
			}
		}
		return ""
	}
	assert.Equal(t, "10.96.0.4/32,192.168.1.0/24", allowedIPsFor(msg, "gw"))

	// Provider clears its declaration (e.g. turned off "advertise subnet
	// route" in its own settings) without the consumer's selection being
	// touched at all — the selection row is left in place on purpose.
	gwPeer, err := st.Peers().GetByID(ctx, "provider1")
	require.NoError(t, err)
	gwPeer.AdvertisedRoutes = ""
	require.NoError(t, st.Peers().Update(ctx, gwPeer))

	// After: same selection still exists, but nothing to expand — falls
	// back to the plain /32 automatically, no selection-row cleanup needed.
	msg2, err := builder.BuildForPeer(ctx, macPeer)
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.4/32", allowedIPsFor(msg2, "gw"))
}
```

Also update the two existing `reconcilers.NewNetmapBuilder(...)` calls already in this test file (the ones from `TestNetmapBuilder_BuildsFullMessage` and any other existing test) to pass `st.RouteSelections()` as the fourth argument — the signature change in Step 3 below is not backwards compatible.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/reconcilers/... -run TestNetmapBuilder_ExpandsAllowedIPsOnlyForConsumerThatSelected -v`
Expected: FAIL — `not enough arguments in call to reconcilers.NewNetmapBuilder`

- [ ] **Step 3: Change `NetmapBuilder` to take the new repo and expand per-recipient**

In `internal/server/reconcilers/netmap_builder.go`, change the struct and constructor:

```go
type NetmapBuilder struct {
	peers           store.PeerRepository
	policies        store.PolicyRepository
	identities      store.PeerIdentityRepository
	routeSelections store.PeerRouteSelectionRepository
	logger          logr.Logger
}

// NewNetmapBuilder returns a builder over the standalone stores.
func NewNetmapBuilder(peers store.PeerRepository, policies store.PolicyRepository, identities store.PeerIdentityRepository, routeSelections store.PeerRouteSelectionRepository) *NetmapBuilder {
	return &NetmapBuilder{
		peers:           peers,
		policies:        policies,
		identities:      identities,
		routeSelections: routeSelections,
		logger:          logr.Discard(),
	}
}
```

Then change `BuildForPeer` (the loop currently at line ~118-127) to expand `AllowedIPs` for selected providers. Replace:

```go
	computedPeers := make([]*infra.Peer, 0, len(rows))
	for _, row := range rows {
		if row.Address == "" {
			continue // still enrolling; not part of the mesh yet
		}
		p := dbToInfraPeer(row)
		network.Peers = append(network.Peers, p)
		if row.ID != peer.ID {
			computedPeers = append(computedPeers, p)
		}
	}
```

with:

```go
	selectedProviderIDs, err := b.routeSelections.ListProviderIDsForConsumer(ctx, peer.WorkspaceID, peer.ID)
	if err != nil {
		return nil, fmt.Errorf("list route selections: %w", err)
	}
	selected := make(map[string]struct{}, len(selectedProviderIDs))
	for _, id := range selectedProviderIDs {
		selected[id] = struct{}{}
	}

	computedPeers := make([]*infra.Peer, 0, len(rows))
	for _, row := range rows {
		if row.Address == "" {
			continue // still enrolling; not part of the mesh yet
		}
		p := dbToInfraPeer(row)
		if _, ok := selected[row.ID]; ok {
			if extra := parseAdvertisedRoutes(row.AdvertisedRoutes); len(extra) > 0 {
				p.AllowedIPs = strings.Join(append([]string{p.AllowedIPs}, extra...), ",")
			}
		}
		network.Peers = append(network.Peers, p)
		if row.ID != peer.ID {
			computedPeers = append(computedPeers, p)
		}
	}
```

Add the helper function (near `dbToInfraPeer`):

```go
// parseAdvertisedRoutes decodes the AdvertisedRoutes JSON-array column.
// Malformed or empty input yields no routes rather than an error — a peer
// that never declared anything (or has a stale/corrupt value) should just
// offer nothing, not break netmap building for everyone who selected it.
func parseAdvertisedRoutes(raw string) []string {
	if raw == "" {
		return nil
	}
	var routes []string
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil
	}
	return routes
}
```

Add `"strings"` to the existing `import` block.

- [ ] **Step 4: Update the one production call site**

In `internal/server/service/peer.go`, line 343 currently reads:

```go
		svc.netmapBuilder = reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities())
```

Change to:

```go
		svc.netmapBuilder = reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/reconcilers/... -v`
Expected: PASS (all tests in the package, including the pre-existing ones you updated in Step 1)

- [ ] **Step 6: Run the whole server test suite to catch any other call site**

Run: `go build ./... && go test ./internal/server/... -v`
Expected: PASS — `go build ./...` is the important one here: it will fail loudly with an exact file:line if any other `NewNetmapBuilder` call was missed

- [ ] **Step 7: Commit**

```bash
git add internal/server/reconcilers/netmap_builder.go internal/server/reconcilers/netmap_builder_test.go \
        internal/server/service/peer.go
git commit -s -m "feat(standalone): expand AllowedIPs per-recipient for selected route providers"
```

---

### Task 4: `PeerService` methods — declare, select, list selections

**Files:**
- Modify: `internal/server/service/peer.go`
- Create: `internal/server/service/peer_route_test.go`

**Interfaces:**
- Consumes: `standalonePeerByName` (existing helper, `internal/server/service/peer.go:539`), `store.PeerRouteSelectionRepository` (Task 2)
- Produces: `PeerService.SetAdvertisedRoutes(ctx, name string, routes []string) error`, `PeerService.SetRouteSelection(ctx, consumerName, providerName string, selected bool) error`, `PeerService.ListRouteSelections(ctx, consumerName string) ([]string, error)` (returns provider **names**, not IDs)

- [ ] **Step 1: Write the failing test**

```go
// internal/server/service/peer_route_test.go
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

package service_test

import (
	"context"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerService_AdvertisedRoutesAndSelection(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "gw", AppID: "gw-app", Token: "enr-test-token"})
	require.NoError(t, err)
	_, err = svc.Register(ctx, &dto.PeerDto{Name: "mac", AppID: "mac-app", Token: "enr-test-token"})
	require.NoError(t, err)

	wsCtx := context.WithValue(ctx, infra.WorkspaceKey, "ws1")

	require.NoError(t, svc.SetAdvertisedRoutes(wsCtx, "gw", []string{"192.168.1.0/24"}))
	require.NoError(t, svc.SetRouteSelection(wsCtx, "mac", "gw", true))

	selected, err := svc.ListRouteSelections(wsCtx, "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw"}, selected)

	// Deselect and confirm it's gone.
	require.NoError(t, svc.SetRouteSelection(wsCtx, "mac", "gw", false))
	selected, err = svc.ListRouteSelections(wsCtx, "mac")
	require.NoError(t, err)
	assert.Empty(t, selected)

	// A peer cannot select itself.
	err = svc.SetRouteSelection(wsCtx, "mac", "mac", true)
	require.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/service/... -run TestPeerService_AdvertisedRoutesAndSelection -v`
Expected: FAIL — `svc.SetAdvertisedRoutes undefined`

- [ ] **Step 3: Add the three methods to the `PeerService` interface**

In `internal/server/service/peer.go`, add to the `PeerService` interface (next to `DeletePeer`):

```go
	DeletePeer(ctx context.Context, namespace, name string) error
	SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error
	SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error
	ListRouteSelections(ctx context.Context, consumerName string) ([]string, error)
```

- [ ] **Step 4: Implement the three methods**

Add near `setPeerDisabledStandalone` (which shows the exact `standalonePeerByName` + `store.Peers().Update` pattern to follow):

```go
// SetAdvertisedRoutes declares (or clears, if routes is empty) the CIDRs
// this peer offers to route for other peers. Standalone only for now —
// K8s mode routes through LatticeNetworkPeering instead (out of scope,
// see docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md).
func (p *peerService) SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error {
	if p.netmapBuilder == nil {
		return stderrors.New("advertised routes are not supported in K8s mode yet")
	}
	peer, err := p.standalonePeerByName(ctx, name)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		peer.AdvertisedRoutes = ""
	} else {
		blob, err := json.Marshal(routes)
		if err != nil {
			return fmt.Errorf("marshal advertised routes: %w", err)
		}
		peer.AdvertisedRoutes = string(blob)
	}
	return p.store.Peers().Update(ctx, peer)
}

// SetRouteSelection opts consumerName in (selected=true) or out
// (selected=false) of providerName's advertised routes. A peer cannot
// select itself.
func (p *peerService) SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error {
	if p.netmapBuilder == nil {
		return stderrors.New("route selection is not supported in K8s mode yet")
	}
	if consumerName == providerName {
		return stderrors.New("a peer cannot select its own advertised routes")
	}
	consumer, err := p.standalonePeerByName(ctx, consumerName)
	if err != nil {
		return err
	}
	provider, err := p.standalonePeerByName(ctx, providerName)
	if err != nil {
		return err
	}
	if !selected {
		return p.store.RouteSelections().Delete(ctx, consumer.WorkspaceID, consumer.ID, provider.ID)
	}
	return p.store.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		WorkspaceID:    consumer.WorkspaceID,
		ConsumerPeerID: consumer.ID,
		ProviderPeerID: provider.ID,
	})
}

// ListRouteSelections returns the names (not IDs) of providers
// consumerName has currently opted into.
func (p *peerService) ListRouteSelections(ctx context.Context, consumerName string) ([]string, error) {
	if p.netmapBuilder == nil {
		return nil, stderrors.New("route selection is not supported in K8s mode yet")
	}
	consumer, err := p.standalonePeerByName(ctx, consumerName)
	if err != nil {
		return nil, err
	}
	providerIDs, err := p.store.RouteSelections().ListProviderIDsForConsumer(ctx, consumer.WorkspaceID, consumer.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(providerIDs))
	for _, id := range providerIDs {
		provider, err := p.store.Peers().GetByID(ctx, id)
		if err != nil {
			continue // provider was deleted since selecting; skip rather than fail the whole list
		}
		names = append(names, provider.Name)
	}
	return names, nil
}
```

No manual ID generation needed: `models.Model.BeforeCreate` (`internal/server/models/model.go`) already auto-fills `ID` with a fresh UUID whenever a row is created with `ID == ""` — the same hook every other standalone `Create` call in this codebase relies on.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/service/... -run TestPeerService_AdvertisedRoutesAndSelection -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/server/service/peer.go internal/server/service/peer_route_test.go
git commit -s -m "feat(standalone): add SetAdvertisedRoutes/SetRouteSelection/ListRouteSelections"
```

---

### Task 5: `PeerController` pass-through

**Files:**
- Modify: `internal/server/controller/peer.go`

**Interfaces:**
- Consumes: `PeerService.SetAdvertisedRoutes` / `SetRouteSelection` / `ListRouteSelections` (Task 4)
- Produces: same three methods on `PeerController`

- [ ] **Step 1: Add to the `PeerController` interface**

In `internal/server/controller/peer.go`, add to the interface (next to `DeletePeer`):

```go
	DeletePeer(ctx context.Context, namespace, name string) error
	SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error
	SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error
	ListRouteSelections(ctx context.Context, consumerName string) ([]string, error)
```

- [ ] **Step 2: Implement the pass-throughs**

Add next to `DeletePeer`'s implementation:

```go
func (p *peerController) SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error {
	return p.peerService.SetAdvertisedRoutes(ctx, name, routes)
}

func (p *peerController) SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error {
	return p.peerService.SetRouteSelection(ctx, consumerName, providerName, selected)
}

func (p *peerController) ListRouteSelections(ctx context.Context, consumerName string) ([]string, error) {
	return p.peerService.ListRouteSelections(ctx, consumerName)
}
```

- [ ] **Step 3: Verify the package builds**

Run: `go build ./internal/server/controller/...`
Expected: no output (success) — this task has no new logic of its own to unit-test; it's a pure pass-through, verified by Task 6's integration test exercising it end to end

- [ ] **Step 4: Commit**

```bash
git add internal/server/controller/peer.go
git commit -s -m "feat(standalone): pass advertised-routes/route-selection through PeerController"
```

---

### Task 6: HTTP API — declare, select, list

**Files:**
- Modify: `internal/server/server/api.go`
- Create: `internal/server/dto/route_selection.go`
- Create: `internal/server/server/route_selection_test.go`

**Interfaces:**
- Consumes: `PeerController.SetAdvertisedRoutes` / `SetRouteSelection` / `ListRouteSelections` (Task 5)
- Produces: `POST /api/v1/peers/:name/advertised-routes`, `POST /api/v1/peers/:name/route-selection`, `GET /api/v1/peers/:name/route-selection`

- [ ] **Step 1: Add the two request DTOs**

```go
// internal/server/dto/route_selection.go
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

package dto

// AdvertisedRoutesDto is the body of POST /peers/:name/advertised-routes.
// An empty Routes slice clears the peer's declaration.
type AdvertisedRoutesDto struct {
	Routes []string `json:"routes"`
}

// RouteSelectionDto is the body of POST /peers/:name/route-selection.
// :name in the URL is the consumer; Provider is who it selects.
type RouteSelectionDto struct {
	Provider string `json:"provider"`
	Selected bool   `json:"selected"`
}
```

- [ ] **Step 2: Write the failing test**

This package already has one internal (white-box) test file, `internal/server/server/demo_test.go`, which constructs `&Server{cfg: cfg}` directly and drives a handler via `gin.CreateTestContext` instead of going through real routing/middleware — follow that exact pattern (it exists precisely so handler-level tests don't need a full HTTP server + auth stack).

```go
// internal/server/server/route_selection_test.go
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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/controller"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// noopVerifier behaves like Community: no license, no node-limit enforcement.
type noopVerifier struct{}

func (noopVerifier) Verify() (*license.License, license.Status, error) {
	return nil, license.StatusNotFound, nil
}
func (noopVerifier) HasFeature(string) bool { return false }

// newRouteSelectionTestServer wires a real PeerController (nil K8s client
// = standalone mode, same trigger internal/server/service/peer.go uses
// everywhere else) over a fresh in-memory SQLite store, seeded with two
// peers in workspace "ws1".
func newRouteSelectionTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "gw-id"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Address: "10.96.0.4", PublicKey: "kgw",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "mac-id"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	return &Server{peerController: controller.NewPeerController(nil, st, nil, noopVerifier{})}
}

// testContext builds a *gin.Context carrying the workspace-scoped request
// the WorkspaceAuthMiddleware would normally have set up, plus the :name
// URL param — everything the handler itself reads.
func testContext(method, name string, body any) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *bytes.Reader
	if body != nil {
		blob, _ := json.Marshal(body)
		reader = bytes.NewReader(blob)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/", reader)
	req = req.WithContext(context.WithValue(req.Context(), infra.WorkspaceKey, "ws1"))
	c.Request = req
	c.Params = gin.Params{{Key: "name", Value: name}}
	return c, w
}

func TestRouteSelectionHandlers_DeclareSelectList(t *testing.T) {
	s := newRouteSelectionTestServer(t)

	c, w := testContext(http.MethodPost, "gw", map[string]any{"routes": []string{"192.168.1.0/24"}})
	s.setAdvertisedRoutes(c)
	require.Equal(t, http.StatusOK, w.Code)

	c, w = testContext(http.MethodPost, "mac", map[string]any{"provider": "gw", "selected": true})
	s.setRouteSelection(c)
	require.Equal(t, http.StatusOK, w.Code)

	c, w = testContext(http.MethodGet, "mac", nil)
	s.listRouteSelections(c)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Data []string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, []string{"gw"}, body.Data)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server/server/... -run TestRouteSelectionHandlers_DeclareSelectList -v`
Expected: FAIL — `s.setAdvertisedRoutes undefined (type *Server has no field or method setAdvertisedRoutes)`

- [ ] **Step 4: Add the three handlers and register the routes**

In `internal/server/server/api.go`, add to the `peerApi` group (next to the existing `peerApi.DELETE("/:name", s.deletePeerHandler)`):

```go
		peerApi.POST("/:name/advertised-routes", s.setAdvertisedRoutes)
		peerApi.POST("/:name/route-selection", s.setRouteSelection)
		peerApi.GET("/:name/route-selection", s.listRouteSelections)
```

Add the three handlers (next to `deletePeerHandler`):

```go
func (s *Server) setAdvertisedRoutes(c *gin.Context) {
	name := c.Param("name")
	var req dto.AdvertisedRoutesDto
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "invalid params")
		return
	}
	if err := s.peerController.SetAdvertisedRoutes(c.Request.Context(), name, req.Routes); err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, nil)
}

func (s *Server) setRouteSelection(c *gin.Context) {
	consumerName := c.Param("name")
	var req dto.RouteSelectionDto
	if err := c.ShouldBindJSON(&req); err != nil {
		resp.BadRequest(c, "invalid params")
		return
	}
	if err := s.peerController.SetRouteSelection(c.Request.Context(), consumerName, req.Provider, req.Selected); err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, nil)
}

func (s *Server) listRouteSelections(c *gin.Context) {
	consumerName := c.Param("name")
	names, err := s.peerController.ListRouteSelections(c.Request.Context(), consumerName)
	if err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, names)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server/server/... -run TestRouteSelectionHandlers_DeclareSelectList -v`
Expected: PASS

- [ ] **Step 6: Run the full test suite**

Run: `go build ./... && go test ./... 2>&1 | tail -60`
Expected: PASS across the repo

- [ ] **Step 7: Verify against the real running `latticed --standalone`**

This machine already has one running (check with `ps aux | grep latticed`, config dir `.standalone-cfg`). Rebuild and restart it, then hit the new endpoints for real — this is the final check for this plan, stronger evidence than the unit tests alone since it exercises the actual SQLite file and actual gin routing/middleware stack together:

```bash
go build -o bin/latticed ./cmd/latticed
kill $(pgrep -f "bin/latticed --standalone") 2>/dev/null
./bin/latticed --standalone --config-dir .standalone-cfg &
sleep 1
TOKEN=$(defaults read io.lattice.mac lattice.authToken)
WS=$(defaults read io.lattice.mac lattice.workspaceId)
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WS" \
  -H "Content-Type: application/json" -d '{"routes":["192.168.1.0/24"]}' \
  http://127.0.0.1:8080/api/v1/peers/node-a/advertised-routes
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WS" \
  -H "Content-Type: application/json" -d '{"provider":"node-a","selected":true}' \
  http://127.0.0.1:8080/api/v1/peers/node-b/route-selection
curl -s -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WS" \
  http://127.0.0.1:8080/api/v1/peers/node-b/route-selection
```

Expected: all three return `{"code":200,...}`; the last one's `data` is `["node-a"]`. (`node-a`/`node-b` are the same two placeholder peers seen earlier this session — real peer names in your workspace may differ; use `GET /api/v1/peers/list` first if unsure which names exist.)

- [ ] **Step 8: Commit**

```bash
git add internal/server/dto/route_selection.go internal/server/server/api.go \
        internal/server/server/route_selection_test.go
git commit -s -m "feat(standalone): expose advertised-routes/route-selection HTTP API"
```

---

## What this plan does not cover

The macOS client side (`PacketTunnelProvider.swift` reading these routes into `NEIPv4Route`, `NetworkSettingsView` wiring to real data) needs a new `Engine` → Swift delegate method and a rebuild of `apple/Frameworks/MacOS/LatticeCore.xcframework` via `gomobile bind`. `gomobile` is installed on this machine (`/Users/francis/go/bin/gomobile`) but currently fails with `go.mod requires go >= 1.26.0 (running go 1.25.8)` — the same toolchain mismatch that blocks `make lint`. That's a prerequisite to fix (upgrade the local Go toolchain) before a client-side plan can include a verified build step instead of a guessed one. Write that plan as a separate pass once the toolchain is sorted.
