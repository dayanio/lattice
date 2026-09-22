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

	"github.com/alatticeio/lattice/internal/server/models"
	"gorm.io/gorm"
)

type publishRepo struct {
	db *gorm.DB
}

func newPublishRepo(db *gorm.DB) *publishRepo {
	return &publishRepo{db: db}
}

func (r *publishRepo) ListByWorkspace(ctx context.Context, workspaceID string) ([]*models.Publish, error) {
	var rows []*models.Publish
	err := r.db.WithContext(ctx).Where("workspace_id = ?", workspaceID).Order("name").Find(&rows).Error
	return rows, err
}

func (r *publishRepo) GetByName(ctx context.Context, workspaceID, name string) (*models.Publish, error) {
	var m models.Publish
	if err := r.db.WithContext(ctx).Where("workspace_id = ? AND name = ?", workspaceID, name).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *publishRepo) Create(ctx context.Context, p *models.Publish) error {
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *publishRepo) Delete(ctx context.Context, workspaceID, name string) error {
	return r.db.WithContext(ctx).
		Where("workspace_id = ? AND name = ?", workspaceID, name).
		Delete(&models.Publish{}).Error
}
