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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func timePtr(t time.Time) *time.Time { return &t }

func newPolicyStore(t *testing.T) store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Policy{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return st
}

func TestPolicy_UpdateStatus(t *testing.T) {
	st := newPolicyStore(t)
	ctx := context.Background()

	p := &models.Policy{WorkspaceID: "ws1", Name: "allow-db", Action: "ALLOW", Status: models.PolicyStatusActive}
	require.NoError(t, st.Policies().Create(ctx, p))

	require.NoError(t, st.Policies().UpdateStatus(ctx, p.ID, models.PolicyStatusExpired))
	got, err := st.Policies().GetByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusExpired, got.Status)
}

func TestPolicy_ListExpiringActiveIDs(t *testing.T) {
	st := newPolicyStore(t)
	ctx := context.Background()
	now := time.Now()

	expired := &models.Policy{WorkspaceID: "ws1", Name: "expired-ttl", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(-time.Minute))}
	live := &models.Policy{WorkspaceID: "ws1", Name: "future-ttl", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(time.Hour))}
	noTTL := &models.Policy{WorkspaceID: "ws1", Name: "no-ttl", Status: models.PolicyStatusActive}
	alreadyExpiredStatus := &models.Policy{WorkspaceID: "ws1", Name: "expired-status", Status: models.PolicyStatusExpired, ExpiresAt: timePtr(now.Add(-time.Hour))}
	for _, p := range []*models.Policy{expired, live, noTTL, alreadyExpiredStatus} {
		require.NoError(t, st.Policies().Create(ctx, p))
	}

	ids, err := st.Policies().ListIDsActiveWithExpiry(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{expired.ID, live.ID}, ids,
		"only active policies carrying a TTL are listed")
}

func TestPolicy_ListActiveByWorkspace_TTLFilter(t *testing.T) {
	st := newPolicyStore(t)
	ctx := context.Background()
	now := time.Now()

	expired := &models.Policy{WorkspaceID: "ws1", Name: "expired-ttl", Action: "ALLOW", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(-time.Minute))}
	live := &models.Policy{WorkspaceID: "ws1", Name: "future-ttl", Action: "ALLOW", Status: models.PolicyStatusActive, ExpiresAt: timePtr(now.Add(time.Hour))}
	noTTL := &models.Policy{WorkspaceID: "ws1", Name: "no-ttl", Action: "ALLOW", Status: models.PolicyStatusActive}
	otherWS := &models.Policy{WorkspaceID: "ws2", Name: "other", Action: "ALLOW", Status: models.PolicyStatusActive}
	for _, p := range []*models.Policy{expired, live, noTTL, otherWS} {
		require.NoError(t, st.Policies().Create(ctx, p))
	}

	rows, err := st.Policies().ListActiveByWorkspace(ctx, "ws1")
	require.NoError(t, err)
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name)
	}
	assert.ElementsMatch(t, []string{"future-ttl", "no-ttl"}, names,
		"expired-TTL policies must not be distributed; other workspaces excluded")
}
