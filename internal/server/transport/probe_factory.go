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
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/provision"
	"github.com/alatticeio/lattice/internal/relay"
	"github.com/alatticeio/lattice/internal/signal"
)

type ProbeFactory struct {
	// localId is the full identity of this node (AppID + PublicKey).
	localId infra.PeerIdentity

	mu     sync.RWMutex
	probes map[string]*Probe // keyed by remote AppID

	signal         infra.SignalService
	getProvisioner func() provision.Provisioner
	getOnMessage   func() func(context.Context, *infra.Message) error
	getRelay       func() infra.RelayChannel
	getStats       func(pubKey string) (PeerStats, error)

	log *log.Logger

	peerManager *infra.PeerManager
	showLog     bool

	FilteringMux  *infra.FilteringUDPMux
	FilteringMux6 *infra.FilteringUDPMux
}

type ProbeFactoryConfig struct {
	LocalId        infra.PeerIdentity
	Signal         infra.SignalService
	GetOnMessage   func() func(context.Context, *infra.Message) error
	PeerManager    *infra.PeerManager
	GetRelay       func() infra.RelayChannel
	FilteringMux   *infra.FilteringUDPMux
	FilteringMux6  *infra.FilteringUDPMux
	GetProvisioner func() provision.Provisioner
	// GetPeerStats returns the WireGuard handshake time and received-byte
	// counter for the given peer public key. Used by the liveness ticker to
	// detect silent peer failures. May be nil (liveness monitoring is disabled).
	GetPeerStats func(pubKey string) (PeerStats, error)
	ShowLog      bool
}

func NewProbeFactory(cfg *ProbeFactoryConfig) *ProbeFactory {
	return &ProbeFactory{
		log:            log.GetLogger("probe-factory"),
		localId:        cfg.LocalId,
		signal:         cfg.Signal,
		probes:         make(map[string]*Probe),
		peerManager:    cfg.PeerManager,
		getRelay:       cfg.GetRelay,
		showLog:        cfg.ShowLog,
		FilteringMux:   cfg.FilteringMux,
		FilteringMux6:  cfg.FilteringMux6,
		getProvisioner: cfg.GetProvisioner,
		getOnMessage:   cfg.GetOnMessage,
		getStats:       cfg.GetPeerStats,
	}
}

// pingDirect sends a path echo to a direct address through the shared UDP
// socket of the matching address family.
func (p *ProbeFactory) pingDirect(ctx context.Context, addr string, timeout time.Duration) (time.Duration, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return 0, err
	}
	mux := p.FilteringMux
	if ua.IP.To4() == nil && p.FilteringMux6 != nil {
		mux = p.FilteringMux6
	}
	if mux == nil {
		return 0, errors.New("no UDP mux for path echo")
	}
	return mux.Ping(ctx, ua, timeout)
}

// keepaliveFor returns the WireGuard persistent-keepalive interval this side
// configures for the peer: only the initiator sends keepalives.
//
// wireguard-go re-arms the keepalive timer on every authenticated packet it
// sends or receives. With keepalives on both sides each received one postpones
// the local one, the sides alternate, and each receives a keepalive only about
// every 50 s. That makes a single lost packet look like a 75 s silence and
// would trip the received-bytes stall check (livenessTracker) on a healthy
// path. With only the initiator sending, the responder receives one on a fixed
// 25 s rhythm and can judge liveness by it; the initiator has no such rhythm
// and relies on the handshake age, or on the responder's restart notice.
func keepaliveFor(local, remote infra.PeerIdentity) int {
	if isInitiator(local, remote) {
		return provision.PersistentKeepalive
	}
	return 0
}

// reconcileAction is what the reconciler must do with a probe.
type reconcileAction int

const (
	reconcileNone    reconcileAction = iota
	reconcileStart                   // never started (Created)
	reconcileRevive                  // permanently closed: replace and start
	reconcileRestart                 // frozen in Probing: restart wholesale
)

