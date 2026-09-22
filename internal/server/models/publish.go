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

// Publish is one 对外发布 rule (v1 gateway mode, see
// docs/superpowers/specs/2026-09-22-mac-client-capabilities-design.md §六):
// a publish gateway exposes <peerName>:<port> under its own HTTP ingress at
// /<name>/. Rules live in the registry; gateways learn them from the
// lattice.signals.publishes NATS broadcast.
type Publish struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	WorkspaceID string    `gorm:"index" json:"workspaceId"`
	// Name is the public path prefix and unique key: [a-z0-9-]{3,32}.
	Name string `gorm:"uniqueIndex" json:"name"`
	// PeerName is the registry name of the target peer (must be approved).
	PeerName string `json:"peerName"`
	// Port is the TCP port on the target peer to forward to.
	Port      int    `json:"port"`
	Enabled   bool   `gorm:"default:true" json:"enabled"`
	CreatedBy string `json:"createdBy"`
}

func (Publish) TableName() string { return "t_publish" }
