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

package reconcilers_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetmapBuilder_BuildsFullMessage(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "a1", Token: "tk1",
		Address: "10.96.0.2", Platform: "linux", PublicKey: "k1",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "db", AppID: "a2", Token: "tk2",
		Address: "10.96.0.3", Platform: "linux", PublicKey: "k2",
	}))
	spec := dto.PolicySpec{
		Network: "ws1",
		Egress: []dto.EgressRule{{
			To:    []dto.PeerSelection{{IdentityRef: "prod-db"}},
			Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
		}},
	}
	specRaw, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NoError(t, st.Policies().Create(ctx, &models.Policy{
		WorkspaceID: "ws1", Name: "api-to-db", Action: "ALLOW",
		Status: models.PolicyStatusActive, Spec: string(specRaw),
	}))
	require.NoError(t, st.PeerIdentities().Create(ctx, &models.PeerIdentity{
		NetworkID: "ws1", Name: "prod-db", PeerRef: "db", ResolvedPeerIP: "10.96.0.3",
	}))

	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	msg, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)
	require.NotNil(t, msg)

	// Current peer
	assert.Equal(t, "api", msg.Current.Name)
	require.NotNil(t, msg.Current.Address)
	assert.Equal(t, "10.96.0.2", *msg.Current.Address)

	// Network peers sorted by name, both present
	require.Len(t, msg.Network.Peers, 2)
	assert.Equal(t, "api", msg.Network.Peers[0].Name)
	assert.Equal(t, "db", msg.Network.Peers[1].Name)
	assert.Equal(t, "ws1", msg.Network.NetworkId)

	// Policies carried for information
	assert.Len(t, msg.Policies, 1)

	// ComputedRules: the egress rule resolves the identity to db's IP,
	// plus a default-deny tail.
	var egressIPs []string
	hasTail := false
	for _, tr := range msg.ComputedRules.Egress {
		if len(tr.Peers) == 0 {
			hasTail = true
			continue
		}
		egressIPs = append(egressIPs, tr.Peers...)
	}
	assert.Contains(t, egressIPs, "10.96.0.3")
	assert.True(t, hasTail, "default-deny tail must be present")

	// ComputedPeers: the other peers to connect to.
	require.Len(t, msg.ComputedPeers, 1)
	assert.Equal(t, "db", msg.ComputedPeers[0].Name)

	// ConfigVersion is stable for identical content.
	msg2, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)
	assert.Equal(t, msg.ConfigVersion, msg2.ConfigVersion, "same content must yield the same version")
}

func TestNetmapBuilder_VersionChangesWithContent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "a1", Token: "tk1", Address: "10.96.0.2",
	}))
	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())

	msg1, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)

	spec := dto.PolicySpec{Network: "ws1"}
	specRaw, _ := json.Marshal(spec)
	require.NoError(t, st.Policies().Create(ctx, &models.Policy{
		WorkspaceID: "ws1", Name: "p1", Action: "ALLOW",
		Status: models.PolicyStatusActive, Spec: string(specRaw),
	}))

	msg2, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)
	assert.NotEqual(t, msg1.ConfigVersion, msg2.ConfigVersion,
		"adding a policy must change the config version")
}

func TestNetmapBuilder_ExcludesExpiredPolicy(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "a1", Token: "tk1", Address: "10.96.0.2",
	}))
	spec := dto.PolicySpec{Network: "ws1"}
	specRaw, _ := json.Marshal(spec)
	expired := time.Now().Add(-time.Hour)
	require.NoError(t, st.Policies().Create(ctx, &models.Policy{
		WorkspaceID: "ws1", Name: "old", Action: "ALLOW",
		Status: models.PolicyStatusActive, Spec: string(specRaw), ExpiresAt: &expired,
	}))

	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	msg, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)
	assert.Empty(t, msg.Policies, "expired-TTL policies must not be distributed")
}

func TestNetmapBuilder_SkipsPeersWithoutAddress(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "a1", Token: "tk1", Address: "10.96.0.2",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "enrolling", AppID: "a2", Token: "tk2",
	}))

	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	msg, err := builder.BuildForAppID(ctx, "a1", "tk1")
	require.NoError(t, err)
	assert.Len(t, msg.Network.Peers, 1, "peers without an overlay IP are not part of the mesh yet")
}

func TestNetmapBuilder_WrongTokenRejected(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "a1", Token: "tk1", Address: "10.96.0.2",
	}))
	builder := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())

	_, err := builder.BuildForAppID(ctx, "a1", "wrong-token")
	assert.Error(t, err, "netmap must only be served to the peer matching the token")
}

func TestAllocateAddress(t *testing.T) {
	free, err := reconcilers.AllocateAddress(nil)
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.2", free, "allocation starts at the first host address")

	free, err = reconcilers.AllocateAddress([]string{"10.96.0.2", "10.96.0.3"})
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.4", free, "lowest free address wins")
}

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

// TestNetmapBuilder_SkipsMalformedAdvertisedRoute is defense-in-depth for
// rows written before CIDR validation existed on SetAdvertisedRoutes (or
// written directly to the DB by some other path): a malformed entry must be
// dropped rather than propagated into AllowedIPs, while a valid entry in the
// same declaration still expands normally.
func TestNetmapBuilder_SkipsMalformedAdvertisedRoute(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer1"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Token: "tk-mac", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider1"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Token: "tk-gw", Address: "10.96.0.4", PublicKey: "kgw",
		AdvertisedRoutes: `["192.168.1.0/24","not-a-cidr"]`,
	}))
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
	assert.Equal(t, "10.96.0.4/32,192.168.1.0/24", gwForMac.AllowedIPs,
		"only the valid CIDR must be expanded; the malformed entry is dropped")
}

// TestNetmapBuilder_FullyMalformedAdvertisedRouteFallsBackToSlash32 covers
// the case where every declared route is malformed: the result must match
// the plain-/32 behavior already exercised for an empty declaration.
func TestNetmapBuilder_FullyMalformedAdvertisedRouteFallsBackToSlash32(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer1"}, WorkspaceID: "ws1", Name: "mac",
		AppID: "mac-app", Token: "tk-mac", Address: "10.96.0.2", PublicKey: "kmac",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider1"}, WorkspaceID: "ws1", Name: "gw",
		AppID: "gw-app", Token: "tk-gw", Address: "10.96.0.4", PublicKey: "kgw",
		AdvertisedRoutes: `["not-a-cidr"]`,
	}))
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
	assert.Equal(t, "10.96.0.4/32", gwForMac.AllowedIPs)
}
