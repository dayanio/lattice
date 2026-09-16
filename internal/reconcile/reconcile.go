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

// Package reconcile provides a K8s-free level-triggered convergence loop
// for the standalone (non-K8s) control plane.
//
// The pattern is identical to controller-runtime's reconcilers: each
// Reconcile call recomputes reality from the full desired state, making
// repeated invocations harmless and convergence eventual. What controller-
// runtime calls a workqueue, this package calls a Runner — a ~small event
// loop with three trigger sources: write-path notifications, periodic
// resync, and timed requeues (RequeueAfter / error backoff).
//
// Design doc: docs/superpowers/specs/2026-09-10-standalone-reconcile-design.md
package reconcile

import (
	"context"
	"errors"
	"time"
)

// DefaultResyncInterval is the fallback resync period used by Register.
// Even if every notification is lost, each registered kind is fully
// enumerated and reconciled once per interval, which bounds convergence
// delay and guarantees eventual consistency.
const DefaultResyncInterval = 30 * time.Second

// ErrAlreadyStarted is returned by Register when called after Start.
var ErrAlreadyStarted = errors.New("reconcile: runner already started")

// Request identifies one instance of a resource kind to reconcile.
// Kind groups resources (e.g. "LatticePolicy", "AgentIdentity"); Key is
// the store primary key or an encoded composite key such as (tenant, name).
type Request struct {
	Kind string
	Key  string
}

// Result optionally requests another round after RequeueAfter elapses.
// It mirrors controller-runtime's reconcile.Result; error returns are
// handled separately by the runner's backoff.
type Result struct {
	RequeueAfter time.Duration
}

// Reconciler converges one resource instance with its desired state.
//
// Implementations must be:
//   - Idempotent: recompute from the full desired state every call.
//   - Context-aware: honor ctx cancellation for slow operations.
//   - Honest about errors: return transient errors so the runner retries
//     with backoff; record permanent failures in the resource status and
//     return nil.
type Reconciler interface {
	Reconcile(ctx context.Context, req Request) (Result, error)
}

// KeyLister enumerates every key of a kind. It is invoked once per resync
// period (and once at startup) so convergence survives lost notifications.
type KeyLister interface {
	ListKeys(ctx context.Context) ([]Request, error)
}
