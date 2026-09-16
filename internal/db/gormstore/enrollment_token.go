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

type enrollmentTokenRepo struct {
	db *gorm.DB
}

func newEnrollmentTokenRepo(db *gorm.DB) *enrollmentTokenRepo {
	return &enrollmentTokenRepo{db: db}
}

func (r *enrollmentTokenRepo) Create(ctx context.Context, token *models.EnrollmentToken) error {
	return r.db.WithContext(ctx).Create(token).Error
}

func (r *enrollmentTokenRepo) GetByToken(ctx context.Context, token string) (*models.EnrollmentToken, error) {
	var m models.EnrollmentToken
	if err := r.db.WithContext(ctx).Where("token = ?", token).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// IncrementUsedCount atomically bumps the usage counter so concurrent
// enrollments cannot exceed the token's usage limit.
func (r *enrollmentTokenRepo) IncrementUsedCount(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Model(&models.EnrollmentToken{}).
		Where("id = ?", id).
		UpdateColumn("used_count", gorm.Expr("used_count + 1")).Error
}

func (r *enrollmentTokenRepo) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.EnrollmentToken{}).Error
}

var _ store.EnrollmentTokenRepository = (*enrollmentTokenRepo)(nil)