// probingStuckAfter is how long a Probing cycle may run before it is
// considered frozen: a healthy cycle self-terminates within ~75 s.
const probingStuckAfter = 90 * time.Second

// reconcileActionFor decides what the reconciler does with a probe given its
// state and (for Probing) when the current cycle started.
func reconcileActionFor(state PeerState, startedAtNanos int64, now time.Time) reconcileAction {
	switch state {
	case StateCreated:
		return reconcileStart
	case StateClosed:
		return reconcileRevive
	case StateProbing:
		if startedAtNanos > 0 && now.Sub(time.Unix(0, startedAtNanos)) > probingStuckAfter {
			return reconcileRestart
		}
	}
	return reconcileNone
}

// StartReconciler periodically revives probes that reached StateClosed.
//
// Probes close permanently after 60 s of failed discovery (Probe.onFailure),
// and the netmap pipeline cannot be relied on to recreate them: the poll loop
// skips re-applying an unchanged ConfigVersion, so a node whose first probe
// window overlapped a peer outage or a control-plane incident stayed dark
// until process restart even though every dependency had recovered (observed
// live: Mac initiator probes closed during the STUN outage and never
// restarted, 2026-09-19). This reconciler replaces every closed probe with a
// fresh one (Get swaps StateClosed entries) and starts it, restoring the
// initiate/answer role it had before.
func (f *ProbeFactory) StartReconciler(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				f.mu.RLock()
				var created, closed, stuck []infra.PeerIdentity
				for _, probe := range f.probes {
					switch reconcileActionFor(probe.sm.Current(), probe.startedAt.Load(), now) {
					case reconcileStart:
						created = append(created, probe.remoteId)
					case reconcileRevive:
						closed = append(closed, probe.remoteId)
					case reconcileRestart:
						// Probing cycles self-terminate within ~75 s (60 s SYN window +
						// Dial timeout). Still Probing well past that means the discover
						// goroutine is gone (e.g. it lost the epoch race): restart the
						// probe wholesale.
						stuck = append(stuck, probe.remoteId)
					}
				}
				f.mu.RUnlock()

				// Created probes were registered but never started, e.g. a signaling
				// packet reached a permanently closed probe, Get swapped in a fresh one
				// and nothing ever started it. Neither the netmap pipeline nor the
				// Closed/Probing checks would pick it up, leaving the peer dark until
				// process restart.
				for _, remoteId := range created {
					f.mu.RLock()
					probe := f.probes[remoteId.AppID]
					f.mu.RUnlock()
					if probe == nil {
						continue
					}
					if err := probe.Start(ctx, remoteId); err != nil {
						f.log.Error("reconciler: start probe failed", err, "remoteId", remoteId.AppID)
						continue
					}
					f.log.Info("reconciler: started never-started probe", "remoteId", remoteId.AppID)
				}
				for _, remoteId := range closed {
					probe, err := f.Get(remoteId)
					if err != nil {
						f.log.Error("reconciler: recreate probe failed", err, "remoteId", remoteId.AppID)
						continue
					}
					if err := probe.Start(ctx, remoteId); err != nil {
						f.log.Error("reconciler: restart probe failed", err, "remoteId", remoteId.AppID)
						continue
					}
					f.log.Info("reconciler: revived closed probe", "remoteId", remoteId.AppID)
				}
				for _, remoteId := range stuck {
					f.mu.RLock()
					probe := f.probes[remoteId.AppID]
					f.mu.RUnlock()
					if probe == nil {
						continue
					}
					f.log.Warn("reconciler: restarting probe stuck in Probing", "remoteId", remoteId.AppID)
					probe.restart()
				}
			}
		}
	}()
}

func (f *ProbeFactory) Register(remoteId infra.PeerIdentity, probe *Probe) {
	f.probes[remoteId.AppID] = probe
}

