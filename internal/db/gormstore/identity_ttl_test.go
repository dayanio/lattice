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
	"time"

	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentIdentity_ListIDs(t *testing.T) {
	db := newAgentIdentityDB(t)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	for _, name := range []string{"agent-1", "agent-2"} {
		require.NoError(t, st.AgentIdentities().Create(ctx, &models.AgentIdentity{
			TenantID: "t1", Name: name, PeerRef: "p-" + name, Phase: "Pending",
		}))
	}

	ids, err := st.AgentIdentities().ListIDs(ctx)
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

func TestAgentIdentity_UpdatePhase(t *testing.T) {
	db := newAgentIdentityDB(t)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	m := &models.AgentIdentity{TenantID: "t1", Name: "agent-1", PeerRef: "p1", Phase: "Pending"}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))

	require.NoError(t, st.AgentIdentities().UpdatePhase(ctx, m.ID, "Active"))
	got, err := st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Active", got.Phase)

	require.NoError(t, st.AgentIdentities().UpdatePhase(ctx, m.ID, "Expired"))
	got, err = st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Expired", got.Phase)
}

func TestPeerIdentity_ListIDs(t *testing.T) {
	db := newPeerIdentityDB(t)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	for _, name := range []string{"prod-db", "api-gateway"} {
		require.NoError(t, st.PeerIdentities().Create(ctx, &models.PeerIdentity{
			NetworkID: "net-1", Name: name, PeerRef: "node-" + name,
		}))
	}

	ids, err := st.PeerIdentities().ListIDs(ctx)
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

func TestPeerIdentity_ClearGracePeriod(t *testing.T) {
	db := newPeerIdentityDB(t)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	expiry := time.Now().Add(-time.Minute)
	m := &models.PeerIdentity{
		NetworkID:            "net-1",
		Name:                 "prod-db",
		PeerRef:              "node-v2",
		PreviousPeerRef:      "node-v1",
		PreviousPeerIP:       "10.0.0.4",
		GracePeriodSeconds:   300,
		GracePeriodExpiresAt: &expiry,
	}
	require.NoError(t, st.PeerIdentities().Create(ctx, m))

	require.NoError(t, st.PeerIdentities().ClearGracePeriod(ctx, m.ID))

	got, err := st.PeerIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Empty(t, got.PreviousPeerRef, "previous_peer_ref must be cleared")
	assert.Empty(t, got.PreviousPeerIP, "previous_peer_ip must be cleared")
	assert.Nil(t, got.GracePeriodExpiresAt, "grace_period_expires_at must be cleared")
	assert.Equal(t, "node-v2", got.PeerRef, "current peer binding must be untouched")
}
