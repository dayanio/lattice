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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPeerStore(t *testing.T) store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Peer{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return st
}

func TestPeer_CreateAndGetByAppID(t *testing.T) {
	st := newPeerStore(t)
	ctx := context.Background()

	p := &models.Peer{
		WorkspaceID: "ws1", Name: "api", AppID: "app-1", Token: "tok-1",
		Address: "10.96.0.2", Endpoint: "1.2.3.4:51820", Platform: "linux",
		PublicKey: "pubkey-1",
	}
	require.NoError(t, st.Peers().Create(ctx, p))

	got, err := st.Peers().GetByAppID(ctx, "app-1")
	require.NoError(t, err)
	assert.Equal(t, "api", got.Name)
	assert.Equal(t, "ws1", got.WorkspaceID)
	assert.Equal(t, "10.96.0.2", got.Address)
	assert.Equal(t, "tok-1", got.Token)
}

func TestPeer_GetByAppID_MissingIsNotFound(t *testing.T) {
	st := newPeerStore(t)
	_, err := st.Peers().GetByAppID(context.Background(), "ghost")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestPeer_ListByWorkspace(t *testing.T) {
	st := newPeerStore(t)
	ctx := context.Background()

	for _, p := range []*models.Peer{
		{WorkspaceID: "ws1", Name: "api", AppID: "a1", Address: "10.96.0.2"},
		{WorkspaceID: "ws1", Name: "db", AppID: "a2", Address: "10.96.0.3"},
		{WorkspaceID: "ws2", Name: "other", AppID: "a3", Address: "10.96.0.4"},
	} {
		require.NoError(t, st.Peers().Create(ctx, p))
	}

	rows, err := st.Peers().ListByWorkspace(ctx, "ws1")
	require.NoError(t, err)
	assert.Len(t, rows, 2, "only the requested workspace's peers are listed")
}

func TestPeer_Update(t *testing.T) {
	st := newPeerStore(t)
	ctx := context.Background()

	p := &models.Peer{WorkspaceID: "ws1", Name: "api", AppID: "a1", Address: "10.96.0.2"}
	require.NoError(t, st.Peers().Create(ctx, p))

	p.Endpoint = "5.6.7.8:51820"
	p.Disabled = true
	require.NoError(t, st.Peers().Update(ctx, p))

	got, err := st.Peers().GetByAppID(ctx, "a1")
	require.NoError(t, err)
	assert.Equal(t, "5.6.7.8:51820", got.Endpoint)
	assert.True(t, got.Disabled)
}
