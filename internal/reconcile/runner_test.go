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

package reconcile_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testKind = "LatticePolicy"

func key(name string) reconcile.Request {
	return reconcile.Request{Kind: testKind, Key: name}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, msg)
}

func startRunner(t *testing.T, r *reconcile.Runner) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	t.Cleanup(cancel)
	return cancel, done
}

// recorder counts every Reconcile call, always succeeding.
type recorder struct {
	mu    sync.Mutex
	calls []reconcile.Request
}

func (r *recorder) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req)
	return reconcile.Result{}, nil
}

func (r *recorder) count(kind, key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c.Kind == kind && c.Key == key {
			n++
		}
	}
	return n
}

func (r *recorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// scriptedReconciler replays a fixed error script, then always succeeds.
type scriptedReconciler struct {
	mu     sync.Mutex
	calls  int
	script []error // nil entries succeed
}

func (s *scriptedReconciler) Reconcile(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i < len(s.script) {
		return reconcile.Result{}, s.script[i]
	}
	return reconcile.Result{}, nil
}

func (s *scriptedReconciler) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// requeueRecorder always asks for another round after interval.
type requeueRecorder struct {
	mu       sync.Mutex
	calls    int
	interval time.Duration
}

func (r *requeueRecorder) Reconcile(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return reconcile.Result{RequeueAfter: r.interval}, nil
}

func (r *requeueRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// gatedReconciler blocks the first call until its gate closes.
type gatedReconciler struct {
	mu           sync.Mutex
	calls        int
	gate         chan struct{}
	firstEntered chan struct{}
	once         sync.Once
}

func (g *gatedReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	g.mu.Lock()
	n := g.calls
	g.calls++
	g.mu.Unlock()
	if n == 0 {
		g.once.Do(func() { close(g.firstEntered) })
		select {
		case <-g.gate:
		case <-ctx.Done():
			return reconcile.Result{}, ctx.Err()
		}
	}
	return reconcile.Result{}, nil
}

func (g *gatedReconciler) total() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// concurrencyProbe tracks the maximum simultaneous invocations. After each
// run it re-notifies via trigger until target runs happened, producing many
// overlapping rounds to exercise per-key serialization.
type concurrencyProbe struct {
	cur     atomic.Int32
	max     atomic.Int32
	calls   atomic.Int32
	target  int32
	trigger func(kind, key string)
}

func (p *concurrencyProbe) Reconcile(_ context.Context, req reconcile.Request) (reconcile.Result, error) {
	calls := p.calls.Add(1)
	c := p.cur.Add(1)
	for {
		m := p.max.Load()
		if c <= m || p.max.CompareAndSwap(m, c) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	p.cur.Add(-1)
	if calls < p.target {
		p.trigger(req.Kind, req.Key)
	}
	return reconcile.Result{}, nil
}

// panickyReconciler panics on its first call.
type panickyReconciler struct {
	mu    sync.Mutex
	calls int
}

func (p *panickyReconciler) Reconcile(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
	p.mu.Lock()
	n := p.calls
	p.calls++
	p.mu.Unlock()
	if n == 0 {
		panic("boom")
	}
	return reconcile.Result{}, nil
}

func (p *panickyReconciler) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeLister returns keys; if failFirst is set, the first call errors.
type fakeLister struct {
	mu        sync.Mutex
	calls     int
	failFirst bool
	keys      []reconcile.Request
}

func (f *fakeLister) ListKeys(_ context.Context) ([]reconcile.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failFirst && f.calls == 1 {
		return nil, errors.New("list failed")
	}
	return f.keys, nil
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestRunner_NotifyRunsReconciler(t *testing.T) {
	rec := &recorder{}
	r := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))
	require.NoError(t, r.Register(testKind, rec, nil))

	cancel, done := startRunner(t, r)

	r.Notify(testKind, "policy-a")
	waitFor(t, 2*time.Second, func() bool { return rec.count(testKind, "policy-a") == 1 },
		"reconciler should run once after Notify")

	assert.Equal(t, 0, rec.count(testKind, "policy-b"), "unrelated key must not run")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "Start should return cleanly on cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}

func TestRunner_RegisterValidation(t *testing.T) {
	r := reconcile.NewRunner()
	assert.Error(t, r.Register("", &recorder{}, nil), "empty kind must be rejected")
	assert.Error(t, r.Register(testKind, nil, nil), "nil reconciler must be rejected")
}

func TestRunner_RegisterAfterStartReturnsError(t *testing.T) {
	rec := &recorder{}
	r := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))
	require.NoError(t, r.Register(testKind, rec, nil))

	cancel, _ := startRunner(t, r)
	waitFor(t, 2*time.Second, r.Started, "runner should be started")

	err := r.Register("Other", &recorder{}, nil)
	assert.ErrorIs(t, err, reconcile.ErrAlreadyStarted)
	cancel()
}

func TestRunner_ResyncEnumeratesAndReconciles(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Now())
	rec := &recorder{}
	lister := &fakeLister{keys: []reconcile.Request{key("a"), key("b")}}
	r := reconcile.NewRunner(reconcile.WithClock(clk))
	require.NoError(t, r.RegisterWithResync(testKind, rec, lister, time.Minute))

	startRunner(t, r)

	// Initial resync at startup converges every listed key.
	waitFor(t, 2*time.Second, func() bool { return rec.count(testKind, "a") == 1 && rec.count(testKind, "b") == 1 },
		"initial resync should reconcile all listed keys")

	// Periodic resync repeats forever.
	clk.Advance(time.Minute)
	waitFor(t, 2*time.Second, func() bool { return lister.callCount() >= 2 && rec.total() >= 4 },
		"second resync should re-enumerate and re-reconcile all keys")
}

