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
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPolicyVersion_CreateAndListOrdered(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.PolicyVersion{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()

	for i, action := range []string{"created", "applied", "applied"} {
		require.NoError(t, st.PolicyVersions().Create(ctx, &models.PolicyVersion{
			PolicyID: "p1", Version: i + 1, Action: action, Spec: "{}", Intent: "描述",
		}))
	}

	rows, err := st.PolicyVersions().ListByPolicyID(ctx, "p1")
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, 3, rows[0].Version, "newest version first")
	assert.Equal(t, "created", rows[2].Action)
	assert.Equal(t, "描述", rows[2].Intent)
}

func TestFlowEvent_SumByAgents(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.FlowEvent{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Now()

	for _, e := range []models.FlowEvent{
		{AgentID: "app-a", Bytes: 100, Ts: now.Add(-time.Minute)},
		{AgentID: "app-a", Bytes: 50, Ts: now.Add(-2 * time.Minute)},
		{AgentID: "app-b", Bytes: 10, Ts: now.Add(-3 * time.Hour)}, // outside window
	} {
		require.NoError(t, st.FlowEvents().Write(ctx, &e))
	}

	count, bytes, err := st.FlowEvents().SumByAgents(ctx, []string{"app-a"}, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2, count)
	assert.EqualValues(t, 150, bytes)
}
