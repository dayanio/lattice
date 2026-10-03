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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/signal"

	"github.com/pion/ice/v4"
)

// ADR-0007 make-before-break: a relayed probe retries a direct connection by
// negotiating ICE in a shadow dialer beside the working relay transport. The
// shadow is invisible to the state machine and to WireGuard — the WG peer is
// never removed — and on success only the endpoint is switched in place via
// the existing RelayReady→ICEReady callback (probe_factory.go). A failed
// attempt costs nothing: the relay path was never touched.

// upgradeShadowTimeout is the whole budget of one background upgrade attempt:
// SYN retries, candidate gathering and connectivity checks all happen inside
// it. On expiry the attempt is abandoned and the backoff schedule re-arms.
var upgradeShadowTimeout = 60 * time.Second

// upgradeShadowCloseGrace keeps the shadow's ICE agent alive this long after a
// successful handoff, mirroring the 2 s the old relay transport gets in
// handleUpgradeTransport: consent/keepalive STUN keeps the NAT binding warm
// while the first WireGuard packets cross to the new direct endpoint. The
// handed-over transport is only an address (see ICETransport) — closing the
// agent does not affect it; WireGuard roams onto the endpoint on its own.
const upgradeShadowCloseGrace = 2 * time.Second

// shadowSynInterval matches the main dialers' handshake retry cadence.
const shadowSynInterval = 2 * time.Second

var errShadowClosed = errors.New("upgrade shadow closed")

// isUpgradeSignal reports whether a packet belongs to a background upgrade
// probe (ADR-0007) rather than a main-line handshake.
func isUpgradeSignal(p *signal.SignalPacket) bool {
	if hs := p.GetHandshake(); hs != nil && hs.IsUpgradeProbe {
		return true
	}
	if of := p.GetOffer(); of != nil && of.AttemptID != "" {
		return true
	}
	return false
}

// newAttemptID returns a random hex correlation id for one upgrade attempt.
func newAttemptID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

type shadowConfig struct {
	log               *log.Logger
	localId, remoteId infra.PeerIdentity
	// sender is signaler.SendFrom: shadow signaling must keep the
	// NATS→relay escalation available outside any Probing window.
	sender  func(ctx context.Context, to infra.PeerID, data []byte, start time.Time) error
	mux     *infra.FilteringUDPMux
	mux6    *infra.FilteringUDPMux // nil when IPv6 unavailable
	showLog bool
	// attemptID is fresh on the initiator and copied from the received SYN on
	// the responder, so the OFFERs of one attempt correlate across peers.
	attemptID string
	// onResult is invoked exactly once with the outcome.
	onResult func(t infra.Transport, err error)
}

// shadowUpgrade is a slim, state-machine-free iceDialer for one upgrade
// attempt. It reuses the main dialer's agent construction (shared UDP mux,
// same STUN list and timeouts) and mirrors its SYN/ACK/OFFER flow, minus the
// peer-info exchange: both peers already know each other's WG config from the
// relayed session the shadow runs beside.
type shadowUpgrade struct {
	cfg shadowConfig

	mu                 sync.Mutex
	agent              *ice.Agent
	rUfrag, rPwd       string
	pendingCandidates  []ice.Candidate
	gatheredCandidates []ice.Candidate
	closed             bool

	offerReady chan struct{}
	offerOnce  sync.Once
	gatherOnce sync.Once
	closeOnce  sync.Once
	closeChan  chan struct{}
	// cancelSyn stops only the SYN retry loop (the initiator cancels it when
	// the ACK arrives); cancelAll is the whole-attempt deadline cancel. They
	// must be distinct — cancelling the attempt context on ACK would also kill
	// this side's dial.
	cancelSyn  context.CancelFunc
	cancelAll  context.CancelFunc
	resultOnce sync.Once

	// testHandle, when set, replaces Handle (unit tests wire fakes through
	// the newShadow hook, mirroring the startUpgradeShadow/pathRestart hooks).
	testHandle func(ctx context.Context, packet *signal.SignalPacket) error

	startedAt time.Time
}

func newShadowUpgrade(cfg *shadowConfig) *shadowUpgrade {
	return &shadowUpgrade{
		cfg:        *cfg,
		offerReady: make(chan struct{}),
		closeChan:  make(chan struct{}),
	}
}

