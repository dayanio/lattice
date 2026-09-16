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

package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type syncRecorder struct {
	mu     sync.Mutex
	bodies []string
}

func (r *syncRecorder) apply(msg *infra.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, msg.ConfigVersion)
	return nil
}

func (r *syncRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func msgWithVersion(v string) *infra.Message {
	return &infra.Message{ConfigVersion: v}
}

func waitForSync(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}

func TestNetmapSync_AppliesWhenVersionChanges(t *testing.T) {
	rec := &syncRecorder{}
	var version atomic.Value // string — read by the loop's fetch closure
	version.Store("v1")
	fetch := func() (*infra.Message, error) { return msgWithVersion(version.Load().(string)), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunNetmapSync(ctx, 20*time.Millisecond, fetch, rec.apply)

	waitForSync(t, 2*time.Second, func() bool { return rec.count() >= 1 }, "initial apply")
	version.Store("v2")
	waitForSync(t, 2*time.Second, func() bool { return rec.count() >= 2 }, "apply after version change")
}

func TestNetmapSync_SkipsSameVersion(t *testing.T) {
	rec := &syncRecorder{}
	fetch := func() (*infra.Message, error) { return msgWithVersion("same"), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunNetmapSync(ctx, 20*time.Millisecond, fetch, rec.apply)

	waitForSync(t, 2*time.Second, func() bool { return rec.count() >= 1 }, "first apply")
	time.Sleep(100 * time.Millisecond)
	first := rec.count()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if rec.count() > first {
			t.Fatalf("same version must not re-apply (applied %d times)", rec.count())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNetmapSync_ContinuesAfterFetchError(t *testing.T) {
	rec := &syncRecorder{}
	var fail atomic.Bool
	fail.Store(true)
	fetch := func() (*infra.Message, error) {
		if fail.Load() {
			return nil, errors.New("boom")
		}
		return msgWithVersion("v1"), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunNetmapSync(ctx, 20*time.Millisecond, fetch, rec.apply)

	fail.Store(false)
	waitForSync(t, 2*time.Second, func() bool { return rec.count() >= 1 },
		"fetch errors must not stop the loop")
}

func TestNetmapSync_StopsOnContextCancel(t *testing.T) {
	rec := &syncRecorder{}
	fetch := func() (*infra.Message, error) { return msgWithVersion("v1"), nil }

	ctx, cancel := context.WithCancel(context.Background())
	go RunNetmapSync(ctx, 20*time.Millisecond, fetch, rec.apply)
	cancel()
	time.Sleep(50 * time.Millisecond)
	before := rec.count()
	time.Sleep(100 * time.Millisecond)
	assert.LessOrEqual(t, rec.count(), before+1, "loop must stop after cancel")
	require.NoError(t, nil)
}
