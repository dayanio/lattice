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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/signal"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type sentPackets struct {
	mu   sync.Mutex
	kind []signal.PacketType
}

func (s *sentPackets) send(_ context.Context, _ infra.PeerID, data []byte) error {
	var p signal.SignalPacket
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	s.mu.Lock()
	s.kind = append(s.kind, p.Type)
	s.mu.Unlock()
	return nil
}

func (s *sentPackets) count(t signal.PacketType) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.kind {
		if k == t {
			n++
		}
	}
	return n
}

func newSynTestDialer(t *testing.T, restarts *atomic.Int32) (*lrpDialer, *sentPackets) {
	t.Helper()
	sent := &sentPackets{}
	d := NewLrpDialer(&LrpDialerConfig{
		LocalId:        infra.NewPeerIdentity("phone", wgtypes.Key{1}),
		RemoteId:       infra.NewPeerIdentity("mac", wgtypes.Key{2}),
		Sender:         sent.send,
		GetLocalPeer:   func() *infra.Peer { return nil },
		OnPeerReceived: func(infra.Peer) {},
		OnRestart:      func() { restarts.Add(1) },
	}).(*lrpDialer)
	return d, sent
}

func synPacket() *signal.SignalPacket {
	return &signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN, Dialer: signal.DialerType_LRP}
}

// The peer retransmits its SYN every 2 s until it sees our ACK. A retransmit
// that lands just after our session formed is not a restart; treating it as
// one made both ends restart on every retransmit, forever (observed on a phone
// after a wifi to cellular switch: 22 restart cycles in a minute, each caused
// by "SYN on active LRP session", and the Mac could not reach the phone).
func TestLRPDialer_SynRightAfterTheSessionFormedIsARetransmitNotARestart(t *testing.T) {
	var restarts atomic.Int32
	d, sent := newSynTestDialer(t, &restarts)
	d.mu.Lock()
	d.active, d.activeAt = true, time.Now()
	d.mu.Unlock()

	if err := d.Handle(context.Background(), d.remoteId, synPacket()); err != nil {
		t.Fatal(err)
	}

	if n := restarts.Load(); n != 0 {
		t.Fatalf("restarted %d time(s) for a retransmitted SYN", n)
	}
	if sent.count(signal.PacketType_HANDSHAKE_ACK) != 1 {
		t.Fatalf("must ACK the retransmit so the peer stops sending SYNs, ACKs=%d", sent.count(signal.PacketType_HANDSHAKE_ACK))
	}
	d.mu.Lock()
	stillActive := d.active
	d.mu.Unlock()
	if !stillActive {
		t.Fatal("the session that just formed must stay active")
	}
}

// A genuine restart still has to be recognised, only a little later.
func TestLRPDialer_SynLongAfterTheSessionFormedIsARestart(t *testing.T) {
	var restarts atomic.Int32
	d, _ := newSynTestDialer(t, &restarts)
	d.mu.Lock()
	d.active, d.activeAt = true, time.Now().Add(-2*lrpSynGrace)
	d.mu.Unlock()

	if err := d.Handle(context.Background(), d.remoteId, synPacket()); err != nil {
		t.Fatal(err)
	}

	if n := restarts.Load(); n != 1 {
		t.Fatalf("restarts = %d, want 1 for a SYN on a session that has been up a while", n)
	}
}

func TestLRPDialer_SynOnAnInactiveDialerIsSimplyAcked(t *testing.T) {
	var restarts atomic.Int32
	d, sent := newSynTestDialer(t, &restarts)

	if err := d.Handle(context.Background(), d.remoteId, synPacket()); err != nil {
		t.Fatal(err)
	}

	if restarts.Load() != 0 || sent.count(signal.PacketType_HANDSHAKE_ACK) != 1 {
		t.Fatalf("restarts=%d acks=%d, want 0 and 1", restarts.Load(), sent.count(signal.PacketType_HANDSHAKE_ACK))
	}
}
