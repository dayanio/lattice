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

// Peer is the standalone (non-K8s) registry record for an enrolled device.
// It mirrors the LatticePeer CRD's netmap-relevant fields so the DB-backed
// netmap builder can serve agents identically to the K8s path.
type Peer struct {
	Model

	WorkspaceID string `gorm:"size:36;uniqueIndex:idx_peer_ws_name;not null" json:"workspace_id"`
	Name        string `gorm:"size:200;uniqueIndex:idx_peer_ws_name;not null" json:"name"`
	AppID       string `gorm:"size:200;index" json:"app_id,omitempty"`
	Token       string `gorm:"size:500" json:"-"` // registration credential; never serialized
	PublicKey   string `gorm:"size:100" json:"public_key,omitempty"`
	// PrivateKey is server-generated (wgtypes key, hex) and only ever
	// leaves the registry inside the owning peer's own netmap message.
	PrivateKey  string     `gorm:"size:100" json:"-"`
	Address     string     `gorm:"size:64;index" json:"address,omitempty"` // overlay IP
	Endpoint    string     `gorm:"size:200" json:"endpoint,omitempty"`
	Hostname    string     `gorm:"size:200" json:"hostname,omitempty"`
	Platform    string     `gorm:"size:50" json:"platform,omitempty"`
	Labels      string     `gorm:"type:text" json:"labels,omitempty"` // JSON map
	// AdvertisedRoutes is a JSON array of CIDRs this peer offers to route
	// for other peers, e.g. ["0.0.0.0/0"] for exit-node, ["192.168.1.0/24"]
	// for a subnet route. Empty/absent means this peer offers nothing.
	// Consumers only get this expanded into their own AllowedIPs after
	// opting in via PeerRouteSelection — see netmap_builder.go.
	AdvertisedRoutes string     `gorm:"type:text" json:"advertised_routes,omitempty"`
	Disabled         bool       `gorm:"default:false;index" json:"disabled"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
	Description string     `gorm:"size:500" json:"description,omitempty"`
}

func (Peer) TableName() string { return "t_peer" }
