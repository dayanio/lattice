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

package models

import "time"

// PolicyVersion is an immutable entry in a policy's version timeline
// ("策略即代码"的审计时间线): every create/apply/expire/delete appends one
// row, so the policy detail view can answer "why does this policy exist,
// who changed it, who approved it".
type PolicyVersion struct {
	Model

	PolicyID   string    `gorm:"size:36;index;not null" json:"policy_id"`
	Version    int       `gorm:"not null" json:"version"`
	Spec       string    `gorm:"type:text" json:"spec,omitempty"`
	Intent     string    `gorm:"type:text" json:"intent,omitempty"` // 自然语言描述原文
	Action     string    `gorm:"size:20;not null" json:"action"`    // created / applied / expired / deleted
	ChangedBy  string    `gorm:"size:64" json:"changed_by,omitempty"`
	ApprovedBy string    `gorm:"size:64" json:"approved_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (PolicyVersion) TableName() string { return "t_policy_version" }

// Policy version actions.
const (
	PolicyVersionActionCreated = "created"
	PolicyVersionActionApplied = "applied"
	PolicyVersionActionExpired = "expired"
	PolicyVersionActionDeleted = "deleted"
)
