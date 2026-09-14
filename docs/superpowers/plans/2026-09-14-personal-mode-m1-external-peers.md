# Personal Mode M1: External WireGuard Peers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A phone running the stock WireGuard app can be added as an `external-wg` peer via `POST /api/v1/peers/external` and receive a one-time wg-quick config that tunnels it to the home agent, while the home agent's netmap automatically accepts the phone's traffic.

**Architecture:** New `external-wg` type on the existing `t_peer` registry (GORM AutoMigrate adds columns). A pure wg-quick renderer package, a thin service that generates the keypair/IPAM address and persists only the public key, one Gin handler on the existing `/api/v1/peers` group. Netmap needs no changes — `NetmapBuilder` already includes every peer row's `PublicKey`/`Address` as AllowedIPs.

**Spec:** `docs/superpowers/specs/2026-09-11-personal-mode-and-ai-trust-layer-design.md` §3.1, §3.2 (POST only), §3.7 (unit + e2e). This is milestone **M1** of the spec's §七 milestone list. M2 (agent fixed port/UPnP/doctor), M3 (dashboard QR/wizard) get their own plans.

**Out of scope for this plan:** `GET /config-status` (needs M2 doctor reporting), dashboard UI, agent changes, policy preset button.

**Tech Stack:** Go 1.25.8, Gin, GORM + glebarez/sqlite, `golang.zx2c4.com/wireguard/wgctrl/wgtypes` (already a dependency via `internal/agent/wireguard`), testify, Ginkgo/Gomega (e2e).

## Global Constraints

- Module `github.com/alatticeio/lattice`, Go 1.25.8.
- Community edition only — no PRO/license-gated code paths.
- Phone private keys exist only in the one-time HTTP response. Never persist, log, or re-serve them.
- Overlay pool is `10.96.0.2`–`10.96.0.254` via `reconcilers.AllocateAddress` (`netmap_builder.go:37`, base `10.96.0.`, starts at `.2`). Never invent a second allocator.
- Follow existing patterns: services live in `internal/server/service`, take `store.Store`, read the workspace from `ctx.Value(infra.WorkspaceKey)`; handlers live in `internal/server/server`, respond via `pkg/utils/resp` (`resp.OK` / `resp.BadRequest` / `resp.Error`).
- Standalone test harness = in-memory sqlite (`glebarez/sqlite`) + `gormstore.New`, as in `internal/server/service/standalone_token_test.go`.
- No new third-party dependencies.
- Conventional commits (`feat:`, `test:`, `docs:`).

---

### Task 1: Spike-1 — wireguard-go fixed port vs ICE coexistence (decision note)

M2 depends on this decision; M1 does not. Timebox: 2 hours. No production code.

**Files:**
- Create: `docs/superpowers/specs/2026-09-12-spike1-fixed-wg-port.md`

**Interfaces:**
- Produces: a committed decision note that names the fork functions controlling the WireGuard device UDP bind, and picks approach A ("bind the existing device to `personal.wg-port` when set") or B ("independent static listener alongside ICE"), with evidence.

- [ ] **Step 1: Trace the bind path**

Read `internal/agent/wireguard/wg.go` (`DeviceManager`, device construction) and follow into the fork (`go doc github.com/wireflowio/wireguard-go`, then the `device.Bind*`/`ListenPort` code paths in the module cache under `$GOPATH/pkg/mod/github.com/wireflowio/wireguard-go@v0.0.0-20260306075115-6de966ac2b08/`). Record: where the listen port is chosen today, whether `NewDevice(..., port)` (or equivalent) accepts a fixed port, and how ICE-derived sockets relate to the device bind.

- [ ] **Step 2: Write the decision note**

