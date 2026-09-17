# Peer Enrollment Approval + Client-Side Key Generation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement ADR-0003 — agents generate their WireGuard keypair locally (only the public key is registered), and peers gain an opt-in per-workspace approval gate (`pending → approved / revoked`) so a leaked enrollment token yields a visible pending peer instead of a silent network join.

**Architecture:** Two phases sharing the registration path. Phase A (Tasks 1–3) moves key generation client-side with a server compat window. Phase B (Tasks 4–10) adds the approval state machine on `t_peer`, gates the netmap builders on it, defers address allocation until approval, and exposes approve/revoke via service → controller → HTTP route → CLI. The agent needs almost no change: a pending peer simply receives an address-less netmap and keeps its existing poll/heartbeat loop.

**Tech Stack:** Go 1.26, gin, gorm (glebarez sqlite), cobra/viper, testify.

## Global Constraints

- Conventional commits with scope, `git commit -s`, no `Co-Authored-By`, push to `origin new_dev` immediately after every commit (CLAUDE.md Git Commit Rules).
- `make lint` is currently broken (bin/golangci-lint built with go1.25 < go.mod 1.26). Run `go vet ./...` on touched packages instead; note it in the final summary.
- Run tests with `-race`.
- Backward compatibility is non-negotiable: approval is opt-in per workspace (`RequirePeerApproval` default false → every existing peer row defaults to `approved`); legacy agents without a client key keep the server-side key path.
- Never log private key material.
- **Out of scope (follow-up plans):** K8s CRD path approval (LatticePeer status condition + reconciler gating), frontend dashboard badges/actions, audit-log wiring for approver identity, `infra.Peer.PrivateKey` wire-field removal (Release N+1 per ADR).

## File Structure

```
internal/agent/device_key.go            # NEW  — agent-side key generation/persistence
internal/agent/device_key_test.go       # NEW
internal/agent/node.go                  # MOD  — use device key, send pubkey in Register
internal/server/service/peer.go         # MOD  — registerStandalone key switch, pending logic, SetPeerApproval
internal/server/service/peer_register_test.go  # MOD — service tests
internal/server/models/peer.go          # MOD  — ApprovalStatus/ApprovedBy/ApprovedAt
internal/server/models/workspace.go     # MOD  — RequirePeerApproval
internal/server/reconcilers/netmap_builder.go   # MOD — approval gating + pendingMessage
internal/server/reconcilers/netmap_builder_test.go # MOD
internal/agent/infra/message.go         # MOD  — infra.Peer.ApprovalStatus wire field
internal/server/controller/<peer controller file>  # MOD — SetPeerApproval delegate
internal/server/server/api.go           # MOD  — PUT /:name/approval route + handler
internal/agent/message_handler.go       # MOD  — "awaiting approval" log
internal/agent/client/admin.go          # MOD  — SetPeerApproval client method
cmd/lattice/cmd/peer/peer.go            # MOD  — approve/reject subcommands
```

---

### Task 1: Agent device key generation + persistence

**Files:**
- Create: `internal/agent/device_key.go`, `internal/agent/device_key_test.go`

**Interfaces:**
- Produces: `ensureDeviceKey() (wgtypes.Key, error)` — returns the persisted device key, generating it on first run; key file at `filepath.Dir(config.GetConfigFilePath())/device.key` with 0600 perms.

- [ ] **Step 1: Write the failing test** — `internal/agent/device_key_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestEnsureDeviceKeyAt_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")

	k1, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms = %v, want 0600", info.Mode().Perm())
	}

	// Second call must return the SAME key (persisted, not regenerated).
	k2, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Fatal("device key must be stable across calls")
	}
}

func TestEnsureDeviceKeyAt_RegeneratesOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if k == (wgtypes.Key{}) {
		t.Fatal("expected a regenerated key")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/agent/ -run TestEnsureDeviceKeyAt` → FAIL: `undefined: ensureDeviceKeyAt`.

- [ ] **Step 3: Implement** — `internal/agent/device_key.go`:

```go
// Copyright 2026 The Lattice Authors, Inc.
// Licensed under the Apache License, Version 2.0 (see project header).

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/pkg/utils"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ensureDeviceKey returns this device's WireGuard private key, generating
// and persisting one on first run (ADR-0003: keys are generated on the
// agent and never leave it — only the derived public key is registered).
func ensureDeviceKey() (wgtypes.Key, error) {
	return ensureDeviceKeyAt(deviceKeyPath())
}

func deviceKeyPath() string {
	return filepath.Join(filepath.Dir(config.GetConfigFilePath()), "device.key")
}

func ensureDeviceKeyAt(path string) (wgtypes.Key, error) {
	if data, err := os.ReadFile(path); err == nil {
		if key, perr := utils.ParseKey(strings.TrimSpace(string(data))); perr == nil {
			return key, nil
		}
		// Corrupt file: fall through and regenerate.
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("generate device key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return wgtypes.Key{}, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(key.String()+"\n"), 0o600); err != nil {
		return wgtypes.Key{}, fmt.Errorf("persist device key: %w", err)
	}
	return key, nil
}
```

