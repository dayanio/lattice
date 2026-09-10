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
	"sync"
	"time"
)

// Clock abstracts time so the runner can be tested deterministically.
// The runner arms at most one timer at a time (it waits for the earliest
// scheduled event), so a Clock only needs Now and NewTimer.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer is a single-shot timer, mirroring the subset of *time.Timer the
// runner relies on.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// RealClock is the production Clock backed by the time package.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }

// NewTimer creates a real single-shot timer.
func (RealClock) NewTimer(d time.Duration) Timer {
	return &realTimer{t: time.NewTimer(d)}
}

type realTimer struct {
	t *time.Timer
}

func (rt *realTimer) C() <-chan time.Time { return rt.t.C }
func (rt *realTimer) Stop() bool          { return rt.t.Stop() }

// FakeClock is a manually advanced Clock for deterministic tests.
// Timers fire only when Advance crosses their deadline, so tests never
// sleep to synchronize with the runner's timing.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

// NewFakeClock returns a FakeClock frozen at start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{
		now:    start,
		timers: make(map[*fakeTimer]struct{}),
	}
}

// Now returns the current virtual time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer creates a timer on the virtual timeline. A timer whose deadline
// is at or before the current virtual time fires immediately.
func (c *FakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{clock: c, ch: make(chan time.Time, 1), deadline: c.now.Add(d)}
	if !ft.deadline.After(c.now) {
		ft.ch <- c.now
	} else {
		c.timers[ft] = struct{}{}
	}
	return ft
}

// Advance moves virtual time forward by d, firing every active timer whose
// deadline falls within the interval, in chronological order. Virtual time
// pauses exactly on each fired deadline so observers see precise timestamps.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	target := c.now.Add(d)
	for {
		var earliest *fakeTimer
		for ft := range c.timers {
			if ft.deadline.After(target) {
				continue
			}
			if earliest == nil || ft.deadline.Before(earliest.deadline) {
				earliest = ft
			}
		}
		if earliest == nil {
			break
		}
		c.now = earliest.deadline
		delete(c.timers, earliest)
		select {
		case earliest.ch <- c.now:
		default:
		}
	}
	c.now = target
}

type fakeTimer struct {
	clock    *FakeClock
	ch       chan time.Time
	deadline time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	_, active := t.clock.timers[t]
	delete(t.clock.timers, t)
	return active
}