func (f *ProbeFactory) Get(remoteId infra.PeerIdentity) (*Probe, error) {
	// Fast path: probe already exists and is not permanently closed.
	f.mu.RLock()
	probe := f.probes[remoteId.AppID]
	f.mu.RUnlock()
	if probe != nil && probe.sm.Current() != StateClosed {
		return probe, nil
	}

	// Slow path: create a new probe under write lock.
	// Double-check after acquiring the lock in case another goroutine raced here.
	f.mu.Lock()
	defer f.mu.Unlock()
	probe = f.probes[remoteId.AppID]
	if probe != nil && probe.sm.Current() != StateClosed {
		return probe, nil
	}
	// Probe is nil or permanently closed — create a fresh one.
	// A closed probe already had its WG state cleaned up (RemovePeer called on
	// StateClosed transition), so it is safe to replace without further teardown.
	return f.NewProbe(remoteId)
}

func (f *ProbeFactory) Remove(appId string) {
	f.mu.Lock()
	probe := f.probes[appId]
	delete(f.probes, appId)
	f.mu.Unlock()

	// Close outside the lock to avoid deadlock if Close() triggers callbacks
	// that themselves call into ProbeFactory.
	if probe != nil {
		probe.Close()
	}
}

// wgConfigAdapter adapts provision.Provisioner to PeerOps and RouteOps.
type wgConfigAdapter struct {
	getProvisioner func() provision.Provisioner
	getRemotePeer  func() *infra.Peer
}

func (a *wgConfigAdapter) AddPeer(publicKey, allowedIPs string) error {
	pr := a.getProvisioner()
	if pr == nil {
		return nil
	}
	return pr.AddPeer(&provision.SetPeer{
		PublicKey:  publicKey,
		AllowedIPs: allowedIPs,
	})
}

func (a *wgConfigAdapter) SetEndpoint(publicKey, endpoint string, persistentKeepalive int) error {
	pr := a.getProvisioner()
	if pr == nil {
		return nil
	}
	rp := a.getRemotePeer()
	allowedIPs := ""
	if rp != nil {
		allowedIPs = rp.AllowedIPs
		if allowedIPs == "" && rp.Address != nil {
			allowedIPs = fmt.Sprintf("%s/32", *rp.Address)
		}
	}
	return pr.AddPeer(&provision.SetPeer{
		PublicKey:            publicKey,
		Endpoint:             endpoint,
		PersistentKeepalived: persistentKeepalive,
		AllowedIPs:           allowedIPs,
	})
}

func (a *wgConfigAdapter) RemovePeer(publicKey string) error {
	pr := a.getProvisioner()
	if pr == nil {
		return nil
	}
	return pr.RemovePeer(&provision.SetPeer{
		PublicKey: publicKey,
		Remove:    true,
	})
}

func (a *wgConfigAdapter) ApplyRoute(address, iface string) error {
	pr := a.getProvisioner()
	if pr == nil {
		return nil
	}
	return pr.ApplyRoute("add", address, iface)
}

func (a *wgConfigAdapter) SetupNAT(iface string) error {
	pr := a.getProvisioner()
	if pr == nil {
		return nil
	}
	return pr.SetupNAT(iface)
}

// relayClient returns the relay client, or nil when the node has none.
func (p *ProbeFactory) relayClient() infra.RelayChannel {
	if p.getRelay == nil {
		return nil
	}
	return p.getRelay()
}