The note must contain: (1) the exact fork file/function names found, (2) chosen approach A or B with one-paragraph rationale, (3) the M2 work items implied. If the fork cannot bind a fixed port, approach B is mandatory and the note says what M2 must build instead.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-09-12-spike1-fixed-wg-port.md
git commit -m "docs(specs): spike-1 decision on fixed wireguard port"
```

---

### Task 2: `Peer.Type` / `Peer.Endpoints` columns

**Files:**
- Modify: `internal/server/models/peer.go`
- Create: `internal/server/service/external_peer_test.go`
- No migration edit needed: `internal/db/gormstore/migrate.go:14` already AutoMigrates `&models.Peer{}` and AutoMigrate adds new columns incrementally.

**Interfaces:**
- Produces: `models.PeerTypeAgent = "agent"`, `models.PeerTypeExternalWG = "external-wg"` constants; `Peer.Type string`, `Peer.Endpoints string` fields consumed by Tasks 4–7.

- [ ] **Step 1: Write the failing test**

Create `internal/server/service/external_peer_test.go` (harness mirrors `standalone_token_test.go:27-40`):

```go
package service_test

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

func newExternalPeerStore(t *testing.T) (context.Context, store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{}, &models.Workspace{}, &models.UserProfile{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Home",
	}))
	return ctx, st
}

func TestPeerTypeRoundTrip(t *testing.T) {
	ctx, st := newExternalPeerStore(t)
	err := st.Peers().Create(ctx, &models.Peer{
		Model:   models.Model{ID: "p1"},
		WorkspaceID: "ws1", Name: "iPhone", AppID: "ext-p1",
		Type: models.PeerTypeExternalWG, PublicKey: "pub", Address: "10.96.0.2",
	})
	require.NoError(t, err)

	peer, err := st.Peers().GetByID(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, models.PeerTypeExternalWG, peer.Type)
}
```

Also add the missing import `"github.com/alatticeio/lattice/internal/agent/store"` for `store.Store` in the signature.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/server/service/ -run TestPeerTypeRoundTrip -v`
Expected: FAIL — `undefined: models.PeerTypeExternalWG` (compile error).

- [ ] **Step 3: Add the fields**

In `internal/server/models/peer.go`, after the existing imports add:

```go
// Peer type discriminator: agent = runs the lattice agent; external-wg =
// stock WireGuard client (phone/tablet) enrolled via a one-time config.
const (
	PeerTypeAgent      = "agent"
	PeerTypeExternalWG = "external-wg"
)
```

Inside the `Peer` struct, after the `Token` field add:

```go
	Type      string `gorm:"size:20;default:'agent';index" json:"type"`
	Endpoints string `gorm:"size:500" json:"endpoints,omitempty"` // candidate public endpoints (manual/UPnP/doctor)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server/service/ -run TestPeerTypeRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/models/peer.go internal/server/service/external_peer_test.go
git commit -m "feat(standalone): add peer type and endpoints columns"
```

---

### Task 3: wg-quick renderer package

**Files:**
- Create: `internal/server/service/wgconfig/config.go`
- Create: `internal/server/service/wgconfig/config_test.go`

**Interfaces:**
- Produces: `wgconfig.Input{PrivateKey, Address, ServerAddress, ServerPublicKey, Endpoint string; FullTunnel bool; Keepalive int}` and `wgconfig.Render(Input) (string, error)` consumed by Task 4.

- [ ] **Step 1: Write the failing tests**

Create `internal/server/service/wgconfig/config_test.go`:

