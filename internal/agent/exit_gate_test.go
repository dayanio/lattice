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
	"testing"
	"time"
)

// healthySample models a working path: keepalives keep the rx counters
// moving every 25 s, the WG session rekeys on its ~120-180 s cycle.
func healthySample(i int) PeerStat {
	return PeerStat{
		State:        "relay-ready",
		RxBytes:      uint64(1000 + i*64),
		TxBytes:      uint64(1000 + i*64),
		HandshakeAgo: int64((i * 25) % 180),
	}
}

func TestExitPathGateHealthyIdleStaysAlive(t *testing.T) {
	var g ExitPathGate
	base := time.Now()
	for i := 0; i <= 16; i++ { // ~7 minutes of healthy keepalive
		if g.Evaluate(healthySample(i), base.Add(time.Duration(i*25)*time.Second)) {
			t.Fatalf("healthy path reported dead at sample %d", i)
		}
	}
}

func TestExitPathGateFailsOpenOnStall(t *testing.T) {
	var g ExitPathGate
	base := time.Now()
	for i := 0; i < 5; i++ {
		g.Evaluate(healthySample(i), base.Add(time.Duration(i*25)*time.Second))
	}
	// Provider dies: probe still says relay-ready (it probes over the
	// relay), but rx freezes and the WG handshake ages without renewal.
	dead := false
	deadAt := time.Duration(0)
	for s := 1; s <= 6; s++ {
		st := PeerStat{State: "relay-ready", RxBytes: 1320, TxBytes: 1320, HandshakeAgo: int64(30 + s*25)}
		at := base.Add(2*time.Minute + time.Duration(s*25)*time.Second)
		if g.Evaluate(st, at) {
			dead = true
			deadAt = at.Sub(base)
			break
		}
	}
	if !dead {
		t.Fatal("stalled data path never reported dead")
	}
	if deadAt < 60*time.Second {
		t.Errorf("fail-open fired before the stall threshold (%s)", deadAt)
	}
}

func TestExitPathGateRevivesOnProgress(t *testing.T) {
	var g ExitPathGate
	base := time.Now()
	for i := 0; i < 5; i++ {
		g.Evaluate(healthySample(i), base.Add(time.Duration(i*25)*time.Second))
	}
	// stall into fail-open
	for s := 1; s <= 6; s++ {
		st := PeerStat{State: "relay-ready", RxBytes: 1320, TxBytes: 1320, HandshakeAgo: int64(30 + s*25)}
		if g.Evaluate(st, base.Add(2*time.Minute+time.Duration(s*25)*time.Second)) {
			break
		}
	}
	// provider back: fresh handshake and counters move again
	revived := g.Evaluate(PeerStat{State: "relay-ready", RxBytes: 5000, TxBytes: 5000, HandshakeAgo: 2},
		base.Add(5*time.Minute))
	if revived {
		t.Fatal("revived path still reported dead")
	}
}

func TestExitPathGateFailedStateFailsOpenOnlyAfterReady(t *testing.T) {
	var g ExitPathGate
	base := time.Now()
	// Initial connect, never came up: no fail-open (server withdrawal's job).
	for s := 0; s < 10; s++ {
		st := PeerStat{State: "failed", HandshakeAgo: -1}
		if g.Evaluate(st, base.Add(time.Duration(s*30)*time.Second)) {
			t.Fatal("never-ready connect must not fail open")
		}
	}
	// Path that worked and then failed: fail-open immediately.
	var g2 ExitPathGate
	for i := 0; i < 3; i++ {
		g2.Evaluate(healthySample(i), base.Add(time.Duration(i*25)*time.Second))
	}
	if !g2.Evaluate(PeerStat{State: "failed", RxBytes: 1192, TxBytes: 1192, HandshakeAgo: 40},
		base.Add(2*time.Minute)) {
		t.Fatal("worked-then-failed path must fail open")
	}
}

func TestExitPathGateNeverReadyProbingStaysAlive(t *testing.T) {
	var g ExitPathGate
	base := time.Now()
	for s := 0; s < 20; s++ {
		st := PeerStat{State: "probing", HandshakeAgo: -1}
		if g.Evaluate(st, base.Add(time.Duration(s*30)*time.Second)) {
			t.Fatal("probing without ever-ready must not fail open")
		}
	}
}