func (p *ProbeFactory) NewProbe(remoteId infra.PeerIdentity) (*Probe, error) {
	getLocalPeer := func() *infra.Peer {
		lp := signalingPeer(p.peerManager.GetPeer(p.localId.AppID))
		if lp != nil && lp.AllowedIPs == "" && lp.Address != nil {
			lp.AllowedIPs = fmt.Sprintf("%s/32", *lp.Address)
		}
		return lp
	}

	var mu sync.Mutex
	var remotePeer *infra.Peer
	var peerKnownDone atomic.Bool

	getRemotePeer := func() *infra.Peer {
		mu.Lock()
		defer mu.Unlock()
		return remotePeer
	}

	// Configurator: the single channel for all WireGuard configuration.
	configurator := NewWGConfigurator(&wgConfigAdapter{
		getProvisioner: p.getProvisioner,
		getRemotePeer:  getRemotePeer,
	}, &wgConfigAdapter{
		getProvisioner: p.getProvisioner,
		getRemotePeer:  getRemotePeer,
	})

	// onPeerKnown: RegisterPeer runs on every call so a widened AllowedIPs
	// (e.g. this peer was just selected as an exit-node route provider,
	// after the connection was already up) reaches WireGuard — the
	// configurator itself is idempotent per (publicKey, allowedIPs), so a
	// repeat call with an unchanged value is a cheap no-op. SetEndpoint and
	// ApplyRoute stay gated to the first SYN/ACK: those don't depend on
	// AllowedIPs and re-running them on every peer update would fight a
	// probe that has since moved the endpoint (ICE upgrade, relay fallback).
	onPeerKnown := func(peer infra.Peer) {
		if peer.Address == nil {
			return
		}
		allowedIPs := peer.AllowedIPs
		if allowedIPs == "" {
			allowedIPs = fmt.Sprintf("%s/32", *peer.Address)
		}
		if err := configurator.RegisterPeer(remoteId.PublicKey.String(), allowedIPs); err != nil {
			p.log.Warn("onPeerKnown: RegisterPeer failed", "remoteId", remoteId.AppID, "err", err)
			return
		}
		p.log.Info("peer known, pre-configured WG entry", "remoteId", remoteId.AppID, "allowedIPs", allowedIPs)

		if !peerKnownDone.CompareAndSwap(false, true) {
			return
		}
		// Static endpoint from the registry: when the operator pins a peer's
		// endpoint (e.g. a container whose NAT topology defeats ICE), wire it
		// into WireGuard immediately instead of waiting for a probe that can
		// never complete.
		if peer.Endpoint != "" {
			if err := configurator.SetEndpoint(remoteId.PublicKey.String(), peer.Endpoint, 0); err != nil {
				p.log.Warn("onPeerKnown: static SetEndpoint failed", "remoteId", remoteId.AppID, "err", err)
			} else {
				p.log.Info("static endpoint configured", "remoteId", remoteId.AppID, "endpoint", peer.Endpoint)
			}
		}
		iface := ""
		if pr := p.getProvisioner(); pr != nil {
			iface = pr.GetIfaceName()
		}
		if err := configurator.ApplyRoute(*peer.Address, iface); err != nil {
			p.log.Warn("onPeerKnown: ApplyRoute failed", "remoteId", remoteId.AppID, "err", err)
		}
	}

	onPeerReceived := func(peer infra.Peer) {
		mu.Lock()
		// The signaling self-description carries at most the remote's own
		// /32, while the stored entry is seeded from this node's netmap,
		// whose AllowedIPs are server-computed per consumer (exit-node
		// 0.0.0.0/0, subnet routes). Overwriting would narrow a widened
		// entry seconds after connect and the version-guarded netmap
		// refresh would never restore it; merge instead. The next full
		// netmap apply stays the authority that widens OR narrows.
		if existing := p.peerManager.GetPeer(peer.AppID); existing != nil && existing.AllowedIPs != "" {
			peer.AllowedIPs = mergeAllowedIPs(existing.AllowedIPs, peer.AllowedIPs)
		}
		p.peerManager.AddPeer(peer.AppID, &peer)
		remotePeer = &peer
		mu.Unlock()
		onPeerKnown(peer)
	}

	var probe *Probe

	// State machine with transition callbacks — all WG config goes through
	// the configurator, NOT direct provisioner calls.
	sm := NewStateMachine(StateCreated)

	signaler := newPeerSignaler(p.log, remoteId.AppID, p.signal.Send,
		func() bool {
			cs, ok := p.signal.(interface{ Connected() bool })
			return !ok || cs.Connected()
		},
		func(ctx context.Context, to infra.PeerID, data []byte) error {
			rc := p.relayClient()
			if rc == nil {
				return errRelayUnready
			}
			return rc.Send(ctx, to.ToUint64(), relay.Probe, data)
		},
		func() bool {
			rc := p.relayClient()
			return rc != nil && rc.Connected()
		},
	)
	sm.OnTransition(signaler.onState)

	pubKey := remoteId.PublicKey.String()

	persistentKA := keepaliveFor(p.localId, remoteId)

	sm.OnTransition(func(from, to PeerState) {
		p.log.Debug("state transition", "remoteId", remoteId.AppID, "from", from, "to", to)

		switch {
		// First transport ready (ICE or Relay): set endpoint, route, NAT.
		case from == StateProbing && (to == StateICEReady || to == StateRelayReady):
			rp := getRemotePeer()
			if rp == nil || rp.Address == nil {
				p.log.Warn("remote peer info not received, cannot set endpoint")
				return
			}

			// Get the active transport to determine endpoint.
			probe.mu.Lock()
			t := probe.currentTransport
			probe.mu.Unlock()
			if t == nil {
				p.log.Warn("no active transport during state transition")
				return
			}

			var endpoint string
			if t.Type() == infra.Relay {
				endpoint = infra.RelayFakeAddrPort(remoteId.ID().ToUint64()).String()
			} else {
				endpoint = t.RemoteAddr()
			}

			if err := configurator.SetEndpoint(pubKey, endpoint, persistentKA); err != nil {
				p.log.Error("transition: SetEndpoint failed", err)
				return
			}
			// ApplyRoute is idempotent; re-run in case onPeerKnown was skipped.
			iface := ""
			if pr := p.getProvisioner(); pr != nil {
				iface = pr.GetIfaceName()
			}
			if err := configurator.ApplyRoute(*rp.Address, iface); err != nil {
				p.log.Error("transition: ApplyRoute failed", err)
			}
			if err := configurator.SetupNAT(iface); err != nil {
				p.log.Error("transition: SetupNAT failed", err)
			}

		// ICE upgrade after Relay: only SetEndpoint — NO duplicate AddPeer,
		// NO route/NAT re-application. This is the P1 bug fix.
		case from == StateRelayReady && to == StateICEReady:
			probe.mu.Lock()
			t := probe.currentTransport
			probe.mu.Unlock()
			if t == nil {
				p.log.Warn("no active transport during upgrade transition")
				return
			}
			if err := configurator.SetEndpoint(pubKey, t.RemoteAddr(), persistentKA); err != nil {
				p.log.Error("transition: SetEndpoint (upgrade) failed", err)
			}

		// Failed or Closed: clean up WireGuard peer.
		case to == StateFailed || to == StateClosed:
			if err := configurator.RemovePeer(pubKey); err != nil {
				p.log.Warn("transition: RemovePeer failed", "err", err)
			}
		}
	})

	makeRelayDialer := func() infra.Dialer {
		return NewRelayDialer(&RelayDialerConfig{
			LocalId:        p.localId,
			RemoteId:       remoteId,
			Relay:          p.getRelay(),
			Sender:         signaler.Send,
			GetLocalPeer:   getLocalPeer,
			OnPeerReceived: onPeerReceived,
			OnRestart:      func() { probe.restart() },
		})
	}

	probe = &Probe{
		log:          p.log,
		localId:      p.localId,
		remoteId:     remoteId,
		signal:       p.signal,
		sm:           sm,
		configurator: configurator,
		getStats:     p.getStats,
		pathPing:     p.pingDirect,
	}

	makeIceDialer := func() infra.Dialer {
		return NewIceDialer(&ICEDialerConfig{
			LocalId:        p.localId,
			RemoteId:       remoteId,
			Sender:         signaler.Send,
			GetLocalPeer:   getLocalPeer,
			OnPeerReceived: onPeerReceived,
			FilteringMux:   p.FilteringMux,
			ShowLog:        p.showLog,
			OnRestart:      func() { probe.restart() },
		})
	}
	probe.newIceDialer = makeIceDialer
	probe.iceDialer = makeIceDialer()
	probe.newRelayDialer = makeRelayDialer
	probe.relayDialer = makeRelayDialer()

	// onBeforeRestart resets the peerKnown guard for fresh SYN/ACK exchange.
	probe.onBeforeRestart = func() {
		peerKnownDone.Store(false)
	}

	p.Register(remoteId, probe)
	return probe, nil
}

