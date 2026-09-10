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
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func peerIdentityKey(id string) reconcile.Request {
	return reconcile.Request{Kind: reconcilers.KindPeerIdentity, Key: id}
}

func TestPeerIdentityGrace_ClearsExpiredGrace(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewPeerIdentityGrace(st.PeerIdentities(), logr.Discard())

	m := &models.PeerIdentity{
		NetworkID:            "net-1",
		Name:                 "prod-db",
		PeerRef:              "node-v2",
		PreviousPeerRef:      "node-v1",
		PreviousPeerIP:       "10.0.0.4",
		GracePeriodSeconds:   300,
		GracePeriodExpiresAt: timePtr(time.Now().Add(-time.Minute)),
	}
	require.NoError(t, st.PeerIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, peerIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "cleared grace period needs no requeue")

	got, err := st.PeerIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, got.PreviousPeerRef)
	assert.Empty(t, got.PreviousPeerIP)
	assert.Nil(t, got.GracePeriodExpiresAt)
	assert.Equal(t, "node-v2", got.PeerRef, "current binding must be untouched")
}

func TestPeerIdentityGrace_RequeuesFutureGrace(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewPeerIdentityGrace(st.PeerIdentities(), logr.Discard())

	m := &models.PeerIdentity{
		NetworkID:            "net-1",
		Name:                 "prod-db",
		PeerRef:              "node-v2",
		PreviousPeerRef:      "node-v1",
		GracePeriodSeconds:   300,
		GracePeriodExpiresAt: timePtr(time.Now().Add(time.Hour)),
	}
	require.NoError(t, st.PeerIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, peerIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0), "active grace period must requeue until expiry")

	got, err := st.PeerIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "node-v1", got.PreviousPeerRef, "grace period must stay intact while active")
}

func TestPeerIdentityGrace_NoGraceIsNoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewPeerIdentityGrace(st.PeerIdentities(), logr.Discard())

	m := &models.PeerIdentity{NetworkID: "net-1", Name: "prod-db", PeerRef: "node-1"}
	require.NoError(t, st.PeerIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, peerIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestPeerIdentityGrace_MissingRecordIsNoop(t *testing.T) {
	st := newTestStore(t)
	gc := reconcilers.NewPeerIdentityGrace(st.PeerIdentities(), logr.Discard())

	res, err := gc.Reconcile(context.Background(), peerIdentityKey("does-not-exist"))
	require.NoError(t, err, "deleted records must not error the reconcile loop")
	assert.Zero(t, res.RequeueAfter)
}

func TestPeerIdentityKeyLister_ReturnsRequests(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"prod-db", "api-gateway"} {
		require.NoError(t, st.PeerIdentities().Create(ctx, &models.PeerIdentity{
			NetworkID: "net-1", Name: name, PeerRef: "node-1",
		}))
	}

	lister := reconcilers.NewPeerIdentityKeyLister(st.PeerIdentities())
	keys, err := lister.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, k := range keys {
		assert.Equal(t, reconcilers.KindPeerIdentity, k.Kind)
		assert.NotEmpty(t, k.Key)
	}
}
