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
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/signal"
	"github.com/pion/stun/v3"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestIsUpgradeSignal(t *testing.T) {
	plain := &signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN}
	if isUpgradeSignal(plain) {
		t.Fatal("a plain SYN is not an upgrade signal")
	}
	markedSyn := &signal.SignalPacket{Handshake: &signal.Handshake{IsUpgradeProbe: true, AttemptID: "a1"}}
	if !isUpgradeSignal(markedSyn) {
		t.Fatal("a marked SYN is an upgrade signal")
	}
	markedOffer := &signal.SignalPacket{Offer: &signal.Offer{AttemptID: "a1"}}
	if !isUpgradeSignal(markedOffer) {
		t.Fatal("an OFFER with an attempt id is an upgrade signal")
	}
	plainOffer := &signal.SignalPacket{Offer: &signal.Offer{Ufrag: "u"}}
	if isUpgradeSignal(plainOffer) {
		t.Fatal("a plain OFFER is not an upgrade signal")
	}
}

// newShadowTestProbe builds a relay-ready probe like newUpgradeProbe does,
// but without the upgrade hook so the real beginUpgradeAttempt path runs.
func newShadowTestProbe(t *testing.T, initiator bool) (*Probe, *fakeConfigurator) {
	t.Helper()
	big := infra.NewPeerIdentity("big", wgtypes.Key{2})
	small := infra.NewPeerIdentity("small", wgtypes.Key{1})
	local, remote := small, big
	if initiator {
		local, remote = big, small
	}
	cfg := &fakeConfigurator{}
	p := &Probe{
		sm:           NewStateMachine(StateProbing),
		localId:      local,
		remoteId:     remote,
		log:          log.GetLogger("test-probe"),
		configurator: cfg,
	}
	return p, cfg
}

// Failed attempts must advance the backoff and leave the probe relay-ready:
// the whole point of ADR-0007 is that a failed upgrade costs nothing.
func TestUpgrade_TriesPersistAcrossFailedShadowAttempts(t *testing.T) {
	fastUpgrade(t)
	p, cfg := newShadowTestProbe(t, true)
	var fired atomic.Int32
	p.startUpgradeShadow = func(uint64) { fired.Add(1) }
	p.newShadow = func(attemptID string, onResult func(infra.Transport, error)) *shadowUpgrade {
		return newShadowUpgrade(&shadowConfig{log: p.log, attemptID: attemptID, onResult: onResult})
	}

	p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})
	waitUntil(t, time.Second, func() bool { return fired.Load() == 1 }, "first attempt fired by the timer")

	// The attempt fails (say the 60 s shadow budget ran out); the outcome lands
	// through the same onResult closure a real shadow reports through.
	p.onShadowResult(p.epoch.Load(), nil, context.DeadlineExceeded)

	waitUntil(t, time.Second, func() bool { return fired.Load() == 2 }, "second attempt after the backoff")

	p.upgradeMu.Lock()
	tries := p.upgradeTries
	p.upgradeMu.Unlock()
	if tries != 2 {
		t.Fatalf("upgradeTries = %d after two failed attempts, want 2", tries)
	}
	if got := p.sm.Current(); got != StateRelayReady {
		t.Fatalf("state = %s after failed attempts, want relay-ready", got)
	}
	if n := cfg.removeCount(); n != 0 {
		t.Fatalf("failed attempts removed the WG peer %d time(s)", n)
	}
}

// A result from an attempt that lost an epoch race (a restart happened since)
// must not touch the probe.
func TestShadow_StaleEpochIsIgnored(t *testing.T) {
	p, cfg := newShadowTestProbe(t, true)
	p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})
	stale := p.epoch.Load()
	p.epoch.Add(1) // a restart happened since the attempt started

	p.onShadowResult(stale, &mockTransport{tp: infra.ICE, addr: "5.6.7.8:9"}, nil)

	if got := p.sm.Current(); got != StateRelayReady {
		t.Fatalf("state = %s after a stale result, want relay-ready", got)
	}
	if cfg.count() != 0 {
		t.Fatalf("stale result SetEndpoint %d time(s)", cfg.count())
	}
}