```go
package wgconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validInput() Input {
	return Input{
		PrivateKey:      "4OrJdT0j40bWropcaBQL8elsOBbN3iVwfKLfXmFPH1c=",
		Address:         "10.96.0.3",
		ServerAddress:   "10.96.0.2",
		ServerPublicKey: "rBGTtNU+hi5TQuqKHOD0xsGMnyMrXda9iT9Q7hLQ6W0=",
		Endpoint:        "198.51.100.7:51820",
	}
}

func TestRenderDefault(t *testing.T) {
	cfg, err := Render(validInput())
	require.NoError(t, err)
	assert.Contains(t, cfg, "[Interface]\nPrivateKey = 4OrJdT0j40bWropcaBQL8elsOBbN3iVwfKLfXmFPH1c=")
	assert.Contains(t, cfg, "Address = 10.96.0.3/32")
	assert.Contains(t, cfg, "[Peer]\nPublicKey = rBGTtNU+hi5TQuqKHOD0xsGMnyMrXda9iT9Q7hLQ6W0=")
	assert.Contains(t, cfg, "Endpoint = 198.51.100.7:51820")
	assert.Contains(t, cfg, "AllowedIPs = 10.96.0.2/32")
	assert.Contains(t, cfg, "PersistentKeepalive = 25")
}

func TestRenderFullTunnel(t *testing.T) {
	in := validInput()
	in.FullTunnel = true
	cfg, err := Render(in)
	require.NoError(t, err)
	assert.Contains(t, cfg, "AllowedIPs = 0.0.0.0/0, ::/0")
}

func TestRenderIPv6Endpoint(t *testing.T) {
	in := validInput()
	in.Endpoint = "[2001:db8::1]:51820"
	cfg, err := Render(in)
	require.NoError(t, err)
	assert.Contains(t, cfg, "Endpoint = [2001:db8::1]:51820")
}

func TestRenderErrors(t *testing.T) {
	cases := map[string]func(*Input){
		"missing private key":  func(i *Input) { i.PrivateKey = "" },
		"bad phone address":    func(i *Input) { i.Address = "not-an-ip" },
		"bad server address":   func(i *Input) { i.ServerAddress = "not-an-ip" },
		"missing server key":   func(i *Input) { i.ServerPublicKey = "" },
		"missing endpoint":     func(i *Input) { i.Endpoint = "" },
		"endpoint without port": func(i *Input) { i.Endpoint = "198.51.100.7" },
		"endpoint bad port":    func(i *Input) { i.Endpoint = "198.51.100.7:99999" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validInput()
			mutate(&in)
			_, err := Render(in)
			assert.Error(t, err)
		})
	}
}

func TestRenderStableOrder(t *testing.T) {
	cfg, err := Render(validInput())
	require.NoError(t, err)
	iface := strings.Index(cfg, "[Interface]")
	peer := strings.Index(cfg, "[Peer]")
	require.NotEqual(t, -1, iface)
	require.NotEqual(t, -1, peer)
	assert.Less(t, iface, peer, "[Interface] must precede [Peer]")
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/service/wgconfig/ -v`
Expected: FAIL — package has no `Render` (compile error).

- [ ] **Step 3: Implement**

Create `internal/server/service/wgconfig/config.go`:

```go
// Package wgconfig renders standard wg-quick configurations for external-wg
// peers (stock WireGuard clients). It is a pure function package: no I/O,
// no key generation, no storage.
package wgconfig

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Input carries everything needed to render one phone-side configuration.
type Input struct {
	PrivateKey      string // phone private key — one-time response only
	Address         string // phone overlay IP, rendered as <ip>/32
	ServerAddress   string // home agent overlay IP, default AllowedIPs target
	ServerPublicKey string // home agent public key
	Endpoint        string // home agent public endpoint, host:port (IPv6 as [h]:p)
	FullTunnel      bool   // true => route all phone traffic through home
	Keepalive       int    // PersistentKeepalive seconds, 0 => 25
}

// Render produces the wg-quick config text exactly as the official
// WireGuard app expects it when imported from a QR code.
func Render(in Input) (string, error) {
	priv := strings.TrimSpace(in.PrivateKey)
	if priv == "" {
		return "", fmt.Errorf("private key is required")
	}
	phone := strings.TrimSpace(in.Address)
	if net.ParseIP(phone) == nil {
		return "", fmt.Errorf("invalid phone address %q", in.Address)
	}
	server := strings.TrimSpace(in.ServerAddress)
	if net.ParseIP(server) == nil {
		return "", fmt.Errorf("invalid server address %q", in.ServerAddress)
	}
	pub := strings.TrimSpace(in.ServerPublicKey)
	if pub == "" {
		return "", fmt.Errorf("server public key is required")
	}
	endpoint := strings.TrimSpace(in.Endpoint)
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	keepalive := in.Keepalive
	if keepalive == 0 {
		keepalive = 25
	}
	allowed := fmt.Sprintf("%s/32", server)
	if in.FullTunnel {
		allowed = "0.0.0.0/0, ::/0"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s/32\n\n", priv, phone)
	fmt.Fprintf(&b, "[Peer]\nPublicKey = %s\nEndpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %d\n",
		pub, endpoint, allowed, keepalive)
	return b.String(), nil
}

func validateEndpoint(ep string) error {
	if ep == "" {
		return fmt.Errorf("endpoint is required")
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return fmt.Errorf("endpoint must be host:port, got %q", ep)
	}
	if host == "" {
		return fmt.Errorf("endpoint host is empty")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid endpoint port %q", port)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server/service/wgconfig/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/service/wgconfig/
git commit -m "feat(personal): wg-quick config renderer for external peers"
```