// start launches the attempt: the initiator runs a marked-SYN retry loop and
// both roles dial (both must reach ICE Connected for the swap to happen on
// both sides). ctx bounds the whole attempt.
func (s *shadowUpgrade) start(ctx context.Context) {
	s.startedAt = time.Now()
	ctx, cancelAll := context.WithTimeout(ctx, upgradeShadowTimeout)
	synCtx, cancelSyn := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancelAll = cancelAll
	s.cancelSyn = cancelSyn
	s.mu.Unlock()

	if isInitiator(s.cfg.localId, s.cfg.remoteId) {
		if err := s.initAgent(); err != nil {
			s.cfg.log.Warn("shadow: init agent failed", "remoteId", s.cfg.remoteId.AppID, "err", err)
			s.finish(nil, err)
			s.Close()
			return
		}
		// Candidates are offered as they gather, like the main dialer; the
		// gather also runs on ACK in case it raced the agent init.
		if err := s.gather(); err != nil {
			s.cfg.log.Warn("shadow: gather failed", "remoteId", s.cfg.remoteId.AppID, "err", err)
			s.finish(nil, err)
			s.Close()
			return
		}
		go s.synLoop(synCtx)
	}
	go s.dial(ctx)
}

func (s *shadowUpgrade) synLoop(ctx context.Context) {
	ticker := time.NewTicker(shadowSynInterval)
	defer ticker.Stop()
	s.sendSyn(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closeChan:
			return
		case <-ticker.C:
			s.sendSyn(ctx)
		}
	}
}

func (s *shadowUpgrade) sendSyn(ctx context.Context) {
	s.send(ctx, &signal.SignalPacket{
		Type:     signal.PacketType_HANDSHAKE_SYN,
		Dialer:   signal.DialerType_ICE,
		SenderID: s.cfg.localId.ID().ToUint64(),
		Handshake: &signal.Handshake{
			Timestamp:      time.Now().Unix(),
			IsUpgradeProbe: true,
			AttemptID:      s.cfg.attemptID,
		},
	})
}

// Handle processes signaling for this attempt. The responder answers SYNs;
// the initiator consumes ACKs and OFFERs.
func (s *shadowUpgrade) Handle(ctx context.Context, packet *signal.SignalPacket) error {
	if s.testHandle != nil {
		return s.testHandle(ctx, packet)
	}
	if s.isClosed() {
		return nil
	}
	switch packet.Type {
	case signal.PacketType_HANDSHAKE_SYN:
		// Responder side. Create the agent on the first SYN; retransmits are
		// answered again (the initiator may have missed the ACK) but never
		// spawn a second agent — one in-flight shadow per remote.
		needAgent := true
		s.mu.Lock()
		if s.agent != nil {
			needAgent = false
		}
		s.mu.Unlock()
		if needAgent {
			if err := s.initAgent(); err != nil {
				return err
			}
		}
		if err := s.sendAck(ctx); err != nil {
			return err
		}
		// Candidates may already be gathered (or gathering finished between
		// SYNs): resend so the initiator cannot stall on a lost OFFER.
		_ = s.gather() //nolint:errcheck // best-effort; the next SYN retries
		s.resendCandidates(ctx)
	case signal.PacketType_HANDSHAKE_ACK:
		// Initiator side: stop the SYN retries and make sure our candidates
		// are on the wire (gather is idempotent; a completed gather resends).
		s.mu.Lock()
		if s.cancelSyn != nil {
			s.cancelSyn()
		}
		s.mu.Unlock()
		if err := s.gather(); err != nil {
			return err
		}
		s.resendCandidates(ctx)
	case signal.PacketType_OFFER:
		offer := packet.GetOffer()
		if offer == nil || offer.AttemptID != s.cfg.attemptID {
			return nil // not this attempt
		}
		s.addRemoteCandidate(offer)
	}
	return nil
}

func (s *shadowUpgrade) initAgent() error {
	agent, err := newICEAgent(iceAgentOpts{
		mux:     s.cfg.mux,
		mux6:    s.cfg.mux6,
		showLog: s.cfg.showLog,
		onCandidate: func(candidate ice.Candidate) {
			if candidate == nil {
				return
			}
			s.mu.Lock()
			s.gatheredCandidates = append(s.gatheredCandidates, candidate)
			s.mu.Unlock()
			s.sendOffer(context.TODO(), candidate)
		},
		onFailed: s.Close,
	})
	if err != nil {
		return err
	}

	var pending []ice.Candidate
	s.mu.Lock()
	if s.agent != nil {
		// A racing goroutine created the agent first; discard the redundant one.
		s.mu.Unlock()
		_ = agent.Close() //nolint:errcheck
		return nil
	}
	s.agent = agent
	pending = s.pendingCandidates
	s.pendingCandidates = nil
	s.mu.Unlock()

	for _, c := range pending {
		if err := agent.AddRemoteCandidate(c); err != nil {
			s.cfg.log.Warn("shadow: replay pending candidate failed", "err", err)
		}
	}
	if len(pending) > 0 {
		s.offerOnce.Do(func() { close(s.offerReady) })
	}
	return nil
}

func (s *shadowUpgrade) gather() error {
	var gatherErr error
	s.gatherOnce.Do(func() {
		s.mu.Lock()
		agent := s.agent
		s.mu.Unlock()
		if agent == nil {
			gatherErr = errShadowClosed
			return
		}
		gatherErr = agent.GatherCandidates()
	})
	return gatherErr
}