// Success must hand off in place: exactly one SetEndpoint, zero RemovePeer,
// RelayReady→ICEReady. The transition callback mirrors the factory wiring.
func TestShadow_SuccessHandsOffWithSetEndpointOnly(t *testing.T) {
	p, cfg := newShadowTestProbe(t, true)
	persistentKA := keepaliveFor(p.localId, p.remoteId)
	pubKey := p.remoteId.PublicKey.String()
	p.sm.OnTransition(func(from, to PeerState) {
		if from == StateRelayReady && to == StateICEReady {
			p.mu.Lock()
			tr := p.currentTransport
			p.mu.Unlock()
			if tr == nil {
				return
			}
			_ = p.configurator.SetEndpoint(pubKey, tr.RemoteAddr(), persistentKA) //nolint:errcheck
		}
	})

	p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})
	p.onShadowResult(p.epoch.Load(), &mockTransport{tp: infra.ICE, addr: "5.6.7.8:9"}, nil)

	if got := p.sm.Current(); got != StateICEReady {
		t.Fatalf("state = %s after a successful upgrade, want ice-ready", got)
	}
	if n := cfg.count(); n != 1 {
		t.Fatalf("SetEndpoint called %d time(s), want exactly 1", n)
	}
	if got := cfg.calls[0]; got != "5.6.7.8:9" {
		t.Fatalf("SetEndpoint endpoint = %q, want the verified direct address", got)
	}
	if n := cfg.removeCount(); n != 0 {
		t.Fatalf("successful upgrade removed the WG peer %d time(s)", n)
	}
	p.upgradeMu.Lock()
	tries := p.upgradeTries
	p.upgradeMu.Unlock()
	if tries != 0 {
		t.Fatalf("upgradeTries = %d after reaching direct, want the backoff reset", tries)
	}
}

// The responder only opens a shadow for a marked SYN while relayed.
func TestShadow_SynOutsideRelayReadySpawnsNothing(t *testing.T) {
	p, _ := newShadowTestProbe(t, false)
	p.onSuccess(&mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}) // ice-ready
	var spawns atomic.Int32
	p.newShadow = func(string, func(infra.Transport, error)) *shadowUpgrade {
		spawns.Add(1)
		return newShadowUpgrade(&shadowConfig{log: p.log})
	}

	syn := &signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN, Handshake: &signal.Handshake{IsUpgradeProbe: true, AttemptID: "a1"}}
	if err := p.Handle(context.Background(), p.remoteId, syn); err != nil {
		t.Fatal(err)
	}
	if n := spawns.Load(); n != 0 {
		t.Fatalf("spawned %d shadow(s) while ice-ready", n)
	}
}

// One in-flight shadow per remote: a second marked SYN is answered by the
// existing shadow and spawns nothing new.
func TestShadow_SingleFlight(t *testing.T) {
	p, _ := newShadowTestProbe(t, false)
	p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})

	var handles atomic.Int32
	p.newShadow = func(attemptID string, onResult func(infra.Transport, error)) *shadowUpgrade {
		s := newShadowUpgrade(&shadowConfig{log: p.log, attemptID: attemptID, onResult: onResult})
		s.testHandle = func(context.Context, *signal.SignalPacket) error {
			handles.Add(1)
			return nil
		}
		return s
	}

	syn := func(id string) *signal.SignalPacket {
		return &signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN, Handshake: &signal.Handshake{IsUpgradeProbe: true, AttemptID: id}}
	}
	if err := p.Handle(context.Background(), p.remoteId, syn("a1")); err != nil {
		t.Fatal(err)
	}
	if err := p.Handle(context.Background(), p.remoteId, syn("a2")); err != nil {
		t.Fatal(err)
	}

	if got := p.currentShadow(); got == nil || got.cfg.attemptID != "a1" {
		t.Fatalf("in-flight shadow = %v, want the first attempt to survive", got)
	}
	if n := handles.Load(); n != 2 {
		t.Fatalf("second SYN handled %d time(s) by the existing shadow, want 2 (both SYNs)", n)
	}
}

// The delivered transport must reach the in-flight shadow (both roles), never
// the main dialers — a stray OFFER with an unknown attempt id is dropped.
func TestShadow_StrayOfferWithNoShadowIsDropped(t *testing.T) {
	p, _ := newShadowTestProbe(t, true)
	offer := &signal.SignalPacket{Type: signal.PacketType_OFFER, Offer: &signal.Offer{AttemptID: "zz", Ufrag: "u", Pwd: "p", Candidate: "candidate"}}
	if err := p.Handle(context.Background(), p.remoteId, offer); err != nil {
		t.Fatal(err)
	}
	if s := p.currentShadow(); s != nil {
		t.Fatal("a stray OFFER spawned a shadow")
	}
}

