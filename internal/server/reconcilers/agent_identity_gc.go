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

	api "github.com/alatticeio/lattice/api/v1alpha1"
	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/go-logr/logr"
	"gorm.io/gorm"
)

// AgentIdentityGC mirrors internal/server/controller's AgentIdentityReconciler:
//   - terminal phases (Expired/Revoked) are left untouched;
//   - an identity whose ExpiresAt has passed transitions to Expired
//     (the record is kept for audit);
//   - a fresh identity activates Pending→Active;
//   - a live TTL schedules the next check just after expiry.
type AgentIdentityGC struct {
	repo   AgentIdentityStore
	logger logr.Logger
	now    func() time.Time
}

// GCOption customizes the TTL reconcilers (mainly for tests).
type GCOption func(nowSetter)

// WithNow overrides the time source used for expiry checks.
func WithNow(now func() time.Time) GCOption {
	return func(n nowSetter) { n.setNow(now) }
}

type nowSetter interface{ setNow(func() time.Time) }

func (g *AgentIdentityGC) setNow(now func() time.Time) { g.now = now }

// NewAgentIdentityGC returns the reconciler for KindAgentIdentity.
func NewAgentIdentityGC(repo AgentIdentityStore, logger logr.Logger, opts ...GCOption) *AgentIdentityGC {
	g := &AgentIdentityGC{repo: repo, logger: logger, now: time.Now}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// Reconcile converges one AgentIdentity with its TTL lifecycle.
func (g *AgentIdentityGC) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	id, err := g.repo.GetByID(ctx, req.Key)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return reconcile.Result{}, nil // deleted elsewhere; nothing to converge
		}
		return reconcile.Result{}, err
	}

	// Already in a terminal phase — nothing to do.
	if id.Phase == string(api.AgentPhaseExpired) || id.Phase == string(api.AgentPhaseRevoked) {
		return reconcile.Result{}, nil
	}

	now := g.now()

	// Check expiry before activating, like the K8s reconciler.
	if id.ExpiresAt != nil && now.After(*id.ExpiresAt) {
		if err := g.repo.UpdatePhase(ctx, req.Key, string(api.AgentPhaseExpired)); err != nil {
			return reconcile.Result{}, err
		}
		g.logger.Info("agent identity expired", "id", req.Key, "name", id.Name)
		return reconcile.Result{}, nil
	}

	// Transition new / pending identities to Active.
	if id.Phase == "" || id.Phase == string(api.AgentPhasePending) {
		if err := g.repo.UpdatePhase(ctx, req.Key, string(api.AgentPhaseActive)); err != nil {
			return reconcile.Result{}, err
		}
	}

	// Schedule the expiry check just after the deadline.
	if id.ExpiresAt != nil {
		return reconcile.Result{RequeueAfter: requeueAfter(now, *id.ExpiresAt)}, nil
	}
	return reconcile.Result{}, nil
}

// requeueAfter returns the delay until deadline (plus a one-second margin)
// clamped to a positive value so the timer always fires after the deadline.
func requeueAfter(now, deadline time.Time) time.Duration {
	d := deadline.Sub(now) + time.Second
	if d < time.Second {
		d = time.Second
	}
	return d
}

// AgentIdentityKeyLister enumerates identities for resync rounds.
type AgentIdentityKeyLister struct {
	repo AgentIdentityStore
}

// NewAgentIdentityKeyLister returns the KeyLister for KindAgentIdentity.
func NewAgentIdentityKeyLister(repo AgentIdentityStore) *AgentIdentityKeyLister {
	return &AgentIdentityKeyLister{repo: repo}
}

// ListKeys returns one Request per stored identity.
func (l *AgentIdentityKeyLister) ListKeys(ctx context.Context) ([]reconcile.Request, error) {
	ids, err := l.repo.ListIDs(ctx)
	if err != nil {
		return nil, err
	}
	reqs := make([]reconcile.Request, 0, len(ids))
	for _, id := range ids {
		reqs = append(reqs, reconcile.Request{Kind: KindAgentIdentity, Key: id})
	}
	return reqs, nil
}

var (
	_ reconcile.Reconciler = (*AgentIdentityGC)(nil)
	_ reconcile.KeyLister  = (*AgentIdentityKeyLister)(nil)
)
