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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/relay"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type signalRig struct {
	s          *peerSignaler
	clock      *fakeClock
	natsCalls  int
	relayCalls int
	natsErr    error
	relayErr   error
	natsUp     bool
	relayUp    bool
	relaySeen  [][]byte
}

func newSignalRig(t *testing.T) *signalRig {
	t.Helper()
	prev := relaySignalAfter
	relaySignalAfter = 2 * time.Second
	t.Cleanup(func() { relaySignalAfter = prev })

	r := &signalRig{clock: &fakeClock{t: time.Unix(1000, 0)}, natsUp: true, relayUp: true}
	r.s = newPeerSignaler(log.GetLogger("test-signaler"), "peer-b",
		func(context.Context, infra.PeerID, []byte) error { r.natsCalls++; return r.natsErr },
		func() bool { return r.natsUp },
		func(_ context.Context, _ infra.PeerID, data []byte) error {
			r.relayCalls++
			r.relaySeen = append(r.relaySeen, data)
			return r.relayErr
		},
		func() bool { return r.relayUp },
	)
	r.s.now = r.clock.now
	return r
}

func (r *signalRig) send(t *testing.T, data string) error {
	t.Helper()
	return r.s.Send(context.Background(), infra.PeerID{1}, []byte(data))
}

func TestSignaler_HealthyNATSAndFastProbeNeverTouchesTheRelay(t *testing.T) {
	r := newSignalRig(t)
	r.s.onState(StateCreated, StateProbing)

	r.clock.advance(1500 * time.Millisecond)
	if err := r.send(t, "syn"); err != nil {
		t.Fatal(err)
	}
	if r.natsCalls != 1 || r.relayCalls != 0 {
		t.Fatalf("nats=%d relay=%d, want 1 and 0 inside the first %v", r.natsCalls, r.relayCalls, relaySignalAfter)
	}
}

func TestSignaler_StuckProbeAddsTheRelayAfterTheWindow(t *testing.T) {
	r := newSignalRig(t)
	r.s.onState(StateCreated, StateProbing)

	r.clock.advance(2 * time.Second)
	if err := r.send(t, "syn"); err != nil {
		t.Fatal(err)
	}
	if r.natsCalls != 1 || r.relayCalls != 1 {
		t.Fatalf("nats=%d relay=%d, want both channels once the probe is stuck", r.natsCalls, r.relayCalls)
	}
	if string(r.relaySeen[0]) != "syn" {
		t.Fatalf("relay got %q, want the same packet", r.relaySeen[0])
	}
}

func TestSignaler_NoEscalationOutsideAnAttempt(t *testing.T) {
	r := newSignalRig(t)

	r.clock.advance(time.Hour)
	_ = r.send(t, "restart-notify")
	if r.relayCalls != 0 {
		t.Fatalf("relay used %d time(s) with no probing attempt", r.relayCalls)
	}

	r.s.onState(StateCreated, StateProbing)
	r.clock.advance(5 * time.Second)
	r.s.onState(StateProbing, StateICEReady)
	_ = r.send(t, "late")
	if r.relayCalls != 0 {
		t.Fatalf("relay used %d time(s) after the probe left probing", r.relayCalls)
	}
}

func TestSignaler_ANewAttemptRestartsTheWindow(t *testing.T) {
	r := newSignalRig(t)
	r.s.onState(StateCreated, StateProbing)
	r.clock.advance(5 * time.Second)
	r.s.onState(StateProbing, StateFailed)
	r.s.onState(StateFailed, StateProbing)

	r.clock.advance(time.Second)
	_ = r.send(t, "syn")
	if r.relayCalls != 0 {
		t.Fatalf("relay used %d time(s) 1s into a fresh attempt", r.relayCalls)
	}
}

