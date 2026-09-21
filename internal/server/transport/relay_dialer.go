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
	"fmt"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/relay"
	"github.com/alatticeio/lattice/internal/signal"
	"sync"
	"time"

	"github.com/pion/ice/v4"
)

var (
	_ infra.Dialer = (*relayDialer)(nil)
)

// relaySynGrace is how long after a session forms a SYN is still treated as a
// retransmit of the handshake that formed it rather than as a remote restart.
const relaySynGrace = 5 * time.Second

type relayDialer struct {
	mu             sync.Mutex
	log            *log.Logger
	localId        infra.PeerIdentity
	remoteId       infra.PeerIdentity
	relay          infra.RelayChannel
	readyChan      chan struct{}
	readyOnce      sync.Once // guards close(readyChan)
	active         bool      // true once SYN/ACK exchange completes; guarded by mu
	activeAt       time.Time // when active became true; guarded by mu
	cancel         context.CancelFunc
	sender         func(ctx context.Context, peerId infra.PeerID, data []byte) error
	getLocalPeer   func() *infra.Peer
	onPeerReceived func(peer infra.Peer)
	onRestart      func() // called when SYN arrives on an active session (remote restarted)
	sm             *SessionManager
	closeOnce      sync.Once
	stopChan       chan struct{} // closed on Close() to unblock goroutines
}

type RelayDialerConfig struct {
	LocalId   infra.PeerIdentity
	RemoteId  infra.PeerIdentity
	Relay     infra.RelayChannel
	SM        *SessionManager
	SessionId uint64
	// GetLocalPeer is called at send time so late-arriving ApplyFullConfig
	// updates (Address, AllowedIPs) are always reflected in SYN/ACK peer info.
	GetLocalPeer   func() *infra.Peer
	OnPeerReceived func(peer infra.Peer)
	Sender         func(ctx context.Context, peerId infra.PeerID, data []byte) error
	// OnRestart is called when a HANDSHAKE_SYN arrives while the session is
	// already active, signalling that the remote peer restarted.  The callback
	// should trigger probe.restart() to re-run discovery with fresh dialers.
	OnRestart func()
}

func NewRelayDialer(cfg *RelayDialerConfig) infra.Dialer {
	return &relayDialer{
		log:            log.GetLogger("relay-dialer"),
		localId:        cfg.LocalId,
		remoteId:       cfg.RemoteId,
		relay:          cfg.Relay,
		readyChan:      make(chan struct{}),
		stopChan:       make(chan struct{}),
		sm:             cfg.SM,
		sender:         cfg.Sender,
		getLocalPeer:   cfg.GetLocalPeer,
		onPeerReceived: cfg.OnPeerReceived,
		onRestart:      cfg.OnRestart,
	}
}

// Prepare sends HANDSHAKE_SYN every 2 s for up to 60 s.
// Both sides send SYN so that either side can detect a remote restart.
// The first SYN is sent immediately (no initial 2 s wait), matching iceDialer behaviour.
func (w *relayDialer) Prepare(ctx context.Context, remoteId infra.PeerIdentity) error {
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		newCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		w.mu.Lock()
		if w.cancel != nil {
			w.cancel()
		}
		w.cancel = cancel
		w.mu.Unlock()

		// Send the first SYN immediately instead of waiting for the first tick.
		w.log.Debug("sending SYN", "remote", remoteId)
		if err := w.sendPacket(ctx, remoteId, signal.PacketType_HANDSHAKE_SYN, nil); err != nil {
			w.log.Error("send syn failed", err)
		}

		for {
			select {
			case <-newCtx.Done():
				w.log.Warn("SYN canceled", "err", newCtx.Err())
				return
			case <-w.stopChan:
				w.log.Debug("SYN stopped via Close")
				return
			case <-ticker.C:
				w.log.Debug("sending SYN", "remote", remoteId)
				if err := w.sendPacket(ctx, remoteId, signal.PacketType_HANDSHAKE_SYN, nil); err != nil {
					w.log.Error("send syn failed", err)
				}
			}
		}
	}()

	return nil
}

func (w *relayDialer) sendPacket(ctx context.Context, remoteId infra.PeerIdentity, packetType signal.PacketType, _ ice.Candidate) error {
	p := &signal.SignalPacket{
		Type:     packetType,
		Dialer:   signal.DialerType_Relay,
		SenderID: w.localId.ID().ToUint64(),
	}

	switch packetType {
	case signal.PacketType_HANDSHAKE_SYN, signal.PacketType_HANDSHAKE_ACK:
		// Include local peer info so the remote learns our WG config at
		// SYN/ACK time, before any transport negotiation begins.
		hs := &signal.Handshake{Timestamp: time.Now().Unix()}
		if lp := w.getLocalPeer(); lp != nil {
			if data, err := json.Marshal(lp); err == nil {
				hs.PeerInfo = data
			}
		}
		p.Handshake = hs
	}

	data, err := json.Marshal(p)
	if err != nil {
		return err
	}

	w.log.Debug("send packet", "localId", w.localId, "remoteId", remoteId, "packetType", packetType)
	return w.sender(ctx, remoteId.ID(), data)
}

func (w *relayDialer) sendOfferFromRelay(ctx context.Context, offerType signal.PacketType) error {
	data, err := json.Marshal(w.getLocalPeer())
	if err != nil {
		return err
	}
	p := &signal.SignalPacket{
		Type:     offerType,
		Dialer:   signal.DialerType_Relay,
		SenderID: w.localId.ID().ToUint64(),
		Offer: &signal.Offer{
			PublicKey: w.localId.PublicKey.String(),
			Current:   data,
		},
	}

	offerData, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return w.relay.Send(ctx, w.remoteId.ID().ToUint64(), relay.Probe, offerData)
}

