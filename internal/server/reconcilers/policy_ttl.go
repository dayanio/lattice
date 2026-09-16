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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/go-logr/logr"
	"gorm.io/gorm"
)

// PolicyStore is the narrow store surface the policy TTL reconciler needs.
type PolicyStore interface {
	GetByID(ctx context.Context, id string) (*models.Policy, error)
	UpdateStatus(ctx context.Context, id string, status models.PolicyStatus) error
	ListIDsActiveWithExpiry(ctx context.Context) ([]string, error)
}

// PolicyTTL mirrors internal/agent/controller's PolicyTTLReconciler on the
// DB path: an active policy whose ExpiresAt has passed flips to status
// expired. Unlike the K8s reconciler (which deletes the CRD and leaves
// t_policy stale "active"), the record and its status stay consistent.
type PolicyTTL struct {
	repo   PolicyStore
	logger logr.Logger
}

// NewPolicyTTL returns the reconciler for KindPolicy.
func NewPolicyTTL(repo PolicyStore, logger logr.Logger) *PolicyTTL {
	return &PolicyTTL{repo: repo, logger: logger}
}

// Reconcile converges one policy's TTL.
func (t *PolicyTTL) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	p, err := t.repo.GetByID(ctx, req.Key)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	if p.Status != models.PolicyStatusActive || p.ExpiresAt == nil {
		return reconcile.Result{}, nil
	}

	now := time.Now()
	if !now.After(*p.ExpiresAt) {
		return reconcile.Result{RequeueAfter: requeueAfter(now, *p.ExpiresAt)}, nil
	}

	if err := t.repo.UpdateStatus(ctx, req.Key, models.PolicyStatusExpired); err != nil {
		return reconcile.Result{}, err
	}
	t.logger.Info("policy expired", "id", req.Key, "name", p.Name, "workspace", p.WorkspaceID)
	return reconcile.Result{}, nil
}

// PolicyTTLKeyLister enumerates active policies carrying a TTL.
type PolicyTTLKeyLister struct {
	repo store.PolicyRepository
}

// NewPolicyTTLKeyLister returns the KeyLister for KindPolicy.
func NewPolicyTTLKeyLister(repo store.PolicyRepository) *PolicyTTLKeyLister {
	return &PolicyTTLKeyLister{repo: repo}
}

// ListKeys returns one Request per active policy with a TTL.
func (l *PolicyTTLKeyLister) ListKeys(ctx context.Context) ([]reconcile.Request, error) {
	ids, err := l.repo.ListIDsActiveWithExpiry(ctx)
	if err != nil {
		return nil, err
	}
	reqs := make([]reconcile.Request, 0, len(ids))
	for _, id := range ids {
		reqs = append(reqs, reconcile.Request{Kind: KindPolicy, Key: id})
	}
	return reqs, nil
}

var (
	_ reconcile.Reconciler = (*PolicyTTL)(nil)
	_ reconcile.KeyLister  = (*PolicyTTLKeyLister)(nil)
)
