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

package reconcilers

import (
	"context"
	"errors"
	"time"

	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/go-logr/logr"
	"gorm.io/gorm"
)

// PeerIdentityGrace mirrors the K8s-side grace-period controller: once
// GracePeriodExpiresAt passes, the previous device binding is cleared so
// the old peer stops matching identityRef policies. During the grace
// window both old and new devices resolve, enabling zero-downtime device
// replacement.
type PeerIdentityGrace struct {
	repo   PeerIdentityStore
	logger logr.Logger
	now    func() time.Time
}

// NewPeerIdentityGrace returns the reconciler for KindPeerIdentity. The
// GCOption variadic accepts WithNow (shared with AgentIdentityGC).
func NewPeerIdentityGrace(repo PeerIdentityStore, logger logr.Logger, opts ...GCOption) *PeerIdentityGrace {
	g := &PeerIdentityGrace{repo: repo, logger: logger, now: time.Now}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Reconcile converges one PeerIdentity's grace period.
func (g *PeerIdentityGrace) setNow(now func() time.Time) { g.now = now }

func (g *PeerIdentityGrace) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	id, err := g.repo.GetByID(ctx, req.Key)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if id.GracePeriodExpiresAt == nil {
		return reconcile.Result{}, nil // no device replacement in flight
	}

	now := g.now()
	if !now.After(*id.GracePeriodExpiresAt) {
		return reconcile.Result{RequeueAfter: requeueAfter(now, *id.GracePeriodExpiresAt)}, nil
	}

	if err := g.repo.ClearGracePeriod(ctx, req.Key); err != nil {
		return reconcile.Result{}, err
	}
	g.logger.Info("peer identity grace period elapsed",
		"id", req.Key, "name", id.Name, "previousPeerRef", id.PreviousPeerRef)
	return reconcile.Result{}, nil
}

// PeerIdentityKeyLister enumerates peer identities for resync rounds.
type PeerIdentityKeyLister struct {
	repo PeerIdentityStore
}

// NewPeerIdentityKeyLister returns the KeyLister for KindPeerIdentity.
func NewPeerIdentityKeyLister(repo PeerIdentityStore) *PeerIdentityKeyLister {
	return &PeerIdentityKeyLister{repo: repo}
}

// ListKeys returns one Request per stored peer identity.
func (l *PeerIdentityKeyLister) ListKeys(ctx context.Context) ([]reconcile.Request, error) {
	ids, err := l.repo.ListIDs(ctx)
	if err != nil {
		return nil, err
	}
	reqs := make([]reconcile.Request, 0, len(ids))
	for _, id := range ids {
		reqs = append(reqs, reconcile.Request{Kind: KindPeerIdentity, Key: id})
	}
	return reqs, nil
}

var (
	_ reconcile.Reconciler = (*PeerIdentityGrace)(nil)
	_ reconcile.KeyLister  = (*PeerIdentityKeyLister)(nil)
)
