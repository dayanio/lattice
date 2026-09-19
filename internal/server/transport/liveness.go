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

import "time"

// PeerStats is the WireGuard-side view of a peer used for liveness checks.
type PeerStats struct {
	LastHandshake time.Time
	RxBytes       uint64
}

// rxStallThreshold is how long a keepalive-armed peer may go without any
// received byte before its path is declared dead. WireGuard keepalives arrive
// every 25 s, so this tolerates two consecutive lost keepalives.
const rxStallThreshold = 60 * time.Second

type livenessVerdict int

const (
	livenessAlive livenessVerdict = iota
	livenessHandshakeStale
	livenessRxStalled
)

// rxCadenceMax is the largest gap between rx growth events that still looks
// like a keepalive rhythm; rxArmAfter consecutive such events arm the fast
// check for a peer. Peers that never send keepalives only grow rx when a
// handshake happens (every ~2 min), which never satisfies this.
const (
	rxCadenceMax = 40 * time.Second
	rxArmAfter   = 3
)

// livenessTracker decides whether an established peer is still delivering.
//
// Two independent signals:
//   - the WireGuard handshake age (slow, works for every peer version), and
//   - received-byte growth (fast), used only once a peer has shown a
//     keepalive-like rhythm, so peers that send no keepalives (older agents)
//     are never declared dead merely for being quiet.
type livenessTracker struct {
	seen         bool
	lastRx       uint64
	lastRxChange time.Time
	lastGrowth   time.Time
	streak       int
}

func newLivenessTracker(now time.Time) *livenessTracker {
	return &livenessTracker{lastRxChange: now}
}

func (t *livenessTracker) observe(now time.Time, s PeerStats) livenessVerdict {
	if s.LastHandshake.IsZero() || now.Sub(s.LastHandshake) > livenessThreshold {
		return livenessHandshakeStale
	}

	switch {
	case !t.seen:
		t.seen = true
		t.lastRx = s.RxBytes
	case s.RxBytes < t.lastRx:
		// Counter restarted (device recreated): re-baseline, no verdict.
		t.lastRx = s.RxBytes
		t.lastRxChange = now
		t.streak = 0
	case s.RxBytes > t.lastRx:
		if !t.lastGrowth.IsZero() && now.Sub(t.lastGrowth) <= rxCadenceMax {
			t.streak++
		} else {
			t.streak = 1
		}
		t.lastRx = s.RxBytes
		t.lastRxChange = now
		t.lastGrowth = now
	}

	if t.streak >= rxArmAfter && now.Sub(t.lastRxChange) > rxStallThreshold {
		return livenessRxStalled
	}
	return livenessAlive
}