// Handle is the NATS SignalHandler boundary: remoteId is PeerID from packet.SenderId.
// It resolves to a full PeerIdentity via PeerManager before passing down.
func (p *ProbeFactory) Handle(ctx context.Context, remoteId infra.PeerID, packet *signal.SignalPacket) error {
	p.log.Debug("Handle packet", "remoteId", remoteId, "packet", packet)

	// Config messages pushed from the management server (not peer-to-peer ICE packets).
	if packet.Type == signal.PacketType_MESSAGE {
		onMessage := p.getOnMessage()
		if onMessage == nil {
			return nil
		}
		var msg infra.Message
		if err := json.Unmarshal(packet.GetMessage().Content, &msg); err != nil {
			return fmt.Errorf("handle MESSAGE: unmarshal: %w", err)
		}
		return onMessage(ctx, &msg)
	}

	remoteIdentity, ok := p.peerManager.GetIdentity(remoteId)
	if !ok {
		// Peer not yet registered (config message hasn't arrived yet). Drop
		// the packet — the sender will retry the handshake after backoff.
		p.log.Warn("dropping signal packet from unknown peer, config not yet applied", "remoteId", remoteId)
		return nil
	}
	probe, err := p.Get(remoteIdentity)
	if err != nil {
		return err
	}
	// Get may have just replaced a permanently closed probe with a fresh one.
	// A responder that receives a SYN needs a running Dial to accept the ICE
	// session, and an initiator must be running to react to a restart notice,
	// so start it now instead of waiting for the reconciler tick. Background
	// context: ctx dies when this packet's handler returns.
	if probe.State() == StateCreated {
		_ = probe.Start(context.Background(), remoteIdentity)
	}
	return probe.Handle(ctx, remoteIdentity, packet)
}