---

### Task 4: ExternalPeerService (keypair, IPAM, one-time response)

**Files:**
- Create: `internal/server/service/external_peer.go`
- Modify: `internal/server/service/external_peer_test.go` (append tests)

**Interfaces:**
- Consumes: `store.Store` (`Peers().Create/ListByWorkspace/GetByID`, `Workspaces().GetByID`), `models.PeerTypeExternalWG` (Task 2), `wgconfig.Render` (Task 3), `reconcilers.AllocateAddress(taken []string) (string, error)`, `wgtypes.GeneratePrivateKey()`.
- Produces: `service.ExternalPeerService` interface with `Create(ctx, *ExternalPeerCreateRequest) (*ExternalPeerCreateResponse, error)`; types `ExternalPeerCreateRequest{Name, HomePeerName, EndpointOverride string; FullTunnel bool}` and `ExternalPeerCreateResponse{PeerID, Name, Address, Config string}` consumed by Task 5.

- [ ] **Step 1: Write the failing tests**

Append to `internal/server/service/external_peer_test.go` (the file from Task 2; `workspaceContext` is the helper already defined in `standalone_token_test.go` — same package):

```go
func newExternalPeerService(t *testing.T) (context.Context, service.ExternalPeerService, store.Store) {
	t.Helper()
	ctx, st := newExternalPeerStore(t)
	// The home agent peer the phone will tunnel to.
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model:       models.Model{ID: "home1"},
		WorkspaceID: "ws1", Name: "home-node", AppID: "home-node-1",
		Type: models.PeerTypeAgent, PublicKey: "agent-pub-key", Address: "10.96.0.2",
	}))
	return workspaceContext(ctx, "ws1"), service.NewExternalPeerService(st), st
}

func TestExternalPeerCreate_OneTimeConfig(t *testing.T) {
	ctx, svc, st := newExternalPeerService(t)

	out, err := svc.Create(ctx, &service.ExternalPeerCreateRequest{
		Name: "iPhone", EndpointOverride: "198.51.100.7:51820",
	})
	require.NoError(t, err)

	assert.Equal(t, "iPhone", out.Name)
	assert.Equal(t, "10.96.0.3", out.Address)
	assert.Contains(t, out.Config, "Address = 10.96.0.3/32")
	assert.Contains(t, out.Config, "Endpoint = 198.51.100.7:51820")
	assert.Contains(t, out.Config, "AllowedIPs = 10.96.0.2/32")
	assert.Contains(t, out.Config, "PublicKey = agent-pub-key")

	// Only the public key is persisted; the private key must not appear in the row.
	peer, err := st.Peers().GetByID(ctx, out.PeerID)
	require.NoError(t, err)
	assert.Equal(t, models.PeerTypeExternalWG, peer.Type)
	assert.NotEmpty(t, peer.PublicKey)
	assert.Empty(t, peer.Token)
	privLine := out.Config[strings.Index(out.Config, "PrivateKey =")+len("PrivateKey ="):]
	privLine = strings.TrimSpace(privLine[:strings.Index(privLine, "\n")])
	assert.NotContains(t, fmt.Sprintf("%+v", *peer), privLine)
}

func TestExternalPeerCreate_RequiresEndpoint(t *testing.T) {
	ctx, svc, _ := newExternalPeerService(t)
	_, err := svc.Create(ctx, &service.ExternalPeerCreateRequest{Name: "iPad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint")
}

func TestExternalPeerCreate_FullTunnel(t *testing.T) {
	ctx, svc, _ := newExternalPeerService(t)
	out, err := svc.Create(ctx, &service.ExternalPeerCreateRequest{
		Name: "iPad", EndpointOverride: "[2001:db8::1]:51820", FullTunnel: true,
	})
	require.NoError(t, err)
	assert.Contains(t, out.Config, "AllowedIPs = 0.0.0.0/0, ::/0")
}

func TestExternalPeerCreate_NameDedup(t *testing.T) {
	ctx, svc, _ := newExternalPeerService(t)
	first, err := svc.Create(ctx, &service.ExternalPeerCreateRequest{Name: "Phone", EndpointOverride: "198.51.100.7:51820"})
	require.NoError(t, err)
	second, err := svc.Create(ctx, &service.ExternalPeerCreateRequest{Name: "Phone", EndpointOverride: "198.51.100.7:51820"})
	require.NoError(t, err)
	assert.NotEqual(t, first.Name, second.Name)
	assert.Equal(t, "Phone 2", second.Name)
}

func TestExternalPeerCreate_RespectsWorkspaceNodeLimit(t *testing.T) {
	ctx, st := newExternalPeerStore(t)
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws2"}, Namespace: "wf-ws2", DisplayName: "Tiny", MaxNodeCount: 2,
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "home2"}, WorkspaceID: "ws2", Name: "home-node", AppID: "home-node-2",
		Type: models.PeerTypeAgent, PublicKey: "agent-pub-key-2", Address: "10.96.0.2",
	}))
	svc := service.NewExternalPeerService(st)
	sctx := workspaceContext(ctx, "ws2")

	_, err := svc.Create(sctx, &service.ExternalPeerCreateRequest{Name: "iPhone", EndpointOverride: "198.51.100.7:51820"})
	require.NoError(t, err, "second node fits the cap")
	_, err = svc.Create(sctx, &service.ExternalPeerCreateRequest{Name: "iPad", EndpointOverride: "198.51.100.7:51820"})
	require.Error(t, err, "third node exceeds the cap")
	assert.Contains(t, err.Error(), "node limit")
}
```