func TestRunner_ResyncRetriesAfterListerError(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Now())
	rec := &recorder{}
	lister := &fakeLister{failFirst: true, keys: []reconcile.Request{key("a")}}
	r := reconcile.NewRunner(reconcile.WithClock(clk))
	require.NoError(t, r.RegisterWithResync(testKind, rec, lister, time.Minute))

	startRunner(t, r)

	// First resync: ListKeys fails, nothing reconciled, runner survives.
	waitFor(t, 2*time.Second, func() bool { return lister.callCount() == 1 }, "first resync attempted")
	clk.Advance(time.Minute)
	waitFor(t, 2*time.Second, func() bool { return rec.count(testKind, "a") == 1 },
		"keys should reconcile on the resync after a lister error")
}

func TestRunner_RequeueAfter(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Now())
	rec := &requeueRecorder{interval: time.Minute}
	r := reconcile.NewRunner(reconcile.WithClock(clk))
	require.NoError(t, r.Register(testKind, rec, nil))

	startRunner(t, r)

	r.Notify(testKind, "a")
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 1 }, "first run")

	// Give the scheduler a moment to arm the requeue timer.
	time.Sleep(20 * time.Millisecond)

	clk.Advance(59 * time.Second)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, rec.total(), "must not re-run before RequeueAfter elapses")

	clk.Advance(2 * time.Second)
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 2 }, "requeue after the interval")
}

func TestRunner_BackoffOnError(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Now())
	rec := &scriptedReconciler{script: []error{errors.New("e1"), errors.New("e2")}}
	r := reconcile.NewRunner(reconcile.WithClock(clk))
	require.NoError(t, r.Register(testKind, rec, nil))

	startRunner(t, r)

	r.Notify(testKind, "a")
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 1 }, "first attempt")
	time.Sleep(20 * time.Millisecond) // arm the 100ms backoff timer

	clk.Advance(99 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, rec.total(), "must not retry before first backoff step elapses")

	clk.Advance(2 * time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 2 }, "second attempt after first backoff step")
	time.Sleep(20 * time.Millisecond) // arm the 1s backoff timer

	clk.Advance(999 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 2, rec.total(), "must not retry before second backoff step elapses")

	clk.Advance(2 * time.Millisecond)
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 3 }, "third attempt after second backoff step")
	time.Sleep(20 * time.Millisecond)

	// Success resets the backoff: nothing scheduled, no matter how long passes.
	clk.Advance(24 * time.Hour)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 3, rec.total(), "successful reconcile must reset backoff")
}

