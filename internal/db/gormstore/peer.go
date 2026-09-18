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
)

type peerRepo struct {
	db *gorm.DB
}

func newPeerRepo(db *gorm.DB) *peerRepo {
	return &peerRepo{db: db}
}

func (r *peerRepo) Create(ctx context.Context, m *models.Peer) error {
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *peerRepo) GetByID(ctx context.Context, id string) (*models.Peer, error) {
	var m models.Peer
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *peerRepo) GetByAppID(ctx context.Context, appID string) (*models.Peer, error) {
	var m models.Peer
	if err := r.db.WithContext(ctx).Where("app_id = ?", appID).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *peerRepo) ListByWorkspace(ctx context.Context, workspaceID string) ([]*models.Peer, error) {
	var rows []*models.Peer
	err := r.db.WithContext(ctx).Where("workspace_id = ?", workspaceID).Find(&rows).Error
	return rows, err
}

func (r *peerRepo) Update(ctx context.Context, m *models.Peer) error {
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *peerRepo) Delete(ctx context.Context, id string) error {
	// Hard delete, not gorm soft delete: (workspace_id, name) carries a
	// UNIQUE index, and a soft-deleted row keeps occupying it — a deleted
	// peer's name could then never be re-enrolled (UNIQUE constraint
	// failed on re-registration). Deleted peer rows have no readers.
	return r.db.Unscoped().WithContext(ctx).Where("id = ?", id).Delete(&models.Peer{}).Error
}

// CountAll counts every registered peer across workspaces.
func (r *peerRepo) CountAll(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&models.Peer{}).Count(&n).Error
	return n, err
}

var _ store.PeerRepository = (*peerRepo)(nil)
