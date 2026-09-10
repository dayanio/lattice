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
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeClock_TimerFiresOnAdvance(t *testing.T) {
	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	clk := reconcile.NewFakeClock(start)

	tm := clk.NewTimer(10 * time.Second)

	clk.Advance(5 * time.Second)
	select {
	case got := <-tm.C():
		t.Fatalf("timer fired early at %v, want fire at %v", got, start.Add(10*time.Second))
	default:
	}

	clk.Advance(5 * time.Second)
	select {
	case got := <-tm.C():
		assert.Equal(t, start.Add(10*time.Second), got)
	default:
		t.Fatal("timer did not fire after advancing past deadline")
	}
	assert.Equal(t, start.Add(10*time.Second), clk.Now())
}

func TestFakeClock_ZeroDurationFiresImmediately(t *testing.T) {
	start := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	clk := reconcile.NewFakeClock(start)

	tm := clk.NewTimer(0)

	select {
	case got := <-tm.C():
		assert.Equal(t, start, got)
	default:
		t.Fatal("zero-duration timer should fire immediately")
	}
}

func TestFakeClock_StoppedTimerDoesNotFire(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	tm := clk.NewTimer(time.Second)
	require.True(t, tm.Stop())

	clk.Advance(time.Hour)
	select {
	case <-tm.C():
		t.Fatal("stopped timer fired")
	default:
	}
}

func TestRealClock_TimerFires(t *testing.T) {
	tm := reconcile.RealClock{}.NewTimer(10 * time.Millisecond)

	select {
	case <-tm.C():
	case <-time.After(2 * time.Second):
		t.Fatal("real timer did not fire")
	}
}