func TestSignaler_NATSDownGoesStraightToTheRelay(t *testing.T) {
	r := newSignalRig(t)
	r.natsUp = false

	if err := r.send(t, "syn"); err != nil {
		t.Fatalf("relay carried it, want no error: %v", err)
	}
	if r.natsCalls != 0 || r.relayCalls != 1 {
		t.Fatalf("nats=%d relay=%d, want the relay only", r.natsCalls, r.relayCalls)
	}
}

func TestSignaler_NATSErrorFallsBackToTheRelay(t *testing.T) {
	r := newSignalRig(t)
	r.natsErr = errors.New("publish failed")

	if err := r.send(t, "syn"); err != nil {
		t.Fatalf("relay carried it, want no error: %v", err)
	}
	if r.relayCalls != 1 {
		t.Fatalf("relay calls = %d, want 1", r.relayCalls)
	}
}

func TestSignaler_ReportsAnErrorOnlyWhenBothChannelsFail(t *testing.T) {
	r := newSignalRig(t)
	r.natsErr = errors.New("nats broke")
	r.relayErr = errors.New("relay broke")
	err := r.send(t, "syn")
	if err == nil || !strings.Contains(err.Error(), "nats broke") || !strings.Contains(err.Error(), "relay broke") {
		t.Fatalf("err = %v, want both failures", err)
	}

	// NATS fine, relay failing during escalation: the packet did go out.
	r2 := newSignalRig(t)
	r2.s.onState(StateCreated, StateProbing)
	r2.clock.advance(3 * time.Second)
	r2.relayErr = errors.New("relay broke")
	if err := r2.send(t, "syn"); err != nil {
		t.Fatalf("NATS delivered it, want no error: %v", err)
	}
}

func TestSignaler_NoRelayMeansNATSOnly(t *testing.T) {
	r := newSignalRig(t)
	r.relayUp = false
	r.s.onState(StateCreated, StateProbing)
	r.clock.advance(10 * time.Second)

	if err := r.send(t, "syn"); err != nil {
		t.Fatal(err)
	}
	if r.natsCalls != 1 || r.relayCalls != 0 {
		t.Fatalf("nats=%d relay=%d, want NATS only when the relay is down", r.natsCalls, r.relayCalls)
	}

	r.natsUp = false
	if err := r.send(t, "syn"); !errors.Is(err, errNATSDown) {
		t.Fatalf("both channels unavailable: err = %v, want errNATSDown in it", err)
	}
}

func TestSignaler_OversizedPacketStaysOnNATS(t *testing.T) {
	r := newSignalRig(t)
	r.s.onState(StateCreated, StateProbing)
	r.clock.advance(3 * time.Second)

	if err := r.send(t, strings.Repeat("x", relay.MaxProbePayload+1)); err != nil {
		t.Fatalf("NATS delivered it, want no error: %v", err)
	}
	if r.relayCalls != 0 {
		t.Fatalf("relay got an oversized packet; older relay clients would desync on it")
	}
}

func TestSigningPeer_DropsCredentialsAndKeepsTheWireGuardFields(t *testing.T) {
	addr := "10.96.0.4"
	orig := &infra.Peer{
		AppID: "a", Name: "a", PublicKey: "pk", Address: &addr, AllowedIPs: "10.96.0.4/32", Port: 51820,
		Token: "agent-jwt", LrpUrl: "relay.example:6266?token=secret",
	}
	got := signalingPeer(orig)
	if got.Token != "" || got.LrpUrl != "" {
		t.Fatalf("credentials leaked into the signaled peer: token=%q lrp=%q", got.Token, got.LrpUrl)
	}
	if got.AppID != "a" || got.PublicKey != "pk" || got.AllowedIPs != "10.96.0.4/32" || got.Port != 51820 || got.Address == nil {
		t.Fatalf("WireGuard fields lost: %+v", got)
	}
	if orig.Token != "agent-jwt" || orig.LrpUrl == "" {
		t.Fatal("the caller's own record was mutated")
	}
	if signalingPeer(nil) != nil {
		t.Fatal("nil in, nil out")
	}
}
