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

package transport

import (
	"testing"
	"time"
)

const tick = livenessInterval

// simulate feeds the tracker one observation per liveness tick. rxAt returns
// the cumulative rx byte count at elapsed time d; hsAt the last handshake.
func simulate(t *testing.T, total time.Duration, rxAt func(d time.Duration) uint64, hsAge func(d time.Duration) time.Duration) (firstBad time.Duration, verdict livenessVerdict) {
	t.Helper()
	start := time.Unix(1_000_000, 0)
	tr := newLivenessTracker(start)
	for d := tick; d <= total; d += tick {
		now := start.Add(d)
		v := tr.observe(now, PeerStats{LastHandshake: now.Add(-hsAge(d)), RxBytes: rxAt(d)})
		if v != livenessAlive {
			return d, v
		}
	}
	return 0, livenessAlive
}

// A peer that keeps a WireGuard keepalive going (25 s cadence) is armed for
// the fast check, and a path that dies is caught in about a minute instead of
// waiting for the 180 s handshake threshold.
func TestLiveness_KeepalivePeerStallDetectedQuickly(t *testing.T) {
	deadAt := 10 * time.Minute
	rx := func(d time.Duration) uint64 {
		if d > deadAt {
			d = deadAt
		}
		return uint64(d/(25*time.Second)) * 32
	}
	// The handshake stays "fresh" (rekeyed 100 s ago) so only rx can catch it.
	when, v := simulate(t, deadAt+5*time.Minute, rx, func(time.Duration) time.Duration { return 100 * time.Second })
	if v != livenessRxStalled {
		t.Fatalf("verdict = %v, want rx-stalled", v)
	}
	if lag := when - deadAt; lag < rxStallThreshold-tick || lag > rxStallThreshold+2*tick {
		t.Fatalf("stall detected %v after the path died, want about %v", lag, rxStallThreshold)
	}
}

func TestLiveness_KeepalivePeerHealthyForever(t *testing.T) {
	rx := func(d time.Duration) uint64 { return uint64(d/(25*time.Second)) * 32 }
	if when, v := simulate(t, time.Hour, rx, func(time.Duration) time.Duration { return 90 * time.Second }); v != livenessAlive {
		t.Fatalf("healthy keepalive peer declared %v at %v", v, when)
	}
}

// A peer that sends no keepalives (older agent, one-sided keepalive) only
// grows rx when a handshake happens, every ~2 minutes. That cadence must
// never arm the fast check, or such peers would be restarted every minute.
func TestLiveness_PeerWithoutKeepalivesIsNeverRxStalled(t *testing.T) {
	rx := func(d time.Duration) uint64 { return uint64(d/(2*time.Minute)) * 148 }
	if when, v := simulate(t, time.Hour, rx, func(d time.Duration) time.Duration { return d % (2 * time.Minute) }); v != livenessAlive {
		t.Fatalf("idle peer without keepalives declared %v at %v", v, when)
	}
}

func TestLiveness_StaleHandshakeStillDetected(t *testing.T) {
	rx := func(d time.Duration) uint64 { return uint64(d/(25*time.Second)) * 32 }
	_, v := simulate(t, 10*time.Minute, rx, func(d time.Duration) time.Duration {
		if d > 5*time.Minute {
			return livenessThreshold + time.Second
		}
		return 60 * time.Second
	})
	if v != livenessHandshakeStale {
		t.Fatalf("verdict = %v, want handshake-stale", v)
	}
}

func TestLiveness_NoHandshakeYetIsStale(t *testing.T) {
	tr := newLivenessTracker(time.Unix(1_000_000, 0))
	if v := tr.observe(time.Unix(1_000_015, 0), PeerStats{}); v != livenessHandshakeStale {
		t.Fatalf("verdict = %v, want handshake-stale for a zero handshake time", v)
	}
}

// Device recreated: the kernel counter restarts from zero.
func TestLiveness_CounterResetIsNotAStall(t *testing.T) {
	start := time.Unix(1_000_000, 0)
	tr := newLivenessTracker(start)
	hs := func(now time.Time) time.Time { return now.Add(-30 * time.Second) }
	var rx uint64
	for d := tick; d <= 3*time.Minute; d += tick {
		now := start.Add(d)
		rx += 32
		if d == 2*time.Minute {
			rx = 0
		}
		if v := tr.observe(now, PeerStats{LastHandshake: hs(now), RxBytes: rx}); v != livenessAlive {
			t.Fatalf("verdict %v at %v after a counter reset", v, d)
		}
	}
}
