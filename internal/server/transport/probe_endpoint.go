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
	"net/netip"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

// endpointGuardDelays are when, after a probe becomes ice-ready, WireGuard's
// endpoint is checked for relay contamination.
var endpointGuardDelays = []time.Duration{time.Second, 3 * time.Second, 8 * time.Second, 20 * time.Second, 45 * time.Second}

// relayPoisoned reports whether a peer whose probe is ice-ready has
// nonetheless been re-pointed at the relay's fake address.
//
// wireguard-go sets a peer's endpoint to the source of the last authenticated
// packet it received, whatever path that was. When ICE finishes a little after
// the relay (the initiator commits to the relay after a 500 ms grace), the
// side that committed early sends a WireGuard handshake through the relay; the
// other side, already on ICE, receives it and is re-pointed at the relay, then
// replies through the relay and re-points the first side too. Both probes say
// ice-ready while all traffic keeps flowing through the relay, and nothing
// ever sets the endpoint back (observed: 10 minutes on the relay with no
// upgrade retry, because neither probe was in the relayed state).
func relayPoisoned(state PeerState, endpoint *net.UDPAddr) bool {
	if state != StateICEReady || endpoint == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(endpoint.IP)
	return ok && infra.IsLrpFakeAddr(addr.Unmap())
}

// reassertDirectEndpoint re-points WireGuard at the ICE address when a
// relay-delivered packet moved the endpoint. It reports whether it did.
//
// Safe only shortly after ICE connected, while the direct path has just been
// proved; that is why it is bounded by startEndpointGuard. Unconditional
// re-pointing later would fight the relay after a real direct-path failure.
// One side re-pointing is enough: its packets then arrive over UDP and
// WireGuard on the other side roams back to the direct address.
func (p *Probe) reassertDirectEndpoint() bool {
	if p.getStats == nil || p.configurator == nil {
		return false
	}
	pubKey := p.remoteId.PublicKey.String()
	stats, err := p.getStats(pubKey)
	if err != nil || !relayPoisoned(p.sm.Current(), stats.Endpoint) {
		return false
	}
	p.mu.RLock()
	t := p.currentTransport
	p.mu.RUnlock()
	if t == nil || t.Type() != infra.ICE {
		return false
	}
	if err := p.configurator.SetEndpoint(pubKey, t.RemoteAddr(), keepaliveFor(p.localId, p.remoteId)); err != nil {
		p.log.Warn("re-pointing endpoint at the direct path failed", "remoteId", p.remoteId.AppID, "err", err)
		return false
	}
	p.log.Info("relay traffic had re-pointed a direct peer; restored the direct endpoint",
		"remoteId", p.remoteId.AppID, "endpoint", t.RemoteAddr())
	return true
}

// startEndpointGuard repairs relay contamination for a short while after the
// probe becomes ice-ready.
func (p *Probe) startEndpointGuard() {
	epoch := p.epoch.Load()
	delays := endpointGuardDelays
	go func() {
		var elapsed time.Duration
		for _, at := range delays {
			time.Sleep(at - elapsed)
			elapsed = at
			if p.epoch.Load() != epoch || p.sm.Current() != StateICEReady {
				return
			}
			p.reassertDirectEndpoint()
		}
	}()
}
