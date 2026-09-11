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

type policyVersionRepo struct {
	db *gorm.DB
}

func newPolicyVersionRepo(db *gorm.DB) *policyVersionRepo {
	return &policyVersionRepo{db: db}
}

func (r *policyVersionRepo) Create(ctx context.Context, v *models.PolicyVersion) error {
	return r.db.WithContext(ctx).Create(v).Error
}

func (r *policyVersionRepo) ListByPolicyID(ctx context.Context, policyID string) ([]*models.PolicyVersion, error) {
	var rows []*models.PolicyVersion
	err := r.db.WithContext(ctx).
		Where("policy_id = ?", policyID).
		Order("version DESC").
		Find(&rows).Error
	return rows, err
}

var _ store.PolicyVersionRepository = (*policyVersionRepo)(nil)