// restart and Close must tear the in-flight shadow down with the probe.
func TestShadow_RestartAndCloseCancelTheShadow(t *testing.T) {
	p, _ := newShadowTestProbe(t, true)
	p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})
	p.startUpgradeShadow = func(uint64) {} // keep the timer out of the way; Handle spawns below
	mux := newLoopbackMux(t)
	p.newShadow = func(attemptID string, onResult func(infra.Transport, error)) *shadowUpgrade {
		return newShadowUpgrade(&shadowConfig{
			log: p.log, mux: mux, attemptID: attemptID, onResult: onResult,
			sender: func(context.Context, infra.PeerID, []byte, time.Time) error { return nil },
		})
	}
	syn := &signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN, Handshake: &signal.Handshake{IsUpgradeProbe: true, AttemptID: "a1"}}
	// Responder side: the SYN spawns the shadow we then expect cleaned up.
	if err := p.Handle(context.Background(), p.remoteId, syn); err != nil {
		t.Fatal(err)
	}
	s := p.currentShadow()
	if s == nil {
		t.Fatal("marked SYN did not spawn a responder shadow")
	}

	p.cancelShadow()
	if !s.isClosed() {
		t.Fatal("cancelShadow left the shadow open")
	}
	if s := p.currentShadow(); s != nil {
		t.Fatal("cancelShadow left the shadow attached")
	}

	// Close nils the factory hook: onShadowResult must not re-arm.
	p.Close()
	if p.newShadow != nil {
		t.Fatal("Close kept the shadow factory hook")
	}
	p.onShadowResult(p.epoch.Load(), &mockTransport{tp: infra.ICE, addr: "5.6.7.8:9"}, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// End-to-end: two probes, real shadow dialers, real ICE over the local
// interfaces, signaling fed in memory. This is the make-before-break
// assertion: the upgrade converges without a single restart or WG removal.
func TestShadow_MakeBeforeBreakUpgradeEndToEnd(t *testing.T) {
	if !hasNonLoopbackIPv4() {
		t.Skip("no non-loopback IPv4 interface; ICE has nothing to dial")
	}
	prevStun := stunURIsFn
	stunURIsFn = func() []*stun.URI { return nil } // host-only candidates
	t.Cleanup(func() { stunURIsFn = prevStun })

	fastUpgrade(t)

	big := infra.NewPeerIdentity("big", wgtypes.Key{2})
	small := infra.NewPeerIdentity("small", wgtypes.Key{1})

	muxA := newLoopbackMux(t)
	muxB := newLoopbackMux(t)

	// In-memory signaling: whatever a probe's shadow sends is unmarshaled and
	// handed to the other probe's Handle — the same path NATS/relay take.
	// Delivery is asynchronous and per-direction ordered (a single drain
	// goroutine), because answering a SYN re-enters the sender's probe with an
	// ACK: a synchronous pipe would deadlock on itself, which no real
	// NATS/relay channel does.
	var (
		deliverToB func(ctx context.Context, pkt *signal.SignalPacket) error
		deliverToA func(ctx context.Context, pkt *signal.SignalPacket) error
	)
	pipe := func(target func(ctx context.Context, pkt *signal.SignalPacket) error) func(context.Context, infra.PeerID, []byte, time.Time) error {
		ch := make(chan *signal.SignalPacket, 64)
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case pkt := <-ch:
					_ = target(context.Background(), pkt)
				case <-stop:
					return
				}
			}
		}()
		t.Cleanup(func() { close(stop); <-done })
		return func(_ context.Context, _ infra.PeerID, data []byte, _ time.Time) error {
			var pkt signal.SignalPacket
			if err := json.Unmarshal(data, &pkt); err != nil {
				return err
			}
			select {
			case ch <- &pkt:
			case <-stop: // torn down; drop
			default: // backed up: upgrade signaling is best-effort, drop
			}
			return nil
		}
	}

	newE2EProbe := func(local, remote infra.PeerIdentity, mux *infra.FilteringUDPMux, send func(context.Context, infra.PeerID, []byte, time.Time) error) (*Probe, *fakeConfigurator) {
		p, cfg := newShadowTestProbe(t, isInitiator(local, remote))
		p.newShadow = func(attemptID string, onResult func(infra.Transport, error)) *shadowUpgrade {
			return newShadowUpgrade(&shadowConfig{
				log:       p.log,
				localId:   local,
				remoteId:  remote,
				sender:    send,
				mux:       mux,
				attemptID: attemptID,
				onResult:  onResult,
			})
		}
		persistentKA := keepaliveFor(local, remote)
		pubKey := remote.PublicKey.String()
		p.sm.OnTransition(func(from, to PeerState) {
			if from == StateRelayReady && to == StateICEReady {
				p.mu.Lock()
				tr := p.currentTransport
				p.mu.Unlock()
				if tr == nil {
					return
				}
				_ = cfg.SetEndpoint(pubKey, tr.RemoteAddr(), persistentKA) //nolint:errcheck
			}
		})
		p.onSuccess(&mockTransport{tp: infra.Relay, addr: "fake"})
		return p, cfg
	}

	var a, b *Probe
	var cfgA, cfgB *fakeConfigurator
	// Resolve the mutual indirection once both closures exist.
	var wireA, wireB func(ctx context.Context, pkt *signal.SignalPacket) error
	deliverToB = func(ctx context.Context, pkt *signal.SignalPacket) error { return wireB(ctx, pkt) }
	deliverToA = func(ctx context.Context, pkt *signal.SignalPacket) error { return wireA(ctx, pkt) }

	a, cfgA = newE2EProbe(big, small, muxA, pipe(deliverToB))
	b, cfgB = newE2EProbe(small, big, muxB, pipe(deliverToA))
	wireA = func(ctx context.Context, pkt *signal.SignalPacket) error { return a.Handle(ctx, a.remoteId, pkt) }
	wireB = func(ctx context.Context, pkt *signal.SignalPacket) error { return b.Handle(ctx, b.remoteId, pkt) }

	deadline := 15 * time.Second
	waitUntil(t, deadline, func() bool { return a.sm.Current() == StateICEReady }, "initiator upgraded to ice-ready")
	waitUntil(t, deadline, func() bool { return b.sm.Current() == StateICEReady }, "responder upgraded to ice-ready")

	for name, tc := range map[string]struct{ cfg *fakeConfigurator }{
		"initiator": {cfgA},
		"responder": {cfgB},
	} {
		if n := tc.cfg.count(); n != 1 {
			t.Fatalf("%s: SetEndpoint called %d time(s), want exactly 1", name, n)
		}
		if n := tc.cfg.removeCount(); n != 0 {
			t.Fatalf("%s: WG peer removed %d time(s) during the upgrade", name, n)
		}
	}

	// The relay session must have survived untouched (make-before-break).
	if a.currentTransport == nil || a.currentTransport.Type() != infra.ICE {
		t.Fatal("initiator current transport is not the direct path")
	}
	a.Close()
	b.Close()
}

