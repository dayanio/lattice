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

package relay

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/signal"
	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func writeProbe(t *testing.T, conn net.Conn, to uint32, payload []byte) {
	t.Helper()
	h := Header{Cmd: Probe, ToID: to, PayloadLen: uint32(len(payload))}
	if _, err := conn.Write(h.Marshal()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
}

// An oversized Probe used to be logged and skipped with its payload still on
// the stream, so every following frame was parsed from the middle of it and
// signaling on that relay session died until it reconnected.
func TestTCPClient_OversizedProbeIsDiscardedAndTheStreamStaysInSync(t *testing.T) {
	_, ts := startTestRelay(t, false)

	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	id := infra.FromKey(priv.PublicKey())

	var got atomic.Uint64
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client, err := NewTCPClient(ctx, id, ts.Listener.Addr().String(), priv,
		func(_ context.Context, _ infra.PeerID, p *signal.SignalPacket) error {
			got.Store(p.SenderID)
			return nil
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

	waitFor(t, 3*time.Second, client.Connected, "the client to connect")

	sender, _ := dialUpgrade(t, ts)
	writeRegister(t, sender, 0x0A0A0A0A)
	to := uint32(id.ToUint64())

	oversized := make([]byte, MaxProbePayload+1000)
	for i := range oversized {
		oversized[i] = 'x'
	}
	writeProbe(t, sender, to, oversized)

	valid, err := json.Marshal(&signal.SignalPacket{Type: signal.PacketType_HANDSHAKE_SYN, SenderID: 42})
	if err != nil {
		t.Fatal(err)
	}
	writeProbe(t, sender, to, valid)

	waitFor(t, 3*time.Second, func() bool { return got.Load() == 42 },
		"the valid Probe that follows an oversized one to be delivered")
}

func TestTCPClient_ConnectedFollowsTheSession(t *testing.T) {
	_, ts := startTestRelay(t, false)
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client, err := NewTCPClient(ctx, infra.FromKey(priv.PublicKey()), ts.Listener.Addr().String(), priv,
		func(context.Context, infra.PeerID, *signal.SignalPacket) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, client.Connected, "the client to connect")
	_ = client.Close()
	if client.Connected() {
		t.Fatal("Connected() is still true after Close")
	}
}