The file's complete import block after this step:

```go
import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

(The `gorm`, `gormstore`, `sqlite` imports from Task 2 stay — `newExternalPeerStore` uses them.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/server/service/ -run TestExternalPeer -v`
Expected: FAIL — `undefined: service.NewExternalPeerService` (compile error).

- [ ] **Step 3: Implement**

Create `internal/server/service/external_peer.go`:

```go
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/alatticeio/lattice/internal/server/service/wgconfig"
	"github.com/google/uuid"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ExternalPeerCreateRequest carries the dashboard's "add phone" inputs.
type ExternalPeerCreateRequest struct {
	Name             string `json:"name"`
	HomePeerName     string `json:"homePeerName"`     // the agent peer to tunnel to; optional when unambiguous
	EndpointOverride string `json:"endpointOverride"` // home public endpoint host:port
	FullTunnel       bool   `json:"fullTunnel"`
}

// ExternalPeerCreateResponse is the ONLY delivery of the phone's private
// key. The service never stores it; losing the response means re-enroll.
type ExternalPeerCreateResponse struct {
	PeerID  string `json:"peerId"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Config  string `json:"config"`
}

// ExternalPeerService enrolls stock WireGuard clients (phones/tablets).
type ExternalPeerService interface {
	Create(ctx context.Context, req *ExternalPeerCreateRequest) (*ExternalPeerCreateResponse, error)
}

type externalPeerService struct {
	store store.Store
}

// NewExternalPeerService builds the service on the standalone registry.
func NewExternalPeerService(st store.Store) ExternalPeerService {
	return &externalPeerService{store: st}
}

