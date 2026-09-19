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
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestReconcileActionFor(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-10 * time.Second).UnixNano()
	frozen := now.Add(-2 * probingStuckAfter).UnixNano()

	for name, tc := range map[string]struct {
		state   PeerState
		started int64
		want    reconcileAction
	}{
		"created probe was never started":  {StateCreated, 0, reconcileStart},
		"closed probe is revived":          {StateClosed, 0, reconcileRevive},
		"probing past its window is stuck": {StateProbing, frozen, reconcileRestart},
		"probing within its window":        {StateProbing, fresh, reconcileNone},
		"probing without a start stamp":    {StateProbing, 0, reconcileNone},
		"ice-ready is healthy":             {StateICEReady, 0, reconcileNone},
		"lrp-ready is healthy":             {StateLRPReady, 0, reconcileNone},
		"failed already has a retry timer": {StateFailed, 0, reconcileNone},
	} {
		if got := reconcileActionFor(tc.state, tc.started, now); got != tc.want {
			t.Errorf("%s: reconcileActionFor(%s) = %v, want %v", name, tc.state, got, tc.want)
		}
	}
}

// A responder announces "I started fresh" so an initiator whose probe still
// believes the old session is alive re-initiates. The single fire-and-forget
// notice can be lost (NATS still connecting, initiator mid-restart), so it
// must repeat until the initiator's SYN arrives or the dialer is closed.
func newResponderDialer(t *testing.T, sends *atomic.Int32) (*iceDialer, infra.PeerIdentity) {
	t.Helper()
	local := infra.NewPeerIdentity("responder", wgtypes.Key{1})
	remote := infra.NewPeerIdentity("initiator", wgtypes.Key{2})
	if isInitiator(local, remote) {
		t.Fatal("test setup: local must be the responder")
	}
	d := NewIceDialer(&ICEDialerConfig{
		LocalId:  local,
		RemoteId: remote,
		Sender: func(ctx context.Context, peerId infra.PeerID, data []byte) error {
			sends.Add(1)
			return nil
		},
	}).(*iceDialer)
	return d, remote
}

func withFastRestartNotify(t *testing.T) {
	t.Helper()
	prev := restartNotifyInterval
	restartNotifyInterval = 10 * time.Millisecond
	t.Cleanup(func() { restartNotifyInterval = prev })
}

func TestICEDialer_ResponderRepeatsRestartNotify(t *testing.T) {
	withFastRestartNotify(t)
	var sends atomic.Int32
	d, remote := newResponderDialer(t, &sends)
	defer d.Close() //nolint:errcheck

	if err := d.Prepare(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := sends.Load(); n < 3 {
		t.Fatalf("restart notify sent %d time(s), want it repeated until the initiator answers", n)
	}
}

func TestICEDialer_RestartNotifyStopsWhenInitiatorAnswers(t *testing.T) {
	withFastRestartNotify(t)
	var sends atomic.Int32
	d, remote := newResponderDialer(t, &sends)
	defer d.Close() //nolint:errcheck

	if err := d.Prepare(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	d.offerOnce.Do(func() { close(d.offerReady) }) // initiator's SYN/OFFER arrived
	time.Sleep(30 * time.Millisecond)              // let any in-flight send land
	before := sends.Load()
	time.Sleep(100 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Fatalf("restart notify kept firing after the initiator answered: %d -> %d", before, after)
	}
}

func TestICEDialer_RestartNotifyStopsWhenClosed(t *testing.T) {
	withFastRestartNotify(t)
	var sends atomic.Int32
	d, remote := newResponderDialer(t, &sends)

	if err := d.Prepare(context.Background(), remote); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	_ = d.Close()
	time.Sleep(30 * time.Millisecond)
	before := sends.Load()
	time.Sleep(100 * time.Millisecond)
	if after := sends.Load(); after != before {
		t.Fatalf("restart notify kept firing after Close: %d -> %d", before, after)
	}
}
