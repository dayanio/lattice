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
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/relay"
)

// relaySignalAfter is how long a probe attempt may stay in probing before its
// signaling packets are also sent over the relay.
var relaySignalAfter = 2 * time.Second

var (
	errNATSDown     = errors.New("nats: not connected")
	errRelayUnready = errors.New("relay: not connected")
	errSignalBig    = errors.New("relay: signaling packet too large")
)

// peerSignaler sends a probe's signaling packets (SYN, ACK, OFFER, ANSWER,
// RESTART_NOTIFY) to one remote peer over NATS and, when NATS is not getting
// the probe anywhere, also over the relay's Probe frame.
//
// The sender cannot see the receiver's NATS: publishing to a peer whose
// subscription is dead still succeeds. So the relay is added on lack of
// progress (the attempt is still probing after relaySignalAfter), not only when
// the local NATS is down. Sending on both channels can deliver a packet twice;
// the dialers already tolerate retransmissions.
type peerSignaler struct {
	log    *log.Logger
	remote string

	nats   func(ctx context.Context, to infra.PeerID, data []byte) error
	natsUp func() bool

	relaySend func(ctx context.Context, to infra.PeerID, data []byte) error
	relayUp   func() bool

	now   func() time.Time
	after time.Duration // relaySignalAfter, fixed when the signaler is built

	mu        sync.Mutex
	since     time.Time // start of the current probing attempt; zero when not probing
	escalated bool      // the escalation of this attempt has been logged
}

func newPeerSignaler(
	logger *log.Logger,
	remote string,
	natsSend func(ctx context.Context, to infra.PeerID, data []byte) error,
	natsUp func() bool,
	relaySend func(ctx context.Context, to infra.PeerID, data []byte) error,
	relayUp func() bool,
) *peerSignaler {
	return &peerSignaler{
		log:       logger,
		remote:    remote,
		nats:      natsSend,
		natsUp:    natsUp,
		relaySend: relaySend,
		relayUp:   relayUp,
		now:       time.Now,
		after:     relaySignalAfter,
	}
}

// onState tracks the attempt window. Register it with the probe's state machine.
func (s *peerSignaler) onState(_, to PeerState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if to == StateProbing {
		s.since = s.now()
		s.escalated = false
		return
	}
	s.since = time.Time{}
}

func (s *peerSignaler) stalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.since.IsZero() && s.now().Sub(s.since) >= s.after
}

// Send has the signature of the dialers' Sender.
func (s *peerSignaler) Send(ctx context.Context, to infra.PeerID, data []byte) error {
	reason := ""
	var natsErr error
	if s.natsUp != nil && !s.natsUp() {
		natsErr, reason = errNATSDown, "nats-disconnected"
	} else if natsErr = s.nats(ctx, to, data); natsErr != nil {
		reason = "nats-error"
	}
	if reason == "" && s.stalled() {
		reason = "no-progress"
	}
	if reason == "" {
		return nil
	}

	relayErr := s.viaRelay(ctx, to, data, reason)
	if natsErr == nil || relayErr == nil {
		return nil
	}
	return errors.Join(natsErr, relayErr)
}

func (s *peerSignaler) viaRelay(ctx context.Context, to infra.PeerID, data []byte, reason string) error {
	if s.relaySend == nil || (s.relayUp != nil && !s.relayUp()) {
		return errRelayUnready
	}
	// Older relay clients drop, and mis-frame, Probe payloads above this limit.
	if len(data) > relay.MaxProbePayload {
		s.log.Debug("signaling packet too large for the relay", "remoteId", s.remote, "bytes", len(data))
		return errSignalBig
	}
	s.mu.Lock()
	first := !s.escalated
	s.escalated = true
	s.mu.Unlock()
	if first {
		s.log.Info("signaling escalated to relay", "remoteId", s.remote, "reason", reason)
	}
	return s.relaySend(ctx, to, data)
}

// signalingPeer is the copy of the local peer record that goes into signaling
// payloads. Remote peers only need the WireGuard-facing fields; the agent JWT
// and the relay URL (which carries the relay token) are credentials that must
// not be handed to every peer we talk to, and dropping them keeps the packet
// small enough for the relay's 2 KiB Probe limit.
func signalingPeer(lp *infra.Peer) *infra.Peer {
	if lp == nil {
		return nil
	}
	cp := *lp
	cp.Token = ""
	cp.RelayURL = ""
	return &cp
}
