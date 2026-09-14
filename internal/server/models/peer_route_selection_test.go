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

package models_test

import (
	"testing"

	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPeerRouteSelection_TableNameAndMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Peer{}, &models.PeerRouteSelection{}))

	require.NoError(t, db.Create(&models.Peer{
		Model:       models.Model{ID: "p1"},
		WorkspaceID: "ws1", Name: "gw", AdvertisedRoutes: `["0.0.0.0/0"]`,
	}).Error)

	var got models.Peer
	require.NoError(t, db.First(&got, "id = ?", "p1").Error)
	require.Equal(t, `["0.0.0.0/0"]`, got.AdvertisedRoutes)

	sel := &models.PeerRouteSelection{
		Model:          models.Model{ID: "sel1"},
		WorkspaceID:    "ws1",
		ConsumerPeerID: "consumer1",
		ProviderPeerID: "p1",
	}
	require.NoError(t, db.Create(sel).Error)

	var gotSel models.PeerRouteSelection
	require.NoError(t, db.First(&gotSel, "id = ?", "sel1").Error)
	require.Equal(t, "consumer1", gotSel.ConsumerPeerID)
	require.Equal(t, "t_peer_route_selection", models.PeerRouteSelection{}.TableName())
}