- [ ] **Step 4: Run tests** — `go test -race ./internal/agent/ -run TestEnsureDeviceKeyAt` → PASS.

- [ ] **Step 5: Commit + push**

```bash
git add internal/agent/device_key.go internal/agent/device_key_test.go
git commit -s -m "feat(agent): local device key generation with 0600 persistence"
git push origin new_dev
```

---

### Task 2: Node uses the device key for registration

**Files:**
- Modify: `internal/agent/node.go` (~line 320-350: Register call + key parse)

**Interfaces:**
- Consumes: `ensureDeviceKey()` (Task 1).
- Produces: `NewNode` registers with the device public key and prefers the device private key over any server-returned one. Sandbox path (`cfg.CurrentPeer != nil`) unchanged.

- [ ] **Step 1: Modify NewNode.** Replace the register + key-parse block:

```go
	if cfg.CurrentPeer != nil {
		node.current = cfg.CurrentPeer
	} else {
		pub := ""
		var deviceKey *wgtypes.Key
		if dk, derr := ensureDeviceKey(); derr == nil {
			deviceKey = &dk
			pub = dk.PublicKey().String()
		} else {
			log.GetLogger("node").Warn("device key generation failed; falling back to server-side key", "err", derr)
		}
		node.current, err = node.ctrClient.Register(ctx, cfg.Token, node.Name, pub)
		if err != nil {
			return nil, err
		}
		if deviceKey != nil {
			// ADR-0003: the key is generated here and never leaves the
			// device; ignore anything the server still sends back.
			if node.current.PrivateKey != "" {
				log.GetLogger("node").Warn("server returned a private key; ignoring it (client-side key generation active)")
			}
			privateKey = *deviceKey
		} else {
			privateKey, err = utils.ParseKey(node.current.PrivateKey)
			if err != nil {
				return nil, err
			}
		}
	}
```

Then DELETE the now-dead original lines below the block:

```go
	privateKey, err = utils.ParseKey(node.current.PrivateKey)
	if err != nil {
		return nil, err
	}
```

(`wgtypes` is already imported in node.go; `utils` remains used by the sandbox path.)

