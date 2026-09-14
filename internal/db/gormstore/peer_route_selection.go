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

package gormstore

import (
	"context"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type peerRouteSelectionRepo struct {
	db *gorm.DB
}

func newPeerRouteSelectionRepo(db *gorm.DB) *peerRouteSelectionRepo {
	return &peerRouteSelectionRepo{db: db}
}

func (r *peerRouteSelectionRepo) Create(ctx context.Context, m *models.PeerRouteSelection) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(m).Error
}

func (r *peerRouteSelectionRepo) Delete(ctx context.Context, workspaceID, consumerPeerID, providerPeerID string) error {
	return r.db.WithContext(ctx).Unscoped().
		Where("workspace_id = ? AND consumer_peer_id = ? AND provider_peer_id = ?", workspaceID, consumerPeerID, providerPeerID).
		Delete(&models.PeerRouteSelection{}).Error
}

func (r *peerRouteSelectionRepo) ListProviderIDsForConsumer(ctx context.Context, workspaceID, consumerPeerID string) ([]string, error) {
	var rows []models.PeerRouteSelection
	if err := r.db.WithContext(ctx).
		Where("workspace_id = ? AND consumer_peer_id = ?", workspaceID, consumerPeerID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ProviderPeerID
	}
	return ids, nil
}

var _ store.PeerRouteSelectionRepository = (*peerRouteSelectionRepo)(nil)
