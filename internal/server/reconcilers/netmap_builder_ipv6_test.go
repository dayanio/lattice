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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedExitMesh creates a consumer that selected an exit provider advertising
// the IPv4 default route, and returns the consumer.
func seedExitMesh(t *testing.T, st store.Store) *models.Peer {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "consumer1"}, WorkspaceID: "ws1", Name: "phone",
		AppID: "phone-app", Token: "tk-phone", Address: "10.96.0.2", PublicKey: "kphone",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider1"}, WorkspaceID: "ws1", Name: "exit",
		AppID: "exit-app", Token: "tk-exit", Address: "10.96.0.4", PublicKey: "kexit",
		AdvertisedRoutes: `["0.0.0.0/0"]`,
	}))
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel1"}, WorkspaceID: "ws1",
		ConsumerPeerID: "consumer1", ProviderPeerID: "provider1",
	}))
	consumer, err := st.Peers().GetByID(ctx, "consumer1")
	require.NoError(t, err)
	return consumer
}

func allowedIPsOf(t *testing.T, b *reconcilers.NetmapBuilder, consumer *models.Peer, name string) string {
	t.Helper()
	msg, err := b.BuildForPeer(context.Background(), consumer)
	require.NoError(t, err)
	for _, p := range msg.Network.Peers {
		if p.Name == name {
			return p.AllowedIPs
		}
	}
	t.Fatalf("peer %q not in the netmap", name)
	return ""
}

// With the switch off (the default) the netmap is exactly what a build without
// IPv6 support produces — old clients depend on that.
func TestNetmapBuilder_IPv6OffIsByteIdentical(t *testing.T) {
	st := newTestStore(t)
	consumer := seedExitMesh(t, st)
	ctx := context.Background()

	plain := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	off := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	// Even a provider that reports an IPv6 egress must not matter while off.
	off.SetIPv6(false, func(string) bool { return true })

	a, err := plain.BuildForPeer(ctx, consumer)
	require.NoError(t, err)
	b, err := off.BuildForPeer(ctx, consumer)
	require.NoError(t, err)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	assert.JSONEq(t, string(ja), string(jb))
	assert.Equal(t, "10.96.0.4/32,0.0.0.0/0", allowedIPsOf(t, off, consumer, "exit"))
}

func TestNetmapBuilder_IPv6OnAddsHostRoutes(t *testing.T) {
	st := newTestStore(t)
	consumer := seedExitMesh(t, st)
	b := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	b.SetIPv6(true, func(string) bool { return false })

	// Every peer, including the consumer itself, gets its derived /128.
	assert.Equal(t, "10.96.0.2/32,fd6c:7270:6c74::a60:2/128", allowedIPsOf(t, b, consumer, "phone"))
	// The provider has no IPv6 egress: IPv4 default only, no "::/0".
	assert.Equal(t, "10.96.0.4/32,fd6c:7270:6c74::a60:4/128,0.0.0.0/0", allowedIPsOf(t, b, consumer, "exit"))

	msg, err := b.BuildForPeer(context.Background(), consumer)
	require.NoError(t, err)
	assert.Equal(t, "10.96.0.2/32,fd6c:7270:6c74::a60:2/128", msg.Current.AllowedIPs, "Current carries its own /128 too")
}

func TestNetmapBuilder_IPv6DefaultRouteFollowsProviderEgress(t *testing.T) {
	st := newTestStore(t)
	consumer := seedExitMesh(t, st)
	egress := map[string]bool{}
	b := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	b.SetIPv6(true, func(appID string) bool { return egress[appID] })

	const dual = "10.96.0.4/32,fd6c:7270:6c74::a60:4/128,0.0.0.0/0"
	assert.Equal(t, dual, allowedIPsOf(t, b, consumer, "exit"))

	egress["exit-app"] = true
	assert.Equal(t, dual+",::/0", allowedIPsOf(t, b, consumer, "exit"), "egress reported: consumer also gets ::/0")

	egress["exit-app"] = false
	assert.Equal(t, dual, allowedIPsOf(t, b, consumer, "exit"), "egress withdrawn: ::/0 goes away")
}

// The IPv6 default route is only ever an addition to an advertised IPv4 default
// route, never a route on its own, and only for consumers that selected the
// provider.
func TestNetmapBuilder_IPv6DefaultRouteOnlyWithIPv4DefaultAndSelection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	consumer := seedExitMesh(t, st)
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "provider2"}, WorkspaceID: "ws1", Name: "subnet-gw",
		AppID: "gw-app", Token: "tk-gw", Address: "10.96.0.5", PublicKey: "kgw",
		AdvertisedRoutes: `["192.168.1.0/24"]`,
	}))
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel2"}, WorkspaceID: "ws1",
		ConsumerPeerID: "consumer1", ProviderPeerID: "provider2",
	}))
	b := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	b.SetIPv6(true, func(string) bool { return true })

	assert.Equal(t, "10.96.0.5/32,fd6c:7270:6c74::a60:5/128,192.168.1.0/24", allowedIPsOf(t, b, consumer, "subnet-gw"),
		"a subnet router never gets ::/0")

	other := &models.Peer{
		Model: models.Model{ID: "consumer2"}, WorkspaceID: "ws1", Name: "laptop",
		AppID: "laptop-app", Token: "tk-laptop", Address: "10.96.0.3", PublicKey: "klaptop",
	}
	require.NoError(t, st.Peers().Create(ctx, other))
	assert.Equal(t, "10.96.0.4/32,fd6c:7270:6c74::a60:4/128", allowedIPsOf(t, b, other, "exit"),
		"a consumer that did not select the provider gets no default routes at all")
}

// An offline provider still has its routes withheld, ::/0 included.
func TestNetmapBuilder_IPv6DefaultRouteWithheldWhenProviderOffline(t *testing.T) {
	st := newTestStore(t)
	consumer := seedExitMesh(t, st)
	online := true
	b := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	b.SetProviderLiveness(func(string) bool { return online })
	b.SetIPv6(true, func(string) bool { return true })

	assert.Contains(t, allowedIPsOf(t, b, consumer, "exit"), "::/0")
	online = false
	assert.Equal(t, "10.96.0.4/32,fd6c:7270:6c74::a60:4/128", allowedIPsOf(t, b, consumer, "exit"))
}

// Enabling IPv6 must change the config version so agents re-apply the netmap.
func TestNetmapBuilder_IPv6ChangesConfigVersion(t *testing.T) {
	st := newTestStore(t)
	consumer := seedExitMesh(t, st)
	b := reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	before, err := b.BuildForPeer(context.Background(), consumer)
	require.NoError(t, err)
	b.SetIPv6(true, nil)
	after, err := b.BuildForPeer(context.Background(), consumer)
	require.NoError(t, err)
	assert.NotEqual(t, before.ConfigVersion, after.ConfigVersion)
}