- [ ] **Step 2: Build + full agent tests** — `go build ./... && go test -race ./internal/agent/...` → PASS (NewNode itself is integration-heavy; behavior is covered end-to-end by Task 3's server tests + manual `lattice up`).

- [ ] **Step 3: Vet** — `go vet ./internal/agent/...` → clean.

- [ ] **Step 4: Commit + push**

```bash
git add internal/agent/node.go
git commit -s -m "feat(agent): register with locally generated device public key

The agent generates its WireGuard keypair on first run, persists it at
<config-dir>/device.key (0600), sends only the public key to the control
plane and ignores any private key the server still returns (ADR-0003).
The sandbox path (pre-registered CurrentPeer) is unchanged."
git push origin new_dev
```

---

### Task 3: Control plane accepts client public keys (standalone path)

**Files:**
- Modify: `internal/server/service/peer.go` (registerStandalone key block, ~line 479-495)
- Test: `internal/server/service/peer_register_test.go`

**Interfaces:**
- Consumes: fixture `newRegisterService(t, verifier)` in peer_register_test.go.
- Produces: register with `dto.PublicKey` set → stored as `peer.PublicKey`, `peer.PrivateKey` cleared, response `infra.Peer.PrivateKey` empty. Legacy requests keep the server-side path. Key mismatch on an existing client-key peer → error.

- [ ] **Step 1: Write the failing tests** — append to `peer_register_test.go` (reuse its existing enrollment-token helper; if the file has a helper like `createToken`, use it — otherwise insert a token row via `st.EnrollmentTokens()` following the pattern of the file's first test):

```go
func TestRegisterStandalone_ClientPublicKey(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{})
	// Seed workspace + token exactly as the file's existing register test does.

	node, err := svc.Register(t.Context(), &dto.PeerDto{
		Token:     "<seeded-token>",
		AppID:     "device-a",
		PublicKey: mustPubKey(t), // helper below
	})
	require.NoError(t, err)
	assert.Equal(t, mustPubKey(t), node.PublicKey)
	assert.Empty(t, node.PrivateKey, "private key must never be returned to client-key agents")

	peer, err := st.Peers().GetByAppID(t.Context(), "device-a")
	require.NoError(t, err)
	assert.Equal(t, mustPubKey(t), peer.PublicKey)
	assert.Empty(t, peer.PrivateKey)
}

func TestRegisterStandalone_KeyMismatchRejected(t *testing.T) {
	svc, _ := newRegisterService(t, &fakeVerifier{})
	// First registration seeds a client-key peer for "device-a".
	_, err := svc.Register(t.Context(), &dto.PeerDto{Token: "<seeded-token>", AppID: "device-a", PublicKey: mustPubKey(t)})
	require.NoError(t, err)

	// Same AppID, different key → takeover attempt.
	_, err = svc.Register(t.Context(), &dto.PeerDto{Token: "<seeded-token>", AppID: "device-a", PublicKey: mustPubKey(t)})
	require.ErrorContains(t, err, "public key mismatch")
}

func mustPubKey(t *testing.T) string {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	require.NoError(t, err)
	return key.PublicKey().String()
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server/service/ -run TestRegisterStandalone_Client` → FAIL (PublicKey ignored / PrivateKey returned).

- [ ] **Step 3: Implement.** Replace the key block in `registerStandalone` (peer.go:479-495):

```go
	// ADR-0003: agents generate their WireGuard keypair locally and submit
	// only the public key. Legacy agents (no PublicKey in the request)
	// keep the server-side generation path during the compat window.
	switch {
	case dto.PublicKey != "":
		if _, pErr := wgtypes.ParseKey(dto.PublicKey); pErr != nil {
			return nil, fmt.Errorf("invalid public key: %w", pErr)
		}
		if peer.PublicKey != "" && peer.PublicKey != dto.PublicKey {
			if peer.PrivateKey != "" {
				// Legacy peer migrating to a client key: the old key was
				// server-generated, rotating to the client key is a strict
				// improvement — accept once.
				p.logger.Warn("peer migrated from server-side to client-side key", "app_id", peer.AppID)
			} else {
				return nil, fmt.Errorf("public key mismatch for peer %q; key rotation requires re-enrollment", peer.AppID)
			}
		}
		peer.PublicKey = dto.PublicKey
		peer.PrivateKey = ""
	case peer.PrivateKey != "":
		// Legacy resume: server-side key already stored.
	default:
		key, kErr := wgtypes.GeneratePrivateKey()
		if kErr != nil {
			return nil, fmt.Errorf("generate key: %w", kErr)
		}
		peer.PrivateKey = key.String()
		peer.PublicKey = key.PublicKey().String()
	}
```

- [ ] **Step 4: Run** — `go test -race ./internal/server/service/ -run TestRegisterStandalone` → PASS (new + existing tests).

- [ ] **Step 5: Commit + push**

```bash
git add internal/server/service/peer.go internal/server/service/peer_register_test.go
git commit -s -m "feat(server): accept client-generated public keys at registration

registerStandalone stores the client-submitted public key and stops
generating/returning a private key for those agents. Legacy agents keep
the server-side path. Re-registering an existing client-key peer with a
different public key is rejected (takeover guard); legacy peers rotate
to the client key once, since the old key was server-issued anyway."
git push origin new_dev
```

---

### Task 4: Approval model fields + workspace opt-in flag

**Files:**
- Modify: `internal/server/models/peer.go`, `internal/server/models/workspace.go`
- Test: `internal/server/models/peer_approval_test.go` (new)

**Interfaces:**
- Produces: `models.ApprovalApproved/Pending/Revoked` constants; `Peer.ApprovalStatus` (default `'approved'`), `Peer.ApprovedBy`, `Peer.ApprovedAt`; `Workspace.RequirePeerApproval` (default false).

- [ ] **Step 1: Write the failing test** — `internal/server/models/peer_approval_test.go`:

```go
package models

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPeerApprovalDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Peer{}, &Workspace{}); err != nil {
		t.Fatal(err)
	}

	p := Peer{WorkspaceID: "ws", Name: "n"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if p.ApprovalStatus != ApprovalApproved {
		t.Fatalf("default approval status = %q, want %q", p.ApprovalStatus, ApprovalApproved)
	}

	w := Workspace{Slug: "s", Namespace: "wf-x", DisplayName: "d"}
	if err := db.Create(&w).Error; err != nil {
		t.Fatal(err)
	}
	if w.RequirePeerApproval {
		t.Fatal("RequirePeerApproval must default to false")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server/models/ -run TestPeerApprovalDefaults` → FAIL (undefined fields).

- [ ] **Step 3: Implement.** In `models/peer.go` add above `Disabled`:

```go
// Peer approval lifecycle (ADR-0003). Approval is opt-in per workspace;
// the default 'approved' keeps existing rows and workspaces unchanged.
const (
	ApprovalApproved = "approved"
	ApprovalPending  = "pending"
	ApprovalRevoked  = "revoked"
)
```

and on the struct:

```go
	ApprovalStatus string     `gorm:"size:20;default:'approved';index" json:"approval_status,omitempty"`
	ApprovedBy     string     `gorm:"size:100" json:"approved_by,omitempty"`
	ApprovedAt     *time.Time `json:"approved_at,omitempty"`
```

In `models/workspace.go` add:

```go
	// RequirePeerApproval gates new enrollments behind an administrator
	// approval step (ADR-0003). Default false: opt-in per workspace.
	RequirePeerApproval bool `gorm:"default:false" json:"requirePeerApproval"`
```

- [ ] **Step 4: Run** — `go test -race ./internal/server/models/` → PASS. Also `go build ./...` (gorm defaults need no repo changes).

- [ ] **Step 5: Commit + push**

```bash
git add internal/server/models/peer.go internal/server/models/workspace.go internal/server/models/peer_approval_test.go
git commit -s -m "feat(server): approval lifecycle fields on Peer and workspace opt-in flag"
git push origin new_dev
```

---

### Task 5: Pending registration (defer address allocation)

**Files:**
- Modify: `internal/server/service/peer.go` (registerStandalone: workspace lookup, address allocation, response construction)
- Modify: `internal/agent/infra/message.go` (add `ApprovalStatus` wire field)
- Test: `internal/server/service/peer_register_test.go`

**Interfaces:**
- Consumes: Task 3/4 fields; existing `p.store.Workspaces().GetByID` (peer.go:525 pattern).
- Produces: pending peers are stored with `ApprovalStatus=pending` and **empty Address**; the register response carries `ApprovalStatus` and no address. `infra.Peer.ApprovalStatus string json:"approvalStatus,omitempty"`.

- [ ] **Step 1: Add the wire field** — in `internal/agent/infra/message.go`, `type Peer struct`, after `Tier`:

```go
	ApprovalStatus      string            `json:"approvalStatus,omitempty"` // pending / approved / revoked (ADR-0003)
```

- [ ] **Step 2: Write the failing test** — in `peer_register_test.go`:

```go
func TestRegisterStandalone_PendingWhenWorkspaceRequiresApproval(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{})
	// Seed workspace with RequirePeerApproval=true + token, following the
	// file's existing workspace seeding; then:
	require.NoError(t, st.Workspaces(). /* set RequirePeerApproval on the seeded workspace, via Update */ )

	node, err := svc.Register(t.Context(), &dto.PeerDto{Token: "<seeded-token>", AppID: "device-p", PublicKey: mustPubKey(t)})
	require.NoError(t, err)
	assert.Equal(t, models.ApprovalPending, node.ApprovalStatus)
	assert.Nil(t, node.Address, "pending peer must not receive an overlay address")

	peer, err := st.Peers().GetByAppID(t.Context(), "device-p")
	require.NoError(t, err)
	assert.Equal(t, models.ApprovalPending, peer.ApprovalStatus)
	assert.Empty(t, peer.Address)
}
```

- [ ] **Step 3: Run to verify failure** — `go test ./internal/server/service/ -run Pending` → FAIL.

- [ ] **Step 4: Implement** in `registerStandalone`:
  (a) after the token is loaded, load the workspace (mirror the lookup used at peer.go:525):

```go
	workspace, wsErr := p.store.Workspaces().GetByID(ctx, tok.WorkspaceID)
	if wsErr != nil {
		return nil, wsErr
	}
```

  (b) gate the existing `if peer == nil { ... }` creation block: when the workspace requires approval, skip `AllocateAddress` and record pending status. Concretely — inside the creation block replace the unconditional allocation:

```go
		peer = &models.Peer{
			WorkspaceID:    tok.WorkspaceID,
			Name:           cmp.Or(dto.Name, dto.AppID),
			AppID:          dto.AppID,
			Token:          dto.Token,
			ApprovalStatus: models.ApprovalApproved,
		}
		if workspace.RequirePeerApproval {
			// Address allocation is deferred to approval time (ADR-0003);
			// the empty address also hides the peer from every netmap via
			// the existing "still enrolling" skip.
			peer.ApprovalStatus = models.ApprovalPending
		} else {
			rows, listErr := p.store.Peers().ListByWorkspace(ctx, tok.WorkspaceID)
			if listErr != nil {
				return nil, listErr
			}
			taken := make([]string, 0, len(rows))
			for _, r := range rows {
				taken = append(taken, r.Address)
			}
			address, allocErr := reconcilers.AllocateAddress(taken)
			if allocErr != nil {
				return nil, allocErr
			}
			peer.Address = address
		}
```

  (c) in the response construction (`node := &infra.Peer{...}`) add:

```go
		ApprovalStatus: peer.ApprovalStatus,
```

- [ ] **Step 5: Run** — `go test -race ./internal/server/service/ ./internal/agent/...` → PASS. `go vet ./internal/server/service/ ./internal/agent/` clean.

- [ ] **Step 6: Commit + push**

```bash
git add internal/server/service/peer.go internal/server/service/peer_register_test.go internal/agent/infra/message.go
git commit -s -m "feat(server): pending registration defers address allocation

Workspaces with RequirePeerApproval enroll new peers as 'pending' with
no overlay address; the register response carries approvalStatus so the
agent can show 'awaiting approval'. The empty address also keeps the
peer out of every netmap via the existing enrolling-skip."
git push origin new_dev
```

---

### Task 6: Netmap gating on approval status

**Files:**
- Modify: `internal/server/reconcilers/netmap_builder.go`
- Test: `internal/server/reconcilers/netmap_builder_test.go`

**Interfaces:**
- Consumes: `models.Approval*` constants (Task 4), `dbToInfraPeer`.
- Produces: `BuildForAppID` returns a minimal "pending" message for unapproved peers; `BuildForPeer` excludes unapproved rows from `ComputedPeers`.

- [ ] **Step 1: Write the failing tests** — append to `netmap_builder_test.go`, reusing its fixture constructors (the file already builds a NetmapBuilder over seeded stores; mirror its first test's seeding):

```go
func TestBuildForAppID_PendingPeerGetsPendingMessage(t *testing.T) {
	// Seed an approved requester plus a second peer with
	// ApprovalStatus = models.ApprovalPending in the same workspace,
	// mirroring the file's existing seeding helpers.

	msg, err := builder.BuildForAppID(ctx, pendingPeerAppID, token)
	require.NoError(t, err) // NOT an error: the agent must keep polling
	require.NotNil(t, msg.Current)
	assert.Equal(t, models.ApprovalPending, msg.Current.ApprovalStatus)
	assert.Nil(t, msg.Current.Address)
	assert.Empty(t, msg.ComputedPeers)
}

func TestBuildForPeer_ExcludesUnapprovedPeers(t *testing.T) {
	// Seed: approved target + one pending peer + one revoked peer.
	msg, err := builder.BuildForPeer(ctx, approvedPeer)
	require.NoError(t, err)
	for _, p := range msg.ComputedPeers {
		assert.NotEqual(t, models.ApprovalPending, p.ApprovalStatus)
		assert.NotEqual(t, models.ApprovalRevoked, p.ApprovalStatus)
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server/reconcilers/ -run Approval` → FAIL.

- [ ] **Step 3: Implement** in `netmap_builder.go`:

(a) `BuildForAppID`, after the `peer.Disabled` check:

```go
	if peer.ApprovalStatus != "" && peer.ApprovalStatus != models.ApprovalApproved {
		// Not an error: the agent polls this endpoint and must keep its
		// heartbeat/poll loop alive while awaiting approval (ADR-0003).
		return b.pendingMessage(peer), nil
	}
```

(b) new helper at the bottom of the file:

```go
// pendingMessage returns the minimal netmap shown to a peer that has not
// been approved yet: its own identity, no address, no mesh peers.
func (b *NetmapBuilder) pendingMessage(peer *models.Peer) *infra.Message {
	addr := ""
	current := dbToInfraPeer(peer)
	current.Address = &addr
	current.PrivateKey = peer.PrivateKey // legacy agents need their key back
	return &infra.Message{Current: current}
}
```

(c) `BuildForPeer` computed-peers loop, extend the existing `row.Address == ""` skip:

```go
		if row.ApprovalStatus != "" && row.ApprovalStatus != models.ApprovalApproved {
			continue // pending/revoked peers are invisible to the mesh
		}
```

(d) `dbToInfraPeer`: map `ApprovalStatus: row.ApprovalStatus`.

- [ ] **Step 4: Run** — `go test -race ./internal/server/reconcilers/` → PASS (existing tests must stay green; their seeds default to `approved` via the gorm default).

- [ ] **Step 5: Commit + push**

```bash
git add internal/server/reconcilers/netmap_builder.go internal/server/reconcilers/netmap_builder_test.go
git commit -s -m "feat(server): gate netmaps on peer approval status

Pending/revoked peers disappear from every peer's computed netmap, and
a pending peer itself receives a minimal address-less message instead
of an error so its poll loop keeps running (ADR-0003)."
git push origin new_dev
```

---

### Task 7: SetPeerApproval service method

**Files:**
- Modify: `internal/server/service/peer.go` (`PeerService` interface + implementation)
- Test: `internal/server/service/peer_register_test.go`

**Interfaces:**
- Produces: `PeerService.SetPeerApproval(ctx, namespace, name, status string) error` — `approved` allocates an address if missing, records approver metadata; `revoked` gates the netmap (Task 6); other statuses rejected. Fires `notifyWorkspacePeers` on every transition.

- [ ] **Step 1: Write the failing tests** — in `peer_register_test.go`:

```go
func TestSetPeerApproval_ApproveAllocatesAddress(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{})
	// Seed workspace with RequirePeerApproval=true; register "device-p"
	// (ends pending, no address) as in Task 5's test.

	require.NoError(t, svc.SetPeerApproval(t.Context(), "<namespace>", "device-p", models.ApprovalApproved))

	peer, err := st.Peers().GetByAppID(t.Context(), "device-p")
	require.NoError(t, err)
	assert.Equal(t, models.ApprovalApproved, peer.ApprovalStatus)
	assert.NotEmpty(t, peer.Address, "approval must allocate the overlay address")
	assert.NotEmpty(t, peer.ApprovedBy)
}

func TestSetPeerApproval_RejectsUnknownStatus(t *testing.T) {
	svc, _ := newRegisterService(t, &fakeVerifier{})
	if err := svc.SetPeerApproval(t.Context(), "<namespace>", "x", "maybe"); err == nil {
		t.Fatal("unknown status must be rejected")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/server/service/ -run TestSetPeerApproval` → FAIL (method missing).

- [ ] **Step 3: Implement.** Add to the `PeerService` interface (locate via `grep -n "type PeerService interface" internal/server/service/peer.go`):

```go
	SetPeerApproval(ctx context.Context, namespace, name, status string) error
```

Implementation on `*peerService` (place next to registerStandalone; reuse the workspace-lookup pattern from `DisablePeer` — find it via `grep -n "func (p \*peerService) DisablePeer" internal/server/service/peer.go` and copy its namespace→workspace resolution verbatim):

```go
// SetPeerApproval transitions a peer between approved/revoked (ADR-0003).
// Approving a pending peer allocates its overlay address, making it part
// of the mesh; revoking keeps the row but the netmap gates exclude it.
func (p *peerService) SetPeerApproval(ctx context.Context, namespace, name, status string) error {
	switch status {
	case models.ApprovalApproved, models.ApprovalRevoked:
	default:
		return fmt.Errorf("invalid approval status %q", status)
	}

	workspace, err := /* workspace lookup by namespace — copy from DisablePeer */
	if err != nil {
		return err
	}
	peer, err := p.store.Peers().GetByName(ctx, workspace.ID, name)
	if err != nil {
		return err
	}

	now := time.Now()
	peer.ApprovalStatus = status
	peer.ApprovedAt = &now
	if status == models.ApprovalRevoked {
		peer.Disabled = true
	}
	if status == models.ApprovalApproved {
		peer.Disabled = false
		if peer.Address == "" {
			rows, listErr := p.store.Peers().ListByWorkspace(ctx, workspace.ID)
			if listErr != nil {
				return listErr
			}
			taken := make([]string, 0, len(rows))
			for _, r := range rows {
				taken = append(taken, r.Address)
			}
			address, allocErr := reconcilers.AllocateAddress(taken)
			if allocErr != nil {
				return allocErr
			}
			peer.Address = address
		}
	}
	if err := p.store.Peers().Update(ctx, peer); err != nil {
		return err
	}
	p.notifyWorkspacePeers(ctx, workspace.ID, peer.AppID)
	return nil
}
```

Notes: `GetByName(ctx, workspaceID, name)` — if the peers repository exposes a different name lookup (check `grep -n "GetByName\|ListByWorkspace" internal/agent/store/store.go`), use that method; `ApprovedBy` stays empty in this plan (actor identity wiring is a documented follow-up); revoked also flips `Disabled` so iptables-side handling matches existing semantics.

- [ ] **Step 4: Run** — `go test -race ./internal/server/service/` → PASS.

- [ ] **Step 5: Commit + push**

```bash
git add internal/server/service/peer.go internal/server/service/peer_register_test.go
git commit -s -m "feat(server): SetPeerApproval service with address allocation on approve"
git push origin new_dev
```

---

### Task 8: HTTP endpoint `PUT /api/v1/peers/:name/approval`

**Files:**
- Modify: `internal/server/server/api.go` (route at the `peerApi` group, ~line 80; handler next to `disablePeer` at line 301)
- Modify: `internal/server/controller/<peer controller>` — locate via `grep -rn "func (c \*PeerController) DisablePeer" internal/server/controller/`
- Test: controller-level test with a stub `service.PeerService` (the controller already depends on the interface)

**Interfaces:**
- Consumes: `PeerService.SetPeerApproval` (Task 7).
- Produces: `PUT /api/v1/peers/:name/approval` body `{"status":"approved"|"revoked"}` → `resp.OK`.

- [ ] **Step 1: Add the controller method** (mirror `DisablePeer` in the same file; the controller's service field name matches whatever DisablePeer uses):

```go
func (c *PeerController) SetPeerApproval(ctx context.Context, namespace, name, status string) error {
	return c.<peerServiceField>.SetPeerApproval(ctx, namespace, name, status)
}
```

- [ ] **Step 2: Wire the route** — inside the `peerApi` group block in `api.go`:

```go
		peerApi.PUT("/:name/approval", s.setPeerApproval)
```

(Keep the group's existing `WorkspaceAuthMiddleware`; tightening to an admin role is a follow-up — `dto.RoleAdmin`'s availability is checked via `grep -n "RoleAdmin" internal/server/dto/` and applied as `peerApi.PUT("/:name/approval", s.middleware.WorkspaceAuthMiddleware(dto.RoleAdmin), s.setPeerApproval)` when present.)

- [ ] **Step 3: Add the handler** next to `disablePeer` (api.go:301):

```go
func (s *Server) setPeerApproval(c *gin.Context) {
	name := c.Param("name")
	ns, err := s.peerNamespace(c)
	if err != nil {
		resp.Error(c, err.Error())
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		resp.Error(c, err.Error())
		return
	}
	if err := s.peerController.SetPeerApproval(c.Request.Context(), ns, name, body.Status); err != nil {
		resp.Error(c, err.Error())
		return
	}
	resp.OK(c, nil)
}
```

- [ ] **Step 4: Build + vet + smoke** — `go build ./... && go vet ./internal/server/...` → clean. Manual smoke (documented in commit message): start `make build && ./bin/latticed`, register a pending peer, `curl -X PUT -H "Authorization: <token>" -d '{"status":"approved"}' .../api/v1/peers/device-p/approval` → `{"code":0}`.

- [ ] **Step 5: Commit + push**

```bash
git add internal/server/server/api.go internal/server/controller/
git commit -s -m "feat(server): approval endpoint PUT /api/v1/peers/:name/approval"
git push origin new_dev
```

---

### Task 9: Agent "awaiting approval" UX log

**Files:**
- Modify: `internal/agent/message_handler.go` (applyFullConfig)
- Test: extend `internal/agent/netmap_sync_test.go` or a new `message_handler_test.go`

**Interfaces:**
- Consumes: Task 5/6 — a pending peer receives `msg.Current` with nil address.

- [ ] **Step 1: Write the failing test** — `internal/agent/message_handler_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/provision"
)

// A pending peer's netmap (Current set, no Address, no peers) must apply
// as a silent no-op — no error, no device writes.
func TestApplyFullConfig_PendingPeerIsNoop(t *testing.T) {
	h := NewMessageHandler(nil, log.GetLogger("test"), provision.NewNoopEnforcer(log.GetLogger("test")))
	addr := ""
	msg := &infra.Message{Current: &infra.Peer{AppID: "self", Address: &addr}}
	if err := h.ApplyFullConfig(context.Background(), msg); err != nil {
		t.Fatalf("pending netmap must apply cleanly: %v", err)
	}
}
```

Note: check `provision.NewNoopEnforcer`'s exact signature first (`grep -n "func NewNoopEnforcer" internal/agent/provision/`); the apply path for a nil/empty address must not reach the provisioner — if the existing code already guards (`msg.Current.Address != nil`), the test passes unchanged; if it panics on nil deviceManager, guard order in applyFullConfig puts the address check first (Step 2 does that).

- [ ] **Step 2: Implement** — top of `applyFullConfig` in `message_handler.go`:

```go
	if msg.Current == nil || msg.Current.Address == nil {
		h.logger.Info("netmap empty: peer is awaiting administrator approval")
		return nil
	}
```

(early return replaces the fallthrough into ApplyIP for the nil-address case — the existing body already guarded with `msg.Current != nil && msg.Current.Address != nil`, so this only adds the log + explicit no-op.)

- [ ] **Step 3: Run** — `go test -race ./internal/agent/ -run TestApplyFullConfig` → PASS; then full `go test -race ./internal/agent/...`.

- [ ] **Step 4: Commit + push**

```bash
git add internal/agent/message_handler.go internal/agent/message_handler_test.go
git commit -s -m "feat(agent): pending peers log 'awaiting administrator approval'"
git push origin new_dev
```

---

### Task 10: CLI `lattice peer approve|reject` + client method

**Files:**
- Modify: `internal/agent/client/admin.go` (client method near the other `/api/v1/peers/*` calls at lines 269/295)
- Modify: `cmd/lattice/cmd/peer/peer.go` (subcommands)
- Test: `internal/agent/client/admin_test.go` (new; gin httptest stub)

**Interfaces:**
- Produces: `Client.SetPeerApproval(namespace, name, status string) error`; CLI `lattice peer approve <name> -n <ns>` / `lattice peer reject <name> -n <ns>`.

- [ ] **Step 1: Write the failing test** — `internal/agent/client/admin_test.go`:

```go
package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSetPeerApproval_RequestShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath, gotStatus string
	router := gin.New()
	router.PUT("/api/v1/peers/:name/approval", func(c *gin.Context) {
		gotPath = c.FullPath()
		var body map[string]string
		_ = c.ShouldBindJSON(&body)
		gotStatus = body["status"]
		c.JSON(200, gin.H{"code": 0})
	})
	ts := httptest.NewServer(router)
	defer ts.Close()

	c := NewClientForTest(t, ts.URL) // see note
	if err := c.SetPeerApproval("ws-ns", "device-p", "approved"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/peers/:name/approval" || gotStatus != "approved" {
		t.Fatalf("path=%q status=%q", gotPath, gotStatus)
	}
}
```

Note: `NewClientForTest` does not exist — check how existing client tests construct a Client against a test server (`grep -rn "httptest" internal/agent/client/`); if none exist, the method under test is exercised through `NewClient(ts.URL, "token")` and the namespace→ID resolution stubbed by registering `router.GET("/api/v1/workspaces...", ...)` returning `{"data":{"id":"ws-1"}}` per `resolveWorkspaceID`'s real path (`grep -n "resolveWorkspaceID" -A 12 internal/agent/client/admin.go` and mirror the endpoint it hits).

- [ ] **Step 2: Run to verify failure** — `go test ./internal/agent/client/ -run TestSetPeerApproval` → FAIL (method missing).

- [ ] **Step 3: Implement** — in `internal/agent/client/admin.go`, after the peers list/update methods:

```go
// SetPeerApproval approves or revokes a peer (ADR-0003).
func (c *Client) SetPeerApproval(namespace, name, status string) error {
	wsID, err := c.resolveWorkspaceID(namespace)
	if err != nil {
		return err
	}
	return c.do(context.Background(), http.MethodPut,
		"/api/v1/peers/"+name+"/approval", wsID,
		map[string]string{"status": status}, nil)
}
```

- [ ] **Step 4: Implement the CLI** — in `cmd/lattice/cmd/peer/peer.go`, extend `NewPeerCommand`'s `c.AddCommand(...)`:

```go
	c.AddCommand(
		peerListCmd(),
		peerLabelCmd(),
		peerApprovalCmd("approve", "approved", "Approve a pending peer"),
		peerApprovalCmd("reject", "revoked", "Reject/revoke a peer"),
	)
```

and add:

```go
// peerApprovalCmd builds `lattice peer approve|reject <name> -n <namespace>`.
func peerApprovalCmd(verb, status, short string) *cobra.Command {
	var namespace string
	c := &cobra.Command{
		Use:   verb + " <name>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			return client.SetPeerApproval(namespace, args[0], status)
		},
	}
	c.Flags().StringVarP(&namespace, "namespace", "n", "", "workspace namespace")
	_ = c.MarkFlagRequired("namespace")
	return c
}
```

(Mirror `peerListCmd`'s namespace flag style — `grep -n "namespace" cmd/lattice/cmd/peer/peer.go` — if the existing commands read it differently, match them.)

- [ ] **Step 5: Run** — `go build ./... && go test -race ./internal/agent/client/ ./cmd/...` → PASS.

- [ ] **Step 6: Commit + push**

```bash
git add internal/agent/client/admin.go internal/agent/client/admin_test.go cmd/lattice/cmd/peer/peer.go
git commit -s -m "feat(cli): lattice peer approve/reject with approval HTTP client"
git push origin new_dev
```

---

### Task 11: End-to-end verification + ADR status

**Files:**
- Modify: `docs/adr/0003-peer-enrollment-approval-and-client-side-keygen.md` (Status → `Adopted (standalone path, Phase 1)`)

- [ ] **Step 1: Full gate** — `go build ./... && go vet ./... && go test -race ./internal/... ` → all PASS (e2e suite excluded: needs k3d).

- [ ] **Step 2: Manual E2E smoke** (documented in the commit message):
  1. `./bin/latticed` (standalone); create workspace + token with approval enabled (set `require_peer_approval` via API/seed).
  2. `lattice up --server-url ... --token ...` on a second terminal → expect `awaiting administrator approval`, no `wf0` address.
  3. Dashboard/API shows the peer as pending; `lattice peer approve <name> -n <ns>` → agent converges within one poll cycle and gets an address.
  4. `lattice down && lattice up` on the same device → resumes approved (device key stable).
  5. Legacy check: an old agent binary (server-key flow) still registers into a non-approval workspace.

- [ ] **Step 3: Update ADR status + commit + push**

```bash
git add docs/adr/0003-peer-enrollment-approval-and-client-side-keygen.md
git commit -s -m "docs(adr): mark ADR-0003 adopted for the standalone path"
git push origin new_dev
```

---

## Self-Review Notes

- Spec coverage: ADR-0003 Decisions 1 (approval gate: Tasks 4–8, 10), 2 (client keygen: Tasks 1–3), 3 (key binding/rotation guard: Task 3 mismatch handling). Rollout Release-N scope only; wire-field removal and K8s defaults explicitly out of scope. K8s CRD path + dashboard UI + audit actor identity = follow-up plans (documented in Global Constraints).
- Type consistency: `SetPeerApproval(ctx, namespace, name, status string) error` used identically in Tasks 7/8/10; `models.Approval{Approved,Pending,Revoked}` in Tasks 4–7; `infra.Peer.ApprovalStatus` in Tasks 5–6.
- Open exec-time lookups (marked inline): exact seed helpers in `peer_register_test.go`, `DisablePeer`'s workspace lookup lines, controller file name, client-test construction — each has a concrete grep command and the code that follows.