func (s *externalPeerService) Create(ctx context.Context, req *ExternalPeerCreateRequest) (*ExternalPeerCreateResponse, error) {
	wsID, _ := ctx.Value(infra.WorkspaceKey).(string)
	if wsID == "" {
		return nil, fmt.Errorf("workspace context is required")
	}
	if req == nil || strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	endpoint := strings.TrimSpace(req.EndpointOverride)
	if endpoint == "" {
		return nil, fmt.Errorf("endpointOverride is required: no connectivity report yet, set the home endpoint manually")
	}

	workspace, err := s.store.Workspaces().GetByID(ctx, wsID)
	if err != nil {
		return nil, err
	}

	rows, err := s.store.Peers().ListByWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	if workspace.MaxNodeCount > 0 && len(rows) >= workspace.MaxNodeCount {
		return nil, fmt.Errorf("workspace node limit reached (%d)", workspace.MaxNodeCount)
	}
	home, err := pickHomePeer(rows, req.HomePeerName)
	if err != nil {
		return nil, err
	}

	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	taken := make([]string, 0, len(rows))
	for _, r := range rows {
		taken = append(taken, r.Address)
	}
	address, err := reconcilers.AllocateAddress(taken)
	if err != nil {
		return nil, err
	}

	peerID := uuid.NewString()
	peer := &models.Peer{
		Model:       models.Model{ID: peerID},
		WorkspaceID: wsID,
		Name:        uniqueName(strings.TrimSpace(req.Name), rows),
		AppID:       "ext-" + peerID,
		Type:        models.PeerTypeExternalWG,
		PublicKey:   priv.PublicKey().String(),
		Address:     address,
		Endpoints:   endpoint,
	}
	if err := s.store.Peers().Create(ctx, peer); err != nil {
		return nil, err
	}

	cfg, err := wgconfig.Render(wgconfig.Input{
		PrivateKey:      priv.String(),
		Address:         address,
		ServerAddress:   home.Address,
		ServerPublicKey: home.PublicKey,
		Endpoint:        endpoint,
		FullTunnel:      req.FullTunnel,
	})
	if err != nil {
		return nil, err
	}
	return &ExternalPeerCreateResponse{PeerID: peerID, Name: peer.Name, Address: address, Config: cfg}, nil
}

// pickHomePeer resolves which agent peer the phone tunnels to.
func pickHomePeer(rows []*models.Peer, homePeerName string) (*models.Peer, error) {
	var eligible []*models.Peer
	for _, r := range rows {
		if r.Type == models.PeerTypeAgent && !r.Disabled && r.PublicKey != "" && r.Address != "" {
			eligible = append(eligible, r)
		}
	}
	if homePeerName != "" {
		for _, r := range eligible {
			if r.Name == homePeerName {
				return r, nil
			}
		}
		return nil, fmt.Errorf("home peer %q is not an enrollable agent peer", homePeerName)
	}
	if len(eligible) == 1 {
		return eligible[0], nil
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no agent peer enrolled yet: enroll the home computer first")
	}
	names := make([]string, 0, len(eligible))
	for _, r := range eligible {
		names = append(names, r.Name)
	}
	return nil, fmt.Errorf("multiple agent peers, set homePeerName to one of: %s", strings.Join(names, ", "))
}

// uniqueName appends " 2", " 3", ... until the workspace-unique name is free.
func uniqueName(base string, rows []*models.Peer) string {
	taken := make(map[string]bool, len(rows))
	for _, r := range rows {
		taken[r.Name] = true
	}
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s %d", base, i)
		if !taken[candidate] {
			return candidate
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/server/service/ -run TestExternalPeer -v`
Expected: all PASS. If `TestExternalPeerCreate_OneTimeConfig` fails on `Address = 10.96.0.3`, verify only one peer row existed before Create (the harness seeds exactly `home-node` at `10.96.0.2`).

- [ ] **Step 5: Run the whole package suite (guard against regressions)**

Run: `go build ./... && go test ./internal/server/service/...`
Expected: build OK, all packages PASS (wgconfig + service).

- [ ] **Step 6: Commit**

```bash
git add internal/server/service/external_peer.go internal/server/service/external_peer_test.go
git commit -m "feat(personal): external peer enrollment with one-time wg-quick config"
```

---

### Task 5: HTTP handler and route

**Files:**
- Create: `internal/server/server/external_peer.go`
- Modify: `internal/server/server/api.go` (inside the `peerApi` group, around line 80)

**Interfaces:**
- Consumes: `service.NewExternalPeerService` / `service.ExternalPeerCreateRequest` (Task 4), `s.store` (already a `Server` field — see `peerNamespace` at `api.go:277`).
- Produces: `POST /api/v1/peers/external`, mounted inside the `WorkspaceAuthMiddleware(dto.RoleViewer)`-protected group.

- [ ] **Step 1: Add the handler**

Create `internal/server/server/external_peer.go`:

```go
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

package server

import (
	"github.com/gin-gonic/gin"

	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/alatticeio/lattice/pkg/utils/resp"
)

// createExternalPeer enrolls a stock WireGuard client (personal mode).
// The response body carries the phone's private key exactly once.
func (s *Server) createExternalPeer() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req service.ExternalPeerCreateRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.BadRequest(c, "invalid params")
			return
		}
		out, err := service.NewExternalPeerService(s.store).Create(c.Request.Context(), &req)
		if err != nil {
			resp.Error(c, err.Error())
			return
		}
		resp.OK(c, out)
	}
}
```

- [ ] **Step 2: Mount the route**

In `internal/server/server/api.go`, inside the `peerApi` group block add one line as the first route:

```go
		peerApi.POST("/external", s.createExternalPeer())