func newLoopbackMux(t *testing.T) *infra.FilteringUDPMux {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: nil, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	mux := infra.NewFilteringUDPMux(conn, nil)
	mux.Start()
	t.Cleanup(func() { _ = mux.Close() })
	return mux
}

func hasNonLoopbackIPv4() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				return true
			}
		}
	}
	return false
}

// A SYN marked as an upgrade probe must never look like a remote restart to
// the relay dialer, whatever the session age (ADR-0007 step 4).
func TestRelayDialer_MarkedSynOnActiveSessionDoesNotRestart(t *testing.T) {
	var restarts atomic.Int32
	d, sent := newSynTestDialer(t, &restarts)
	d.mu.Lock()
	d.active, d.activeAt = true, time.Now().Add(-2*relaySynGrace) // well past the grace
	d.mu.Unlock()

	marked := &signal.SignalPacket{
		Type:      signal.PacketType_HANDSHAKE_SYN,
		Dialer:    signal.DialerType_ICE,
		Handshake: &signal.Handshake{IsUpgradeProbe: true, AttemptID: "a1"},
	}
	if err := d.Handle(context.Background(), d.remoteId, marked); err != nil {
		t.Fatal(err)
	}

	if n := restarts.Load(); n != 0 {
		t.Fatalf("a marked SYN restarted the dialer %d time(s)", n)
	}
	if n := sent.count(signal.PacketType_HANDSHAKE_ACK); n != 0 {
		t.Fatalf("the main dialer answered a marked SYN %d time(s)", n)
	}
}
