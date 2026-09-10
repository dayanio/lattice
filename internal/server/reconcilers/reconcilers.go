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

// Package reconcilers contains the standalone (non-K8s) TTL reconcilers
// for identity resources. They mirror the semantics of the K8s
// controller-runtime reconcilers in internal/server/controller so both
// deployment paths converge identically.
//
// Design doc: docs/superpowers/specs/2026-09-10-standalone-reconcile-design.md
package reconcilers

import (
	"context"

	"github.com/alatticeio/lattice/internal/server/models"
)

// Resource kinds registered by RegisterAll.
const (
	KindAgentIdentity = "AgentIdentity"
	KindPeerIdentity  = "PeerIdentity"
)

// AgentIdentityStore is the narrow store surface the GC reconciler needs.
// *gormstore.agentIdentityRepo satisfies it structurally.
type AgentIdentityStore interface {
	GetByID(ctx context.Context, id string) (*models.AgentIdentity, error)
	UpdatePhase(ctx context.Context, id string, phase string) error
	ListIDs(ctx context.Context) ([]string, error)
}

// PeerIdentityStore is the narrow store surface the grace-period
// reconciler needs.
type PeerIdentityStore interface {
	GetByID(ctx context.Context, id string) (*models.PeerIdentity, error)
	ClearGracePeriod(ctx context.Context, id string) error
	ListIDs(ctx context.Context) ([]string, error)
}
