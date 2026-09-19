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
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

// Direct-path echo tuning. The initiator pings every pathPingInterval; after
// pathPingMisses consecutive unanswered pings the direct path is declared dead.
// A peer that has never answered is assumed not to support echo (older agent)
// and is given up on after pathPingGiveUp unanswered pings, never declared dead.
var (
	pathPingInterval = 5 * time.Second
	pathPingTimeout  = 2 * time.Second
)

const (
	pathPingMisses = 3
	pathPingGiveUp = 6
)

// pathPinger sends one echo request to a direct address and returns its RTT.
type pathPinger func(ctx context.Context, addr string, timeout time.Duration) (time.Duration, error)

// startPathPing watches the direct path with periodic echoes while the probe is
// ice-ready. Only the initiator pings; a reply proves both directions work.
//
// WireGuard alone cannot do this. The responder sees the initiator's keepalives
// (livenessTracker), but the initiator receives nothing periodic, so a path that
// died only in the responder-to-initiator direction went unnoticed until the
// 180 s handshake rule. An echo dies if either direction dies.
//
// The check arms on the first reply. A peer that never answers is an older
// agent that does not know the echo, so pinging stops after pathPingGiveUp
// unanswered pings and the peer keeps the older rules; treating its silence as
// a dead path would restart a healthy connection every half minute.
func (p *Probe) startPathPing() {
	if p.pathPing == nil || !isInitiator(p.localId, p.remoteId) {
		return
	}
	p.mu.RLock()
	t := p.currentTransport
	p.mu.RUnlock()
	if t == nil || t.Type() != infra.ICE {
		return
	}
	addr := t.RemoteAddr()
	epoch := p.epoch.Load()
	interval, timeout := pathPingInterval, pathPingTimeout

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		armed := false
		unanswered, sent := 0, 0 // unanswered is the current run of misses
		for range ticker.C {
			if p.epoch.Load() != epoch || p.sm.Current() != StateICEReady {
				return
			}
			sent++
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			rtt, err := p.pathPing(ctx, addr, timeout)
			cancel()
			if p.epoch.Load() != epoch || p.sm.Current() != StateICEReady {
				return
			}
			if err == nil {
				armed, unanswered = true, 0
				p.log.Debug("direct path echo ok", "remoteId", p.remoteId.AppID, "rtt", rtt.Round(time.Millisecond))
				continue
			}
			unanswered++
			switch {
			case !armed && sent >= pathPingGiveUp:
				p.log.Debug("peer does not answer path echoes, relying on handshake and rx checks", "remoteId", p.remoteId.AppID)
				return
			case armed && unanswered >= pathPingMisses:
				p.log.Warn("direct path stopped answering, restarting probe",
					"remoteId", p.remoteId.AppID, "addr", addr, "misses", unanswered)
				if p.pathRestart != nil {
					p.pathRestart()
				} else {
					go p.restart()
				}
				return
			}
		}
	}()
}