```

- [ ] **Step 3: Build and vet**

Run: `go build ./... && go vet ./internal/server/...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add internal/server/server/external_peer.go internal/server/server/api.go
git commit -m "feat(personal): POST /api/v1/peers/external endpoint"
```

---

### Task 6: Surface `Type` in the peer list API

**Files:**
- Modify: `internal/server/vo/peer.go` (struct near line 15)
- Modify: `internal/server/service/peer.go` (`peerItem` at line 137, mapping at lines 207-218, VO literal at lines 263-272)

**Interfaces:**
- Consumes: `models.Peer.Type` (Task 2).
- Produces: `vo.PeerVo.Type` (JSON `type`) read by the M3 dashboard; no server code depends on it.

- [ ] **Step 1: Add the VO field**

In `internal/server/vo/peer.go`, after the `Platform` field add:

```go
	Type                string    `json:"type,omitempty"` // agent | external-wg
```

- [ ] **Step 2: Carry it through the standalone list path**

In `internal/server/service/peer.go`:

Add to the `peerItem` struct (line 137) after `disabled bool`:

```go
	peerType    string
```

In `listPeersStandalone`'s mapping loop (the `allPeers = append(allPeers, peerItem{...}` at lines 208-218)) add after `disabled: r.Disabled,`:

```go
			peerType:    r.Type,
```

In `renderPeerPage`'s VO literal (the `pv := vo.PeerVo{...}` at lines 263-272) add after `Disabled: n.disabled,`:

```go
			Type:                 n.peerType,
```

- [ ] **Step 3: Build and run service tests**

Run: `go build ./... && go test ./internal/server/service/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/server/vo/peer.go internal/server/service/peer.go
git commit -m "feat(personal): expose peer type in list API"
```

---

### Task 7: Standalone e2e — enroll an external peer over REST

**Files:**
- Create: `test/e2e_standalone/external_peer_specs_test.go`

**Interfaces:**
- Consumes: suite helpers from `standalone_specs_test.go` / `standalone_suite_test.go` — `serverURL`, `natsURL`, `apiPOST(url, accessToken, body, headers...)`, `apiGET(url, accessToken, headers...)`, `newRequest(method, url, body, token, wsID)`, `doAPI(req)`, `dataMap(data)`, `header(workspaceID)`; NATS peer registration subject `lattice.signals.peer.register` with `dto.PeerDto`.
- Produces: regression coverage for the full M1 REST loop; run via `go test ./test/e2e_standalone/...` (the suite builds and boots `latticed --standalone` itself; port 4222 must be free).

- [ ] **Step 1: Write the spec**

Create `test/e2e_standalone/external_peer_specs_test.go`:

```go
// Copyright 2026 The Lattice Authors, Inc.
// Use of this source code is governed by the Apache-2.0 license found in LICENSE.

package standalone_e2e

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Personal-mode M1: enroll the home agent peer, then add a stock-WireGuard
// phone as an external-wg peer and verify the one-time config contract.
var _ = Describe("External WireGuard peers", Ordered, func() {
	var (
		accessToken string
		workspaceID string
		joinToken   string
		homePubKey  string
		nc          *nats.Conn
	)

	BeforeAll(func() {
		status, data := apiPOST(serverURL+"/api/v1/users/login", "", map[string]any{
			"username": "admin", "password": "123456",
		})
		Expect(status).To(Equal(200), "login failed: %s", data.Msg)
		accessToken = dataMap(data)["token"]

		status, data = apiPOST(serverURL+"/api/v1/workspaces/add", accessToken, map[string]any{
			"namespace":    "e2e-personal",
			"displayName":  "Personal E2E",
			"slug":         fmt.Sprintf("pers-%d", time.Now().UnixMilli()),
			"maxNodeCount": 10,
		})
		Expect(status).To(Equal(200), "create workspace failed: %s", data.Msg)
		workspaceID = dataMap(data)["id"]

		var err error
		nc, err = nats.Connect(natsURL, nats.Timeout(5*time.Second))
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		if nc != nil {
			nc.Close()
		}
	})

	It("enrolls the home agent peer", func() {
		req, err := newRequest("POST", serverURL+"/api/v1/token/generate", nil, accessToken, workspaceID)
		Expect(err).NotTo(HaveOccurred())
		status, data := doAPI(req)
		Expect(status).To(Equal(200), "generate token failed: %s", data.Msg)
		joinToken = dataMap(data)["token"]
		Expect(joinToken).NotTo(BeEmpty())

		payload, _ := json.Marshal(dto.PeerDto{
			Name: "home-node", AppID: "home-node-1", Token: joinToken,
			Platform: "linux", Hostname: "home", PublicKey: "agent-pub-key",
		})
		raw, err := nc.Request("lattice.signals.peer.register", payload, 10*time.Second)
		Expect(err).NotTo(HaveOccurred(), "register request failed")

		// The control plane owns the agent keypair: the register response
		// carries the freshly generated public key, not the dto's hint.
		var node struct {
			Address   string `json:"address"`
			PublicKey string `json:"publicKey"`
		}
		Expect(json.Unmarshal(raw.Data, &node)).To(Succeed())
		Expect(node.Address).NotTo(BeEmpty())
		homePubKey = node.PublicKey
		Expect(homePubKey).NotTo(BeEmpty())
	})

	It("creates an external peer with a one-time wg-quick config", func() {
		status, data := apiPOST(serverURL+"/api/v1/peers/external", accessToken, map[string]any{
			"name":             "iPhone",
			"endpointOverride": "198.51.100.7:51820",
		}, header(workspaceID))
		Expect(status).To(Equal(200), "create external peer failed: %s", data.Msg)

		cfg := dataMap(data)["config"]
		Expect(cfg).To(ContainSubstring("[Interface]"))
		Expect(cfg).To(ContainSubstring("PrivateKey = "))
		Expect(cfg).To(ContainSubstring("Endpoint = 198.51.100.7:51820"))
		Expect(cfg).To(ContainSubstring("PublicKey = " + homePubKey))
	})

	It("lists the external peer with its type and deletes it", func() {
		status, data := apiGET(serverURL+"/api/v1/peers/list", accessToken, header(workspaceID))
		Expect(status).To(Equal(200))
		listJSON, _ := json.Marshal(data.Data)
		Expect(string(listJSON)).To(ContainSubstring(`"type":"external-wg"`))
		Expect(string(listJSON)).To(ContainSubstring("iPhone"))

		req, err := newRequest("DELETE", serverURL+"/api/v1/peers/iPhone", nil, accessToken, workspaceID)
		Expect(err).NotTo(HaveOccurred())
		status, data = doAPI(req)
		Expect(status).To(Equal(200), "delete failed: %s", data.Msg)

		status, data = apiGET(serverURL+"/api/v1/peers/list", accessToken, header(workspaceID))
		Expect(status).To(Equal(200))
		listJSON, _ = json.Marshal(data.Data)
		Expect(string(listJSON)).NotTo(ContainSubstring("iPhone"))
	})
})
```

- [ ] **Step 2: Run the suite**

Run: `go test ./test/e2e_standalone/... -v`
Expected: the suite builds `latticed`, boots it, and all specs PASS including the new Describe. If port 4222 is busy, stop stray latticed instances first (the suite fails fast with a message telling you so).

- [ ] **Step 3: Commit**

```bash
git add test/e2e_standalone/external_peer_specs_test.go
git commit -m "test(e2e): external peer enrollment over standalone REST"
```

---

## Final verification

- [ ] Full unit pass: `go build ./... && go test ./internal/server/...`
- [ ] E2E pass: `go test ./test/e2e_standalone/...`
- [ ] Grep guard — the private key must never be persisted: `grep -rn "PrivateKey" internal/server/service/external_peer.go internal/server/models/peer.go` returns only comments/none.
