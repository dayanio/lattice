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
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func fastPathPing(t *testing.T) {
	t.Helper()
	pi, pt := pathPingInterval, pathPingTimeout
	pathPingInterval, pathPingTimeout = 10*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { pathPingInterval, pathPingTimeout = pi, pt })
}

// scriptedPinger answers according to a function of the call number (1-based).
type scriptedPinger struct {
	mu    sync.Mutex
	calls int
	addrs []string
	reply func(n int) bool
}

func (s *scriptedPinger) ping(_ context.Context, addr string, _ time.Duration) (time.Duration, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.addrs = append(s.addrs, addr)
	s.mu.Unlock()
	if s.reply(n) {
		return time.Millisecond, nil
	}
	return 0, errors.New("no reply")
}

func (s *scriptedPinger) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func newPingProbe(t *testing.T, initiator bool, s *scriptedPinger, restarted *atomic.Int32) *Probe {
	t.Helper()
	big := infra.NewPeerIdentity("big", wgtypes.Key{2})
	small := infra.NewPeerIdentity("small", wgtypes.Key{1})
	local, remote := small, big
	if initiator {
		local, remote = big, small
	}
	p := &Probe{
		sm:               NewStateMachine(StateProbing),
		localId:          local,
		remoteId:         remote,
		log:              log.GetLogger("test-probe"),
		currentTransport: &mockTransport{tp: infra.ICE, addr: "203.0.113.9:4000"},
		pathPing:         s.ping,
		pathRestart:      func() { restarted.Add(1) },
	}
	if err := p.sm.Transition(StateICEReady); err != nil {
		t.Fatal(err)
	}
	return p
}

// The whole point: a direct path that stops answering (either direction) is
// caught in about pathPingMisses intervals, not after the 180 s handshake rule.
func TestPathPing_ArmedPeerThatGoesSilentTriggersARestart(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(n int) bool { return n <= 3 }} // alive, then the path dies
	p := newPingProbe(t, true, s, &restarted)

	p.startPathPing()

	waitUntil(t, 2*time.Second, func() bool { return restarted.Load() == 1 }, "a dead direct path must restart the probe")
	if got := s.count(); got < 3+pathPingMisses {
		t.Fatalf("declared dead after only %d pings, want 3 answered + %d missed", got, pathPingMisses)
	}
	if s.addrs[0] != "203.0.113.9:4000" {
		t.Fatalf("pinged %q, want the ICE address", s.addrs[0])
	}
}

func TestPathPing_OccasionalLossIsTolerated(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	// answered, then every third ping lost: never pathPingMisses in a row
	s := &scriptedPinger{reply: func(n int) bool { return n%3 != 0 }}
	p := newPingProbe(t, true, s, &restarted)

	p.startPathPing()

	waitUntil(t, 2*time.Second, func() bool { return s.count() >= 20 }, "pinging continues")
	if n := restarted.Load(); n != 0 {
		t.Fatalf("restarted %d time(s) over a link that only drops the odd ping", n)
	}
}

// An older peer never answers; treating that as a dead path would restart a
// healthy connection every half minute.
func TestPathPing_PeerThatNeverAnswersIsNeverDeclaredDead(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(int) bool { return false }}
	p := newPingProbe(t, true, s, &restarted)

	p.startPathPing()

	waitUntil(t, 2*time.Second, func() bool { return s.count() >= pathPingGiveUp }, "gives up after pathPingGiveUp pings")
	time.Sleep(100 * time.Millisecond)
	if n := restarted.Load(); n != 0 {
		t.Fatalf("restarted %d time(s) for a peer that never supported echo", n)
	}
	if got := s.count(); got != pathPingGiveUp {
		t.Fatalf("kept pinging an unresponsive peer: %d pings, want it to stop at %d", got, pathPingGiveUp)
	}
}

func TestPathPing_ResponderDoesNotPing(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(int) bool { return true }}
	p := newPingProbe(t, false, s, &restarted)

	p.startPathPing()

	time.Sleep(100 * time.Millisecond)
	if n := s.count(); n != 0 {
		t.Fatalf("responder sent %d pings; only the initiator watches the path", n)
	}
}

func TestPathPing_StopsWhenTheProbeLeavesIceReady(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(int) bool { return true }}
	p := newPingProbe(t, true, s, &restarted)

	p.startPathPing()
	waitUntil(t, time.Second, func() bool { return s.count() >= 2 }, "pinging started")
	_ = p.sm.Transition(StateFailed)
	time.Sleep(50 * time.Millisecond) // let an in-flight ping finish
	before := s.count()
	time.Sleep(100 * time.Millisecond)
	if after := s.count(); after != before {
		t.Fatalf("kept pinging after leaving ice-ready: %d -> %d", before, after)
	}
}

func TestPathPing_NoPingerMeansNoCheck(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(int) bool { return false }}
	p := newPingProbe(t, true, s, &restarted)
	p.pathPing = nil

	p.startPathPing() // must not panic

	time.Sleep(50 * time.Millisecond)
	if restarted.Load() != 0 {
		t.Fatal("restarted without a pinger")
	}
}

func TestPathPing_StaleEpochStops(t *testing.T) {
	fastPathPing(t)
	var restarted atomic.Int32
	s := &scriptedPinger{reply: func(int) bool { return true }}
	p := newPingProbe(t, true, s, &restarted)

	p.startPathPing()
	waitUntil(t, time.Second, func() bool { return s.count() >= 1 }, "pinging started")
	p.epoch.Add(1) // a restart elsewhere
	time.Sleep(50 * time.Millisecond)
	before := s.count()
	time.Sleep(100 * time.Millisecond)
	if after := s.count(); after != before {
		t.Fatalf("a superseded pinger kept running: %d -> %d", before, after)
	}
}