func (w *relayDialer) Handle(ctx context.Context, remoteId infra.PeerIdentity, packet *signal.SignalPacket) error {
	if packet.Dialer != signal.DialerType_Relay {
		return nil
	}
	switch packet.Type {
	case signal.PacketType_HANDSHAKE_SYN:
		// Extract peer info from SYN — new design: peer info in SYN/ACK.
		if hs := packet.GetHandshake(); hs != nil && len(hs.PeerInfo) > 0 {
			var remotePeer infra.Peer
			if err := json.Unmarshal(hs.PeerInfo, &remotePeer); err == nil {
				w.onPeerReceived(remotePeer)
			}
		}

		// If the session is already active, a SYN means the remote peer restarted.
		// Close this dialer to tear down stale state and trigger probe.restart()
		// so both sides re-run discovery with fresh dialers — same pattern as
		// iceDialer's "SYN on active agent" handling.
		w.mu.Lock()
		isActive := w.active
		retransmit := isActive && time.Since(w.activeAt) < relaySynGrace
		if isActive && !retransmit {
			w.active = false
		}
		w.mu.Unlock()

		// The peer resends its SYN every 2 s until it sees our ACK, so one can
		// land just after our session formed. That is the tail of the handshake
		// that formed it, not a restart: answer it (the ACK stops the peer's
		// retransmits) and keep the session. Restarting here made both ends
		// restart on every retransmit, indefinitely, once signaling latency
		// was high enough for retransmits to cross the handshake (a phone after
		// switching from wifi to cellular: 22 restart cycles in one minute).
		if retransmit {
			return w.sendPacket(ctx, remoteId, signal.PacketType_HANDSHAKE_ACK, nil)
		}

		if isActive {
			w.log.Debug("SYN on active Relay session — remote restarted, triggering restart", "remoteId", remoteId)
			if w.onRestart != nil {
				w.onRestart()
			}
			return nil
		}
		return w.sendPacket(ctx, remoteId, signal.PacketType_HANDSHAKE_ACK, nil)

	case signal.PacketType_HANDSHAKE_ACK:
		// Extract peer info from ACK — new design: peer info in SYN/ACK.
		if hs := packet.GetHandshake(); hs != nil && len(hs.PeerInfo) > 0 {
			var remotePeer infra.Peer
			if err := json.Unmarshal(hs.PeerInfo, &remotePeer); err == nil {
				w.onPeerReceived(remotePeer)
			}
		}

		w.mu.Lock()
		cancel := w.cancel
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		// Only the initiator (bigger-ID numerically) drives the OFFER/ANSWER exchange.
		// Use numeric comparison to avoid decimal string ordering bugs.
		if isInitiator(w.localId, w.remoteId) {
			return w.sendOfferFromRelay(ctx, signal.PacketType_OFFER)
		}
		return nil

	case signal.PacketType_OFFER:
		offer := packet.GetOffer()
		var peer infra.Peer
		if err := json.Unmarshal(offer.Current, &peer); err != nil {
			return err
		}
		w.onPeerReceived(peer)
		w.mu.Lock()
		w.active = true
		w.activeAt = time.Now()
		cancel := w.cancel
		w.cancel = nil
		w.mu.Unlock()
		if cancel != nil {
			cancel() // stop SYN ticker so we don't trigger spurious onRestart on the remote
		}
		w.closeReady()
		if err := w.sendOfferFromRelay(ctx, signal.PacketType_ANSWER); err != nil {
			w.log.Error("send ANSWER failed", err)
		}
		return nil

	case signal.PacketType_ANSWER:
		offer := packet.GetOffer()
		var peer infra.Peer
		if err := json.Unmarshal(offer.Current, &peer); err != nil {
			return err
		}
		w.onPeerReceived(peer)
		w.mu.Lock()
		w.active = true
		w.activeAt = time.Now()
		cancel := w.cancel
		w.cancel = nil
		w.mu.Unlock()
		if cancel != nil {
			cancel() // stop SYN ticker
		}
		w.readyOnce.Do(func() { close(w.readyChan) })
		return nil
	}
	return nil
}

// Dial blocks until the OFFER/ANSWER exchange completes or the 65 s deadline
// fires.  The timeout matches iceDialer so discover() sees consistent failure
// semantics: onFailure → 10 s backoff → probe.restart().
func (w *relayDialer) Dial(ctx context.Context) (infra.Transport, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	select {
	case <-dialCtx.Done():
		return nil, fmt.Errorf("relayDialer: timed out waiting for ready: %w", dialCtx.Err())
	case <-w.readyChan:
		remoteAddr := ""
		if ra := w.relay.RemoteAddr(); ra != nil {
			remoteAddr = ra.String()
		}
		return &RelayTransport{remoteAddr: remoteAddr}, nil
	}
}

func (w *relayDialer) Type() infra.DialerType {
	return infra.Relay_DIALER
}

func (w *relayDialer) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		cancel := w.cancel
		w.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		close(w.stopChan)
	})
	return nil
}

func (w *relayDialer) closeReady() {
	w.readyOnce.Do(func() { close(w.readyChan) })
}

type RelayTransport struct {
	remoteAddr string
}

func (w RelayTransport) Priority() uint8 {
	return infra.PriorityRelay
}

func (w RelayTransport) Close() error {
	return nil
}

func (w RelayTransport) Write(data []byte) error {
	return nil
}

func (w RelayTransport) Read(buff []byte) (int, error) {
	return 0, nil
}

func (w RelayTransport) RemoteAddr() string {
	return w.remoteAddr
}

func (w RelayTransport) Type() infra.TransportType {
	return infra.Relay
}