func (p *ProbeFactory) OnReceive(sessionId [28]byte, data []byte) error {
	return nil
}

// TODO
func (p *ProbeFactory) Allows(remoteId string) bool {
	return true
}

// PeerConnectionStates snapshots each tracked peer's connection lifecycle
// state (probing / ice-ready / relay-ready / failed / closed), keyed by remote
// AppID. Embedded-engine clients (Apple Network Extension) surface this as
// connection quality: ice-ready = direct, relay-ready = relayed.
func (p *ProbeFactory) PeerConnectionStates() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	states := make(map[string]string, len(p.probes))
	for appID, probe := range p.probes {
		states[appID] = probe.State().String()
	}
	return states
}

// PeerRTTs snapshots each tracked peer's latest direct-path echo RTT in
// milliseconds, keyed by remote AppID. 0 = not measured (older agent,
// relayed path, or no echo yet).
func (p *ProbeFactory) PeerRTTs() map[string]int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]int64, len(p.probes))
	for appID, probe := range p.probes {
		out[appID] = probe.RTT().Milliseconds()
	}
	return out
}

// mergeAllowedIPs returns the deduplicated union of two comma-separated
// AllowedIPs lists, existing entries first. Used when a signaling
// self-description (which only ever lists the remote's own /32) meets a
// peer-manager entry that the netmap widened per consumer: the union
// preserves server-computed routes without letting a stale /32 linger
// once the next netmap apply narrows the entry wholesale.
func mergeAllowedIPs(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	if existing == "" {
		return incoming
	}
	if incoming == "" {
		return existing
	}
	seen := make(map[string]struct{})
	out := make([]string, 0, 4)
	for _, part := range strings.Split(existing+","+incoming, ",") {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}
		if _, dup := seen[cidr]; dup {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	return strings.Join(out, ",")
}