func (s *shadowUpgrade) resendCandidates(ctx context.Context) {
	s.mu.Lock()
	cached := make([]ice.Candidate, len(s.gatheredCandidates))
	copy(cached, s.gatheredCandidates)
	s.mu.Unlock()
	for _, c := range cached {
		s.sendOffer(ctx, c)
	}
}

func (s *shadowUpgrade) addRemoteCandidate(offer *signal.Offer) {
	candidate, err := ice.UnmarshalCandidate(offer.Candidate)
	if err != nil {
		s.cfg.log.Debug("shadow: unmarshal candidate failed", "err", err)
		return
	}
	s.mu.Lock()
	if offer.Ufrag != "" && s.rUfrag == "" {
		s.rUfrag = offer.Ufrag
		s.rPwd = offer.Pwd
	}
	agent := s.agent
	if agent == nil {
		s.pendingCandidates = append(s.pendingCandidates, candidate)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if err := agent.AddRemoteCandidate(candidate); err != nil {
		s.cfg.log.Debug("shadow: add remote candidate failed", "err", err)
		return
	}
	s.offerOnce.Do(func() { close(s.offerReady) })
}

// dial waits for remote candidates, runs the ICE connectivity checks and
// reports the outcome. Success yields a transport carrying the verified
// direct address; the WG data plane roams onto it independent of this ICE
// connection, so the agent is only kept warm briefly for consent keepalives.
func (s *shadowUpgrade) dial(ctx context.Context) {
	var (
		t   infra.Transport
		err error
	)
	defer func() {
		s.finish(t, err)
		if err != nil {
			s.Close()
		}
	}()

	select {
	case <-ctx.Done():
		err = ctx.Err()
		return
	case <-s.closeChan:
		err = errShadowClosed
		return
	case <-s.offerReady:
	}

	s.mu.Lock()
	agent := s.agent
	rUfrag, rPwd := s.rUfrag, s.rPwd
	s.mu.Unlock()
	if agent == nil {
		err = errShadowClosed
		return
	}

	var iceConn *ice.Conn
	if isInitiator(s.cfg.localId, s.cfg.remoteId) {
		iceConn, err = agent.StartDial(rUfrag, rPwd)
	} else {
		iceConn, err = agent.StartAccept(rUfrag, rPwd)
	}
	if err != nil {
		return
	}

	connectCtx, cancelConnect := context.WithTimeout(ctx, iceConnectTimeout)
	defer cancelConnect()
	if err = agent.AwaitConnect(connectCtx); err != nil {
		return
	}

	t = &ICETransport{remoteAddr: iceConn.RemoteAddr().String()}
	time.AfterFunc(upgradeShadowCloseGrace, s.Close)
}

// sendAck answers a marked SYN with a marked ACK echoing the SAME attempt id.
func (s *shadowUpgrade) sendAck(ctx context.Context) error {
	s.send(ctx, &signal.SignalPacket{
		Type:     signal.PacketType_HANDSHAKE_ACK,
		Dialer:   signal.DialerType_ICE,
		SenderID: s.cfg.localId.ID().ToUint64(),
		Handshake: &signal.Handshake{
			Timestamp:      time.Now().Unix(),
			IsUpgradeProbe: true,
			AttemptID:      s.cfg.attemptID,
		},
	})
	return nil
}

func (s *shadowUpgrade) sendOffer(ctx context.Context, candidate ice.Candidate) {
	s.mu.Lock()
	agent := s.agent
	s.mu.Unlock()
	if agent == nil {
		return
	}
	ufrag, pwd, err := agent.GetLocalUserCredentials()
	if err != nil {
		return
	}
	// No Current/peer info: WG config was already exchanged on the relayed
	// session, and dropping it keeps the packet far under the relay's
	// MaxProbePayload cap.
	s.send(ctx, &signal.SignalPacket{
		Type:     signal.PacketType_OFFER,
		Dialer:   signal.DialerType_ICE,
		SenderID: s.cfg.localId.ID().ToUint64(),
		Offer: &signal.Offer{
			Ufrag:     ufrag,
			Pwd:       pwd,
			Candidate: candidate.Marshal(),
			PublicKey: s.cfg.localId.PublicKey.String(),
			AttemptID: s.cfg.attemptID,
		},
	})
}

func (s *shadowUpgrade) send(ctx context.Context, p *signal.SignalPacket) {
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := s.cfg.sender(ctx, s.cfg.remoteId.ID(), data, s.startedAt); err != nil {
		s.cfg.log.Debug("shadow: send failed", "remoteId", s.cfg.remoteId.AppID, "err", err)
	}
}

// finish reports the outcome exactly once.
func (s *shadowUpgrade) finish(t infra.Transport, err error) {
	s.resultOnce.Do(func() { s.cfg.onResult(t, err) })
}

func (s *shadowUpgrade) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Close stops the attempt and releases the agent. Idempotent; safe to call
// from the probe (restart/close), the timeout, a Failed agent or the post-
// success grace timer.
func (s *shadowUpgrade) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		cancelSyn, cancelAll := s.cancelSyn, s.cancelAll
		agent := s.agent
		s.mu.Unlock()
		if cancelSyn != nil {
			cancelSyn()
		}
		if cancelAll != nil {
			cancelAll()
		}
		close(s.closeChan)
		if agent != nil {
			_ = agent.Close() //nolint:errcheck
		}
	})
	s.finish(nil, errShadowClosed)
}

