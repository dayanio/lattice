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

// EnrollmentToken is the standalone (non-K8s) device enrollment token,
// mirroring the LatticeEnrollmentToken CRD: it authorises one device to
// join a specific workspace, with optional expiry and usage limit.
type EnrollmentToken struct {
	Model

	Token       string    `gorm:"uniqueIndex;size:64;not null" json:"token"`
	WorkspaceID string    `gorm:"size:36;index;not null" json:"workspace_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	// UsageLimit caps how many devices may enroll with this token; zero
	// means unlimited.
	UsageLimit int    `gorm:"default:0" json:"usage_limit"`
	UsedCount  int    `gorm:"default:0" json:"used_count"`
	CreatedBy  string `gorm:"size:64" json:"created_by,omitempty"`
}

func (EnrollmentToken) TableName() string { return "t_enrollment_token" }
