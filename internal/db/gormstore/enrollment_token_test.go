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

func newEnrollmentTokenStore(t *testing.T) store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.EnrollmentToken{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return st
}

func TestEnrollmentToken_CreateAndGetByToken(t *testing.T) {
	st := newEnrollmentTokenStore(t)
	ctx := context.Background()

	tok := &models.EnrollmentToken{
		Token:       "enr-abc123",
		WorkspaceID: "ws1",
		ExpiresAt:   time.Now().Add(time.Hour),
		UsageLimit:  5,
		CreatedBy:   "admin-1",
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, tok))

	got, err := st.EnrollmentTokens().GetByToken(ctx, "enr-abc123")
	require.NoError(t, err)
	assert.Equal(t, "ws1", got.WorkspaceID)
	assert.Equal(t, 5, got.UsageLimit)
	assert.Zero(t, got.UsedCount)
}

func TestEnrollmentToken_IncrementUsedCount(t *testing.T) {
	st := newEnrollmentTokenStore(t)
	ctx := context.Background()

	tok := &models.EnrollmentToken{Token: "enr-xyz", WorkspaceID: "ws1", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, tok))

	require.NoError(t, st.EnrollmentTokens().IncrementUsedCount(ctx, tok.ID))
	require.NoError(t, st.EnrollmentTokens().IncrementUsedCount(ctx, tok.ID))

	got, err := st.EnrollmentTokens().GetByToken(ctx, "enr-xyz")
	require.NoError(t, err)
	assert.Equal(t, 2, got.UsedCount)
}
