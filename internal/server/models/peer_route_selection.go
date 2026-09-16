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

// PeerRouteSelection records that ConsumerPeerID has opted to accept
// ProviderPeerID's AdvertisedRoutes. One row per (consumer, provider) pair;
// a consumer may select more than one provider (e.g. a subnet route from
// one peer and an exit node from another).
type PeerRouteSelection struct {
	Model

	WorkspaceID    string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ConsumerPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
	ProviderPeerID string `gorm:"size:36;uniqueIndex:idx_route_sel;not null"`
}

func (PeerRouteSelection) TableName() string { return "t_peer_route_selection" }
