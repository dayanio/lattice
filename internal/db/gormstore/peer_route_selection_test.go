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

// TestPeerRouteSelectionRepo_ReselectAfterDeselect reproduces the bug where
// GORM's default soft delete left a tombstoned row occupying idx_route_sel,
// causing a second Create for the same (workspace, consumer, provider) to
// fail with a UNIQUE constraint error. Delete must hard-delete so the pair
// can be selected again.
func TestPeerRouteSelectionRepo_ReselectAfterDeselect(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel1"}, WorkspaceID: "ws1",
		ConsumerPeerID: "mac", ProviderPeerID: "gw1",
	}))

	ids, err := st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw1"}, ids)

	// Deselect.
	require.NoError(t, st.RouteSelections().Delete(ctx, "ws1", "mac", "gw1"))
	ids, err = st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.Empty(t, ids)

	// Re-select the same pair — must succeed, not hit a UNIQUE constraint
	// error against a soft-deleted tombstone row.
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel2"}, WorkspaceID: "ws1",
		ConsumerPeerID: "mac", ProviderPeerID: "gw1",
	}))
	ids, err = st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw1"}, ids, "must show selected again after re-selecting")

	// Selecting an already-selected pair a second time (no deselect in
	// between) must be a no-op, not an error, and must not duplicate the row.
	require.NoError(t, st.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		Model: models.Model{ID: "sel3"}, WorkspaceID: "ws1",
		ConsumerPeerID: "mac", ProviderPeerID: "gw1",
	}))
	ids, err = st.RouteSelections().ListProviderIDsForConsumer(ctx, "ws1", "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw1"}, ids, "duplicate select must not add a second row")
}