func TestRunner_NotifyRunsImmediatelyDuringBackoff(t *testing.T) {
	rec := &scriptedReconciler{script: []error{errors.New("e1")}}
	r := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))
	require.NoError(t, r.Register(testKind, rec, nil))

	startRunner(t, r)

	r.Notify(testKind, "a")
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 1 }, "first attempt fails")

	// A notification supersedes the pending backoff wait: run now, no clock advance.
	r.Notify(testKind, "a")
	waitFor(t, 2*time.Second, func() bool { return rec.total() == 2 }, "notification should trigger an immediate retry")
}

func TestRunner_CoalescesNotificationsWhileInFlight(t *testing.T) {
	g := &gatedReconciler{gate: make(chan struct{}), firstEntered: make(chan struct{})}
	r := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))
	require.NoError(t, r.Register(testKind, g, nil))

	startRunner(t, r)

	r.Notify(testKind, "k")
	select {
	case <-g.firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first reconcile never started")
	}

	// Three notifications arrive while the first round is still in flight.
	r.Notify(testKind, "k")
	r.Notify(testKind, "k")
	r.Notify(testKind, "k")

	close(g.gate)
	waitFor(t, 2*time.Second, func() bool { return g.total() == 2 },
		"notifications coalesce into exactly one follow-up round")

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 2, g.total(), "must not run once per notification")
}

func TestRunner_SameKeyNeverConcurrent(t *testing.T) {
	probe := &concurrencyProbe{target: 30}
	r := reconcile.NewRunner(
		reconcile.WithWorkers(8),
		reconcile.WithClock(reconcile.NewFakeClock(time.Now())),
	)
	require.NoError(t, r.Register(testKind, probe, nil))
	probe.trigger = r.Notify

	startRunner(t, r)

	// A burst of notifications plus self-chained re-notifications produce
	// many rounds for the same key; overlapping runs must never happen.
	for i := 0; i < 20; i++ {
		r.Notify(testKind, "k")
	}

	waitFor(t, 2*time.Second, func() bool { return probe.calls.Load() >= 30 }, "chained rounds complete")
	waitFor(t, 2*time.Second, func() bool { return probe.cur.Load() == 0 }, "all runs finished")
	time.Sleep(100 * time.Millisecond) // allow any illegal overlap to surface

	assert.LessOrEqual(t, probe.max.Load(), int32(1), "same key must never run concurrently")
}

func TestRunner_RecoversFromPanic(t *testing.T) {
	p := &panickyReconciler{}
	r := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))
	require.NoError(t, r.Register(testKind, p, nil))

	startRunner(t, r)

	r.Notify(testKind, "k")
	waitFor(t, 2*time.Second, func() bool { return p.total() == 1 }, "first call panics, runner survives")

	r.Notify(testKind, "k")
	waitFor(t, 2*time.Second, func() bool { return p.total() == 2 },
		"runner must keep reconciling after a panic")
}

func TestRunner_RealClockSmoke(t *testing.T) {
	rec := &recorder{}
	r := reconcile.NewRunner() // default real clock
	require.NoError(t, r.Register(testKind, rec, nil))

	cancel, done := startRunner(t, r)

	r.Notify(testKind, "x")
	waitFor(t, 2*time.Second, func() bool { return rec.count(testKind, "x") == 1 },
		"notify should run with the real clock")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after cancel")
	}
}
