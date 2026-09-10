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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/glebarez/sqlite"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // single shared in-memory database
	require.NoError(t, db.AutoMigrate(&models.AgentIdentity{}, &models.PeerIdentity{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return st
}

func timePtr(t time.Time) *time.Time { return &t }

func agentIdentityKey(id string) reconcile.Request {
	return reconcile.Request{Kind: reconcilers.KindAgentIdentity, Key: id}
}

func TestAgentIdentityGC_ExpiresPendingIdentity(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewAgentIdentityGC(st.AgentIdentities(), logr.Discard())

	m := &models.AgentIdentity{
		TenantID: "t1", Name: "agent-1", PeerRef: "p1",
		Phase: "Pending", ExpiresAt: timePtr(time.Now().Add(-time.Minute)),
	}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, agentIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "terminal identity needs no requeue")

	got, err := st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Expired", got.Phase)
}

func TestAgentIdentityGC_ActivatesPendingWithoutTTL(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewAgentIdentityGC(st.AgentIdentities(), logr.Discard())

	m := &models.AgentIdentity{TenantID: "t1", Name: "agent-1", PeerRef: "p1", Phase: "Pending"}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, agentIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	got, err := st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Active", got.Phase)
}

func TestAgentIdentityGC_ActivatesAndRequeuesFutureTTL(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewAgentIdentityGC(st.AgentIdentities(), logr.Discard())

	m := &models.AgentIdentity{
		TenantID: "t1", Name: "agent-1", PeerRef: "p1",
		Phase: "Pending", ExpiresAt: timePtr(time.Now().Add(time.Hour)),
	}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, agentIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0), "future TTL must schedule the expiry check")

	got, err := st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Active", got.Phase)
}

func TestAgentIdentityGC_TerminalPhaseIsNoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	gc := reconcilers.NewAgentIdentityGC(st.AgentIdentities(), logr.Discard())

	m := &models.AgentIdentity{
		TenantID: "t1", Name: "agent-1", PeerRef: "p1",
		Phase: "Revoked", ExpiresAt: timePtr(time.Now().Add(-time.Hour)),
	}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))

	res, err := gc.Reconcile(ctx, agentIdentityKey(m.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	got, err := st.AgentIdentities().GetByID(ctx, m.ID)
	require.NoError(t, err)
	assert.Equal(t, "Revoked", got.Phase, "terminal phase must stay untouched")
}

func TestAgentIdentityGC_MissingRecordIsNoop(t *testing.T) {
	st := newTestStore(t)
	gc := reconcilers.NewAgentIdentityGC(st.AgentIdentities(), logr.Discard())

	res, err := gc.Reconcile(context.Background(), agentIdentityKey("does-not-exist"))
	require.NoError(t, err, "deleted records must not error the reconcile loop")
	assert.Zero(t, res.RequeueAfter)
}

func TestAgentIdentityKeyLister_ReturnsRequests(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	for _, name := range []string{"a1", "a2"} {
		require.NoError(t, st.AgentIdentities().Create(ctx, &models.AgentIdentity{
			TenantID: "t1", Name: name, PeerRef: "p", Phase: "Pending",
		}))
	}

	lister := reconcilers.NewAgentIdentityKeyLister(st.AgentIdentities())
	keys, err := lister.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	for _, k := range keys {
		assert.Equal(t, reconcilers.KindAgentIdentity, k.Kind)
		assert.NotEmpty(t, k.Key)
	}
}
