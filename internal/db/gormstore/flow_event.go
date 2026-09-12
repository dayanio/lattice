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
	"time"

	"github.com/alatticeio/lattice/internal/server/models"
	"gorm.io/gorm"
)

type flowEventRepo struct{ db *gorm.DB }

func NewFlowEventRepo(db *gorm.DB) *flowEventRepo {
	return &flowEventRepo{db: db}
}

func (r *flowEventRepo) Write(ctx context.Context, e *models.FlowEvent) error {
	return r.db.WithContext(ctx).Create(e).Error
}

// SumByAgents aggregates flow count and bytes for the given agent ids since cutoff.
func (r *flowEventRepo) SumByAgents(ctx context.Context, agentIDs []string, since time.Time) (int64, int64, error) {
	var count, totalBytes int64
	if len(agentIDs) == 0 {
		return 0, 0, nil
	}
	q := r.db.WithContext(ctx).Model(&models.FlowEvent{}).Where("agent_id IN ? AND ts >= ?", agentIDs, since)
	if err := q.Count(&count).Error; err != nil {
		return 0, 0, err
	}
	if err := q.Select("COALESCE(SUM(bytes), 0) AS total_bytes").Scan(&totalBytes).Error; err != nil {
		return 0, 0, err
	}
	return count, totalBytes, nil
}

func (r *flowEventRepo) ListByTrace(ctx context.Context, traceID string) ([]*models.FlowEvent, error) {
	var events []*models.FlowEvent
	return events, r.db.WithContext(ctx).
		Where("trace_id = ?", traceID).
		Order("ts asc").
		Find(&events).Error
}
