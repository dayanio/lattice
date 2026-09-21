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
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/relay"
	"github.com/alatticeio/lattice/internal/signal"
	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

type relayedPeer struct {
	id     infra.PeerIdentity
	client *relay.TCPClient
	dialer infra.Dialer
	sig    *peerSignaler
}

// newRelayedPair builds two Relay dialers whose NATS channel silently drops every
// packet, so the only way their handshake can complete is through the relay.
func newRelayedPair(t *testing.T) (a, b *relayedPeer) {
	t.Helper()
	srv := relay.NewServer(&config.Config{})
	ts := httptest.NewServer(srv.UpgradeHandler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	privA, privB := mustKey(t), mustKey(t)
	a = &relayedPeer{id: infra.NewPeerIdentity("a", privA.PublicKey())}
	b = &relayedPeer{id: infra.NewPeerIdentity("b", privB.PublicKey())}

	var mu sync.Mutex
	wire := func(self, other *relayedPeer, priv wgtypes.Key) {
		self.sig = newPeerSignaler(log.GetLogger("test-signaler"), other.id.AppID,
			func(context.Context, infra.PeerID, []byte) error { return nil }, // NATS: drops everything
			func() bool { return true },
			func(ctx context.Context, to infra.PeerID, data []byte) error {
				return self.client.Send(ctx, to.ToUint64(), relay.Probe, data)
			},
			func() bool { return self.client != nil && self.client.Connected() },
		)
		self.sig.onState(StateCreated, StateProbing)

		client, err := relay.NewTCPClient(ctx, self.id.ID(), ts.Listener.Addr().String(), priv,
			func(ctx context.Context, _ infra.PeerID, p *signal.SignalPacket) error {
				mu.Lock()
				d := self.dialer
				mu.Unlock()
				if d == nil {
					return nil
				}
				return d.Handle(ctx, other.id, p)
			})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
		go func() {
			fn := client.ReceiveFunc()
			bufs := [][]byte{make([]byte, 2048)}
			sizes := make([]int, 1)
			eps := make([]wgconn.Endpoint, 1)
			for ctx.Err() == nil {
				_, _ = fn(bufs, sizes, eps)
			}
		}()
		self.client = client
		d := NewRelayDialer(&RelayDialerConfig{
			LocalId:        self.id,
			RemoteId:       other.id,
			Relay:          client,
			Sender:         self.sig.Send,
			GetLocalPeer:   func() *infra.Peer { return &infra.Peer{AppID: self.id.AppID} },
			OnPeerReceived: func(infra.Peer) {},
		})
		t.Cleanup(func() { _ = d.Close() })
		mu.Lock()
		self.dialer = d
		mu.Unlock()
	}
	wire(a, b, privA)
	wire(b, a, privB)
	waitUntil(t, 3*time.Second, func() bool { return a.client.Connected() && b.client.Connected() }, "both relay sessions")
	return a, b
}

func mustKey(t *testing.T) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func dialBoth(t *testing.T, a, b *relayedPeer, wait time.Duration) (errA, errB error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for _, pr := range []struct{ self, other *relayedPeer }{{a, b}, {b, a}} {
		if err := pr.self.dialer.Prepare(ctx, pr.other.id); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, errA = a.dialer.Dial(ctx) }()
	go func() { defer wg.Done(); _, errB = b.dialer.Dial(ctx) }()
	wg.Wait()
	return
}

// With NATS dropping every packet, two peers on a healthy relay still reach a
// ready session: the SYN/ACK that NATS lost are re-sent over the relay once the
// attempt has been probing for relaySignalAfter.
func TestRelaySignaling_HandshakeCompletesWhenNATSDropsEverything(t *testing.T) {
	prev := relaySignalAfter
	relaySignalAfter = 100 * time.Millisecond
	t.Cleanup(func() { relaySignalAfter = prev })

	a, b := newRelayedPair(t)
	errA, errB := dialBoth(t, a, b, 15*time.Second)
	if errA != nil || errB != nil {
		t.Fatalf("dial over relay-only signaling failed: a=%v b=%v", errA, errB)
	}
}

// Control: the same setup without escalation never completes, so the test above
// is proving the fallback and not a leak through some other channel.
func TestRelaySignaling_ControlNATSDropWithoutEscalationStalls(t *testing.T) {
	prev := relaySignalAfter
	relaySignalAfter = time.Hour
	t.Cleanup(func() { relaySignalAfter = prev })

	a, b := newRelayedPair(t)
	errA, errB := dialBoth(t, a, b, 4*time.Second)
	if errA == nil || errB == nil {
		t.Fatalf("handshake completed with NATS dropped and no escalation: a=%v b=%v", errA, errB)
	}
}

// A realistic SYN must fit the relay's Probe limit, or the fallback would
// silently carry nothing. The peer record in it is stripped of credentials.
func TestSignaling_SynFitsTheRelayProbeLimit(t *testing.T) {
	addr := "10.96.0.4"
	lp := signalingPeer(&infra.Peer{
		Name: "MacBook-Pro", AppID: "MacBook-Pro", PublicKey: "4iy9kiYvpncdXoYYJoAvZ4YFybb4/gQmZxUNaU2680Y=",
		Address: &addr, AllowedIPs: "10.96.0.4/32", Port: 51820, Endpoint: "203.0.113.5:51820",
		Platform: "darwin", Hostname: "macbook-pro.local", InterfaceName: "utun4", GroupName: "default",
		Token:    "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJhZ2VudCJ9.signature-signature-signature-signature-signature-signature",
		RelayURL: "203.0.113.9:6266?token=Zm9vYmFyYmF6cXV4",
		Labels:   map[string]string{"env": "prod", "team": "network", "owner": "someone"},
	})
	info, err := json.Marshal(lp)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(&signal.SignalPacket{
		Type: signal.PacketType_HANDSHAKE_SYN, Dialer: signal.DialerType_Relay, SenderID: 1 << 62,
		Handshake: &signal.Handshake{Timestamp: time.Now().Unix(), PeerInfo: info},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > relay.MaxProbePayload/2 {
		t.Fatalf("a SYN is %d bytes, over half of the %d byte relay Probe limit", len(data), relay.MaxProbePayload)
	}
}
