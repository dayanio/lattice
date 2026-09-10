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

func policyKey(id string) reconcile.Request {
	return reconcile.Request{Kind: reconcilers.KindPolicy, Key: id}
}

func TestPolicyTTL_ExpiresActivePolicy(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ttl := reconcilers.NewPolicyTTL(st.Policies(), logr.Discard())

	p := &models.Policy{
		WorkspaceID: "ws1", Name: "temp-rule", Action: "ALLOW",
		Status: models.PolicyStatusActive, ExpiresAt: timePtr(time.Now().Add(-time.Minute)),
	}
	require.NoError(t, st.Policies().Create(ctx, p))

	res, err := ttl.Reconcile(ctx, policyKey(p.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter, "expired policy needs no requeue")

	got, err := st.Policies().GetByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusExpired, got.Status)
}

func TestPolicyTTL_RequeuesFutureExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ttl := reconcilers.NewPolicyTTL(st.Policies(), logr.Discard())

	p := &models.Policy{
		WorkspaceID: "ws1", Name: "temp-rule", Action: "ALLOW",
		Status: models.PolicyStatusActive, ExpiresAt: timePtr(time.Now().Add(time.Hour)),
	}
	require.NoError(t, st.Policies().Create(ctx, p))

	res, err := ttl.Reconcile(ctx, policyKey(p.ID))
	require.NoError(t, err)
	assert.Greater(t, res.RequeueAfter, time.Duration(0), "live TTL must requeue until expiry")

	got, err := st.Policies().GetByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusActive, got.Status, "status untouched while TTL is live")
}

func TestPolicyTTL_NoExpiryIsNoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ttl := reconcilers.NewPolicyTTL(st.Policies(), logr.Discard())

	p := &models.Policy{WorkspaceID: "ws1", Name: "forever", Status: models.PolicyStatusActive}
	require.NoError(t, st.Policies().Create(ctx, p))

	res, err := ttl.Reconcile(ctx, policyKey(p.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestPolicyTTL_NonActiveStatusIsNoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	ttl := reconcilers.NewPolicyTTL(st.Policies(), logr.Discard())

	p := &models.Policy{
		WorkspaceID: "ws1", Name: "pending-rule",
		Status: models.PolicyStatusPending, ExpiresAt: timePtr(time.Now().Add(-time.Hour)),
	}
	require.NoError(t, st.Policies().Create(ctx, p))

	res, err := ttl.Reconcile(ctx, policyKey(p.ID))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	got, err := st.Policies().GetByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusPending, got.Status, "only active policies expire")
}

func TestPolicyTTL_MissingRecordIsNoop(t *testing.T) {
	st := newTestStore(t)
	ttl := reconcilers.NewPolicyTTL(st.Policies(), logr.Discard())

	res, err := ttl.Reconcile(context.Background(), policyKey("does-not-exist"))
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}

func TestPolicyTTLKeyLister_ListsOnlyActiveWithExpiry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	expired := &models.Policy{WorkspaceID: "ws1", Name: "e", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(-time.Minute))}
	live := &models.Policy{WorkspaceID: "ws1", Name: "l", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(time.Hour))}
	noTTL := &models.Policy{WorkspaceID: "ws1", Name: "n", Status: models.PolicyStatusActive}
	for _, p := range []*models.Policy{expired, live, noTTL} {
		require.NoError(t, st.Policies().Create(ctx, p))
	}

	lister := reconcilers.NewPolicyTTLKeyLister(st.Policies())
	keys, err := lister.ListKeys(ctx)
	require.NoError(t, err)
	assert.Len(t, keys, 2, "only active policies carrying a TTL are enumerated")
	for _, k := range keys {
		assert.Equal(t, reconcilers.KindPolicy, k.Kind)
	}
}