// beginUpgradeAttempt launches one make-before-break attempt (ADR-0007) from
// tryUpgrade. It replaces the old full probe restart: nothing here touches
// the WireGuard peer, the state machine or the working relay transport.
func (p *Probe) beginUpgradeAttempt(epoch uint64) {
	if p.newShadow == nil {
		return // no factory wiring (tests); stay relayed
	}
	s := p.newShadow(newAttemptID(), func(t infra.Transport, err error) {
		p.onShadowResult(epoch, t, err)
	})
	if !p.adoptShadow(s) {
		s.Close() // an attempt is already in flight; drop the duplicate
		return
	}
	s.start(context.Background())
}

// handleShadowSignal routes upgrade-probe signaling (Probe.Handle intercepts
// it before the main dialers ever see it).
func (p *Probe) handleShadowSignal(ctx context.Context, packet *signal.SignalPacket) error {
	// Responder: a marked SYN asks us to open a shadow answering the
	// initiator's background attempt. Only while relayed — an upgrade only
	// makes sense from the relay — and at most one per remote.
	if packet.Type == signal.PacketType_HANDSHAKE_SYN && p.newShadow != nil && p.sm.Current() == StateRelayReady {
		attemptID := ""
		if hs := packet.GetHandshake(); hs != nil {
			attemptID = hs.AttemptID
		}
		if attemptID == "" {
			return nil
		}
		if s := p.currentShadow(); s != nil {
			// One in-flight shadow per remote: answer from it, spawn nothing.
			return s.Handle(ctx, packet)
		}
		s := p.newShadow(attemptID, func(t infra.Transport, err error) {
			p.onShadowResult(p.epoch.Load(), t, err)
		})
		if !p.adoptShadow(s) {
			s.Close()
			return nil
		}
		s.start(context.Background())
		return s.Handle(ctx, packet) // answer this SYN from the fresh shadow
	}
	// Both roles: ACK/OFFER (and SYN retransmits) go to the in-flight shadow.
	if s := p.currentShadow(); s != nil {
		return s.Handle(ctx, packet)
	}
	return nil
}

// onShadowResult consumes the outcome of one background attempt. Success
// switches to the direct path in place — the same SetEndpoint-only mechanism
// the late-ICE race winner uses; failure leaves the relay path untouched and
// re-arms the backoff with the already-incremented attempt count.
func (p *Probe) onShadowResult(epoch uint64, t infra.Transport, err error) {
	p.clearShadow()
	if err == nil && p.epoch.Load() == epoch && p.sm.Current() == StateRelayReady {
		if uerr := p.handleUpgradeTransport(t); uerr != nil {
			p.log.Error("shadow upgrade handoff failed", uerr)
		}
		return
	}
	if err != nil {
		p.log.Info("background direct-connect attempt failed; relay path untouched",
			"remoteId", p.remoteId.AppID, "err", err)
	}
	// A restart since the attempt started owns re-arming (its own onSuccess
	// calls scheduleUpgrade); Close nils newShadow, stopping the cycle.
	if p.newShadow != nil && p.epoch.Load() == epoch && p.sm.Current() == StateRelayReady {
		p.scheduleUpgrade()
	}
}

func (p *Probe) currentShadow() *shadowUpgrade {
	p.shadowMu.Lock()
	defer p.shadowMu.Unlock()
	return p.shadow
}

func (p *Probe) adoptShadow(s *shadowUpgrade) bool {
	p.shadowMu.Lock()
	defer p.shadowMu.Unlock()
	if p.shadow != nil {
		return false
	}
	p.shadow = s
	return true
}

func (p *Probe) clearShadow() {
	p.shadowMu.Lock()
	p.shadow = nil
	p.shadowMu.Unlock()
}

// cancelShadow detaches and closes the in-flight shadow, if any.
func (p *Probe) cancelShadow() {
	p.shadowMu.Lock()
	s := p.shadow
	p.shadow = nil
	p.shadowMu.Unlock()
	if s != nil {
		s.Close()
	}
}
