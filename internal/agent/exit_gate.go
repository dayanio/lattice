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

package agent

import (
	"time"

	"github.com/alatticeio/lattice/internal/server/transport"
)

// FailOpenPathStall is how long the exit provider's data path may show no
// progress before the consumer declares it dead and fails open — dropping
// the 0/1+128/1 takeover so traffic falls back to the direct path instead of
// blackholing.
//
// The provider's peer config carries a 25 s persistent keepalive, so a
// healthy path produces rx counters continuously; 60 s ≈ two missed
// keepalives. The provider-side probe can still report relay-ready while the
// WireGuard data path is dead, so probe state alone must not refresh
// progress — only counter growth and handshake renewal do.
const FailOpenPathStall = 60 * time.Second

// ExitPathGate gates the consumer-side exit takeover on the provider's
// data-path liveness. Feed it the provider's PeerStat on a regular tick;
// Dead flips true only after a path that had been working stops showing
// progress, so an initial slow connect never flaps the takeover.
//
// Both consumers share this gate: the Linux agent removes the table-5180
// policy routes while the gate is closed, and the Apple engine drops
// 0.0.0.0/0 from the OnRoutesChanged payload (the NE then tears down the
// 0/1+128/1 tunnel routes). Issue #63.
type ExitPathGate struct {
	everReady        bool
	lastProgress     time.Time
	lastRx           uint64
	lastTx           uint64
	lastHandshakeAgo int64
	seenHandshake    bool
}

// Evaluate feeds one peer-stat sample into the gate and reports whether the
// provider's data path is currently dead. st may be the zero value (peer
// unknown yet); now is injected for tests. Before the path has ever worked,
// Dead stays false — a slow first connect is the server's withdrawal
// problem, not the gate's.
func (g *ExitPathGate) Evaluate(st PeerStat, now time.Time) bool {
	progress := st.RxBytes > g.lastRx || st.TxBytes > g.lastTx
	if st.HandshakeAgo >= 0 && (!g.seenHandshake || st.HandshakeAgo < g.lastHandshakeAgo) {
		// A handshake happened or renewed since the last sample.
		progress = true
	}
	g.lastRx, g.lastTx = st.RxBytes, st.TxBytes
	g.lastHandshakeAgo, g.seenHandshake = st.HandshakeAgo, true
	if progress {
		g.lastProgress = now
	}

	ready := st.State == string(transport.StateICEReady) || st.State == string(transport.StateRelayReady)
	if ready && !g.everReady {
		// The first time the probe reports a working path counts as a start
		// point for the stall clock.
		g.everReady = true
		g.lastProgress = now
	}

	if st.State == string(transport.StateFailed) || st.State == string(transport.StateClosed) {
		return g.everReady
	}
	return g.everReady && now.Sub(g.lastProgress) > FailOpenPathStall
}
