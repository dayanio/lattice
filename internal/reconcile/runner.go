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

package reconcile

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

const defaultWorkers = 4

// Option configures a Runner.
type Option func(*Runner)

// WithWorkers sets the number of worker goroutines (default 4). Different
// keys reconcile in parallel; the same key never does.
func WithWorkers(n int) Option {
	return func(r *Runner) {
		if n > 0 {
			r.workers = n
		}
	}
}

// WithClock overrides the clock (tests inject a FakeClock).
func WithClock(c Clock) Option {
	return func(r *Runner) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithLogger sets the logger (default discards everything).
func WithLogger(l logr.Logger) Option {
	return func(r *Runner) { r.logger = l }
}

// WithBackoff overrides the retry backoff steps on error. The last step
// caps repeated failures; a success resets the sequence.
func WithBackoff(steps []time.Duration) Option {
	return func(r *Runner) {
		if len(steps) > 0 {
			r.backoff = steps
		}
	}
}

// Runner is the K8s-free equivalent of controller-runtime's manager and
// workqueue: it drives registered Reconcilers from three trigger sources —
// write-path notifications, periodic resync, and timed events
// (RequeueAfter, error backoff).
type Runner struct {
	workers int
	backoff []time.Duration
	clock   Clock
	logger  logr.Logger

	mu      sync.Mutex
	regs    map[string]*registration
	inbox   map[Request]struct{}
	started bool
	wake    chan struct{}
}

// registration couples a kind with its reconciler and optional resync.
type registration struct {
	kind       string
	reconciler Reconciler
	lister     KeyLister
	resync     time.Duration
}

// NewRunner returns a Runner configured by opts.
func NewRunner(opts ...Option) *Runner {
	r := &Runner{
		workers: defaultWorkers,
		backoff: []time.Duration{100 * time.Millisecond, time.Second, 5 * time.Second, 30 * time.Second},
		clock:   RealClock{},
		logger:  logr.Discard(),
		regs:    make(map[string]*registration),
		inbox:   make(map[Request]struct{}),
		wake:    make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a Reconciler for kind. A nil lister disables resync for
// that kind — it then reconciles only via Notify and RequeueAfter.
func (r *Runner) Register(kind string, rec Reconciler, lister KeyLister) error {
	return r.RegisterWithResync(kind, rec, lister, DefaultResyncInterval)
}

// RegisterWithResync is Register with an explicit resync period. A
// non-positive resync disables periodic re-enumeration.
func (r *Runner) RegisterWithResync(kind string, rec Reconciler, lister KeyLister, resync time.Duration) error {
	if kind == "" {
		return errors.New("reconcile: kind must not be empty")
	}
	if rec == nil {
		return fmt.Errorf("reconcile: nil reconciler for kind %q", kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return ErrAlreadyStarted
	}
	r.regs[kind] = &registration{kind: kind, reconciler: rec, lister: lister, resync: resync}
	return nil
}

// Notify requests an immediate reconcile round for (kind, key). It is safe
// to call from any goroutine, never blocks, and coalesces while a round
// for the same key is still in flight. Notifications for kinds that were
// never registered are dropped with a log line.
func (r *Runner) Notify(kind, key string) {
	req := Request{Kind: kind, Key: key}
	r.mu.Lock()
	if _, ok := r.regs[kind]; !ok {
		r.mu.Unlock()
		r.logger.Info("notify dropped for unregistered kind", "kind", kind, "key", key)
		return
	}
	r.inbox[req] = struct{}{}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Started reports whether Start has been called.
func (r *Runner) Started() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started
}

// Start blocks running the reconcile loop until ctx is cancelled. It
// returns nil on graceful shutdown. An initial resync of every registered
// kind with a KeyLister runs at startup, so state converges without any
// prior notification.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return ErrAlreadyStarted
	}
	r.started = true
	r.mu.Unlock()

	tasks := make(chan task)
	completions := make(chan completion)

	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case t, ok := <-tasks:
					if !ok {
						return
					}
					res, err := r.invoke(ctx, t.reg, t.req)
					select {
					case completions <- completion{req: t.req, res: res, err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	err := r.loop(ctx, tasks, completions)
	close(tasks)
	wg.Wait()
	return err
}

// task pairs a request with its registration so workers need no map reads.
type task struct {
	reg *registration
	req Request
}

type completion struct {
	req Request
	res Result
	err error
}

// event is a scheduled future action: either a requeue/backoff for a key
// or a resync of a whole kind.
type event struct {
	at     time.Time
	req    Request
	kind   string
	resync bool
}

type eventQueue []event

func (q eventQueue) Len() int            { return len(q) }
func (q eventQueue) Less(i, j int) bool  { return q[i].at.Before(q[j].at) }
func (q eventQueue) Swap(i, j int)       { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x interface{}) { *q = append(*q, x.(event)) }
func (q *eventQueue) Pop() interface{} {
	old := *q
	n := len(old)
	ev := old[n-1]
	*q = old[:n-1]
	return ev
}

// loop is the scheduler goroutine. It owns all reconciliation state:
// pending (queued, awaiting a worker), inFlight (being processed),
// pendingAfter (notified while in flight — coalesced into one rerun),
// fails (consecutive error counts for backoff), and the timed event heap.
func (r *Runner) loop(ctx context.Context, tasks chan task, completions chan completion) error {
	r.mu.Lock()
	regs := make(map[string]*registration, len(r.regs))
	for kind, reg := range r.regs {
		regs[kind] = reg
	}
	r.mu.Unlock()

	pending := make(map[Request]struct{})
	var pendingQueue []Request
	inFlight := make(map[Request]struct{})
	pendingAfter := make(map[Request]struct{})
	fails := make(map[Request]int)

	var eq eventQueue
	heap.Init(&eq)

	// Initial resync: converge from storage without any prior notification.
	for _, reg := range regs {
		if reg.lister != nil && reg.resync > 0 {
			heap.Push(&eq, event{at: r.clock.Now(), kind: reg.kind, resync: true})
		}
	}

	// enqueue adds one round for req, coalescing by current state.
	enqueue := func(req Request) {
		if _, ok := inFlight[req]; ok {
			pendingAfter[req] = struct{}{}
			return
		}
		if _, ok := pending[req]; ok {
			return
		}
		pending[req] = struct{}{}
		pendingQueue = append(pendingQueue, req)
	}

	// drainInbox moves notified keys into the queue. A notification always
	// runs immediately, superseding any pending backoff wait (the stale
	// heap entry later turns into a harmless no-op or extra idempotent run).
	drainInbox := func() {
		r.mu.Lock()
		batch := r.inbox
		r.inbox = make(map[Request]struct{})
		r.mu.Unlock()
		for req := range batch {
			enqueue(req)
		}
	}

	// fireDue pops every event whose deadline has passed. Resync events
	// schedule their next occurrence before listing keys so a concurrent
	// clock advance can never miss the following round.
	fireDue := func() {
		now := r.clock.Now()
		for eq.Len() > 0 && !eq[0].at.After(now) {
			ev := heap.Pop(&eq).(event)
			if ev.resync {
				if reg := regs[ev.kind]; reg != nil && reg.lister != nil && reg.resync > 0 {
					heap.Push(&eq, event{at: now.Add(reg.resync), kind: ev.kind, resync: true})
				}
				reg := regs[ev.kind]
				if reg == nil || reg.lister == nil {
					continue
				}
				keys, err := reg.lister.ListKeys(ctx)
				if err != nil {
					r.logger.Error(err, "resync ListKeys failed", "kind", ev.kind)
					continue
				}
				for _, req := range keys {
					enqueue(req)
				}
			} else {
				enqueue(ev.req)
			}
		}
	}

	busy := 0
	// dispatch hands queued keys to idle workers. inFlight guarantees the
	// same key is never processed by two workers at once.
	dispatch := func() {
		for busy < r.workers && len(pendingQueue) > 0 {
			req := pendingQueue[0]
			pendingQueue = pendingQueue[1:]
			delete(pending, req)
			inFlight[req] = struct{}{}
			busy++
			select {
			case tasks <- task{reg: regs[req.Kind], req: req}:
			case <-ctx.Done():
				return
			}
		}
	}

	var timer Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	for {
		if ctx.Err() != nil {
			return nil
		}

		// Arm a single timer for the earliest scheduled event.
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		var timerC <-chan time.Time
		if eq.Len() > 0 {
			d := eq[0].at.Sub(r.clock.Now())
			if d < 0 {
				d = 0
			}
			timer = r.clock.NewTimer(d)
			timerC = timer.C()
		}

		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			drainInbox()
			dispatch()
		case <-timerC:
			fireDue()
			dispatch()
		case c := <-completions:
			busy--
			delete(inFlight, c.req)
			if c.err != nil {
				fails[c.req]++
				idx := fails[c.req] - 1
				if idx >= len(r.backoff) {
					idx = len(r.backoff) - 1
				}
				heap.Push(&eq, event{at: r.clock.Now().Add(r.backoff[idx]), req: c.req})
				r.logger.Error(c.err, "reconcile failed, backing off",
					"kind", c.req.Kind, "key", c.req.Key, "attempt", fails[c.req])
			} else {
				delete(fails, c.req)
				if c.res.RequeueAfter > 0 {
					heap.Push(&eq, event{at: r.clock.Now().Add(c.res.RequeueAfter), req: c.req})
				}
			}
			if _, ok := pendingAfter[c.req]; ok {
				delete(pendingAfter, c.req)
				enqueue(c.req)
			}
			dispatch()
		}
	}
}

// invoke runs one Reconcile call, converting panics into errors so a
// misbehaving reconciler cannot take the runner down.
func (r *Runner) invoke(ctx context.Context, reg *registration, req Request) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("reconcile: panic in %s/%s: %v", req.Kind, req.Key, p)
		}
	}()
	return reg.reconciler.Reconcile(ctx, req)
}
