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
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var (
	relayAddr  = &net.UDPAddr{IP: net.ParseIP("fd6c:7270::e833:e4a3"), Port: 51820}
	directAddr = &net.UDPAddr{IP: net.ParseIP("45.8.204.88"), Port: 35062}
)

func TestRelayPoisoned(t *testing.T) {
	for name, tc := range map[string]struct {
		state PeerState
		ep    *net.UDPAddr
		want  bool
	}{
		"ice-ready pointed at the relay":       {StateICEReady, relayAddr, true},
		"ice-ready with a real address":        {StateICEReady, directAddr, false},
		"ice-ready without an endpoint":        {StateICEReady, nil, false},
		"relayed probe on the relay is normal": {StateRelayReady, relayAddr, false},
		"failed probe":                         {StateFailed, relayAddr, false},
	} {
		if got := relayPoisoned(tc.state, tc.ep); got != tc.want {
			t.Errorf("%s: relayPoisoned(%s, %v) = %v, want %v", name, tc.state, tc.ep, got, tc.want)
		}
	}
}

// fakeConfigurator records SetEndpoint calls.
type fakeConfigurator struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeConfigurator) RegisterPeer(string, string) error { return nil }
func (f *fakeConfigurator) RemovePeer(string) error           { return nil }
func (f *fakeConfigurator) ApplyRoute(string, string) error   { return nil }
func (f *fakeConfigurator) SetupNAT(string) error             { return nil }
func (f *fakeConfigurator) SetEndpoint(pub, endpoint string, ka int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, endpoint)
	return nil
}
func (f *fakeConfigurator) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

// newIceReadyProbe builds an ice-ready probe whose WireGuard endpoint is
// reported by endpoint().
func newIceReadyProbe(t *testing.T, tr infra.Transport, endpoint func() *net.UDPAddr) (*Probe, *fakeConfigurator) {
	t.Helper()
	cfg := &fakeConfigurator{}
	p := &Probe{
		sm:               NewStateMachine(StateProbing),
		localId:          infra.NewPeerIdentity("local", wgtypes.Key{2}),
		remoteId:         infra.NewPeerIdentity("remote", wgtypes.Key{1}),
		log:              log.GetLogger("test-probe"),
		configurator:     cfg,
		currentTransport: tr,
		getStats: func(string) (PeerStats, error) {
			return PeerStats{LastHandshake: time.Now(), Endpoint: endpoint()}, nil
		},
	}
	if err := p.sm.Transition(StateICEReady); err != nil {
		t.Fatal(err)
	}
	return p, cfg
}

func TestReassertDirectEndpoint_RepairsARelayContaminatedEndpoint(t *testing.T) {
	p, cfg := newIceReadyProbe(t, &mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}, func() *net.UDPAddr { return relayAddr })

	if !p.reassertDirectEndpoint() {
		t.Fatal("a contaminated endpoint must be repaired")
	}
	if cfg.count() != 1 || cfg.calls[0] != "1.2.3.4:5" {
		t.Fatalf("SetEndpoint calls = %v, want one call with the ICE address", cfg.calls)
	}
}

func TestReassertDirectEndpoint_LeavesAHealthyEndpointAlone(t *testing.T) {
	p, cfg := newIceReadyProbe(t, &mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}, func() *net.UDPAddr { return directAddr })

	if p.reassertDirectEndpoint() || cfg.count() != 0 {
		t.Fatalf("a real endpoint (even one WireGuard roamed to) must not be touched, calls=%v", cfg.calls)
	}
}

func TestReassertDirectEndpoint_NeverPointsAtANonICETransport(t *testing.T) {
	p, cfg := newIceReadyProbe(t, &mockTransport{tp: infra.Relay, addr: "fake"}, func() *net.UDPAddr { return relayAddr })

	if p.reassertDirectEndpoint() || cfg.count() != 0 {
		t.Fatalf("no ICE address to assert, calls=%v", cfg.calls)
	}
}

func fastGuard(t *testing.T) {
	t.Helper()
	prev := endpointGuardDelays
	endpointGuardDelays = []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 60 * time.Millisecond}
	t.Cleanup(func() { endpointGuardDelays = prev })
}

// The contamination comes from relay packets arriving in the moments around
// the switch, so the repair has to keep watching for a while and stop once the
// endpoint is right.
func TestEndpointGuard_RepairsUntilTheEndpointIsRight(t *testing.T) {
	fastGuard(t)
	var contaminated atomic.Bool
	contaminated.Store(true)
	var cfg *fakeConfigurator
	p, c := newIceReadyProbe(t, &mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}, func() *net.UDPAddr {
		if contaminated.Load() {
			return relayAddr
		}
		return directAddr
	})
	cfg = c
	// Repairing flips the fake endpoint back, as SetEndpoint would.
	orig := p.configurator
	p.configurator = &flipConfigurator{ConnectionConfigurator: orig, onSet: func() { contaminated.Store(false) }}

	p.startEndpointGuard()

	waitUntil(t, time.Second, func() bool { return cfg.count() >= 1 }, "guard must repair the endpoint")
	time.Sleep(150 * time.Millisecond)
	if n := cfg.count(); n != 1 {
		t.Fatalf("guard kept re-pointing a healthy endpoint: %d calls", n)
	}
}

func TestEndpointGuard_StopsWhenTheProbeLeavesIceReady(t *testing.T) {
	fastGuard(t)
	p, cfg := newIceReadyProbe(t, &mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}, func() *net.UDPAddr { return relayAddr })

	p.startEndpointGuard()
	_ = p.sm.Transition(StateFailed)

	time.Sleep(150 * time.Millisecond)
	if n := cfg.count(); n != 0 {
		t.Fatalf("guard acted on a probe that is no longer ice-ready: %d calls", n)
	}
}

// flipConfigurator runs onSet after every SetEndpoint.
type flipConfigurator struct {
	ConnectionConfigurator
	onSet func()
}

func (f *flipConfigurator) SetEndpoint(pub, endpoint string, ka int) error {
	err := f.ConnectionConfigurator.SetEndpoint(pub, endpoint, ka)
	f.onSet()
	return err
}
