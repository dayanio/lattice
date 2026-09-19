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
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// scriptedNetmaps answers GetNetMap with a fixed sequence of replies, then
// repeats the last one.
type scriptedNetmaps struct {
	mu      sync.Mutex
	replies []*infra.Message
	calls   int
}

func (s *scriptedNetmaps) Request(context.Context, string, string, []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	s.calls++
	return json.Marshal(s.replies[i])
}

func (s *scriptedNetmaps) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func msgWith(status, address string) *infra.Message {
	return &infra.Message{Current: &infra.Peer{AppID: "phone", ApprovalStatus: status, Address: &address}}
}

func fastNetmapPolling(t *testing.T) {
	t.Helper()
	pw, pp, pm := netmapWait, netmapPoll, pendingPollMax
	netmapWait, netmapPoll, pendingPollMax = 300*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { netmapWait, netmapPoll, pendingPollMax = pw, pp, pm })
}

func TestClassifyNetmap(t *testing.T) {
	empty := ""
	addr := "10.96.0.6"
	cases := []struct {
		name string
		msg  *infra.Message
		want netmapPhase
	}{
		{"nil", nil, netmapWaiting},
		{"no current", &infra.Message{}, netmapWaiting},
		{"no address yet", &infra.Message{Current: &infra.Peer{Address: &empty}}, netmapWaiting},
		{"nil address", &infra.Message{Current: &infra.Peer{}}, netmapWaiting},
		{"approved with an address", &infra.Message{Current: &infra.Peer{ApprovalStatus: "approved", Address: &addr}}, netmapReady},
		{"legacy netmap without a status", &infra.Message{Current: &infra.Peer{Address: &addr}}, netmapReady},
		{"pending", &infra.Message{Current: &infra.Peer{ApprovalStatus: "pending", Address: &empty}}, netmapPending},
		{"revoked", &infra.Message{Current: &infra.Peer{ApprovalStatus: "revoked", Address: &empty}}, netmapRevoked},
	}
	for _, c := range cases {
		if got := classifyNetmap(c.msg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A pending device is announced once and then waited for well past the normal
// allocation deadline, until an administrator approves it.
func TestFetchNetMap_WaitsForApprovalPastTheAllocationDeadline(t *testing.T) {
	fastNetmapPolling(t)
	pending := msgWith("pending", "")
	replies := make([]*infra.Message, 0, 80)
	for i := 0; i < 79; i++ { // far more than netmapWait allows at the pending poll rate
		replies = append(replies, pending)
	}
	replies = append(replies, msgWith("approved", "10.96.0.6"))
	src := &scriptedNetmaps{replies: replies}

	var announced int
	key, _ := wgtypes.GeneratePrivateKey()
	peer, err := fetchNetMap(context.Background(), src, "phone", key.PublicKey().String(), "jwt", key, func() { announced++ })
	if err != nil {
		t.Fatalf("waiting for approval failed: %v", err)
	}
	if announced != 1 {
		t.Fatalf("onPending ran %d times, want exactly once", announced)
	}
	if peer.Address == nil || *peer.Address != "10.96.0.6" || peer.Token != "jwt" {
		t.Fatalf("peer after approval = %+v", peer)
	}
	if src.count() < 60 {
		t.Fatalf("only %d polls: the wait must outlast netmapWait while pending", src.count())
	}
}

func TestFetchNetMap_RevokedIsAnError(t *testing.T) {
	fastNetmapPolling(t)
	src := &scriptedNetmaps{replies: []*infra.Message{msgWith("pending", ""), msgWith("revoked", "")}}
	key, _ := wgtypes.GeneratePrivateKey()
	_, err := fetchNetMap(context.Background(), src, "phone", "pk", "jwt", key, nil)
	if !errors.Is(err, ErrDeviceRevoked) {
		t.Fatalf("err = %v, want ErrDeviceRevoked", err)
	}
}

// Without approval gating nothing changes: a device that never gets an address
// still times out.
func TestFetchNetMap_NoAddressAndNotPendingStillTimesOut(t *testing.T) {
	fastNetmapPolling(t)
	src := &scriptedNetmaps{replies: []*infra.Message{msgWith("", "")}}
	var announced bool
	key, _ := wgtypes.GeneratePrivateKey()
	start := time.Now()
	_, err := fetchNetMap(context.Background(), src, "phone", "pk", "jwt", key, func() { announced = true })
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if announced {
		t.Fatal("a device that is not pending must not be announced as pending")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v, want about netmapWait", time.Since(start))
	}
}

func TestFetchNetMap_CancelEndsTheWait(t *testing.T) {
	fastNetmapPolling(t)
	src := &scriptedNetmaps{replies: []*infra.Message{msgWith("pending", "")}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	key, _ := wgtypes.GeneratePrivateKey()
	go func() {
		_, err := fetchNetMap(ctx, src, "phone", "pk", "jwt", key, nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling did not end the wait for approval")
	}
}
