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

package engine

import (
	"encoding/json"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

func TestBuildPeerList_ExcludesSelfAndPeersWithoutAnAddress(t *testing.T) {
	peers := []*infra.Peer{
		{AppID: "MacBook-Pro", Name: "MacBook-Pro", Address: addr("10.96.0.6")},
		{AppID: "cloud-node-1", Name: "cloud-node-1", Address: addr("10.96.0.2"), Platform: "linux"},
		{AppID: "no-address", Name: "no-address"},
		{AppID: "empty-address", Name: "empty-address", Address: addr("")},
		{AppID: "gone", Name: "gone", Address: addr("10.96.0.9"), Remove: true},
		nil,
	}
	got := buildPeerList(peers, map[string]string{"cloud-node-1": "relay-ready"}, "MacBook-Pro")
	if len(got) != 1 || got[0].AppID != "cloud-node-1" {
		t.Fatalf("got %+v, want only cloud-node-1", got)
	}
	if got[0].Address != "10.96.0.2" || got[0].Platform != "linux" || got[0].State != "relay-ready" || !got[0].Online {
		t.Fatalf("fields wrong: %+v", got[0])
	}
}

func TestBuildPeerList_StateMapsToOnlineAndDefaultsToNone(t *testing.T) {
	peers := []*infra.Peer{
		{AppID: "a", Address: addr("10.96.0.2")},
		{AppID: "b", Address: addr("10.96.0.3")},
		{AppID: "c", Address: addr("10.96.0.4")},
		{AppID: "d", Address: addr("10.96.0.5")},
	}
	states := map[string]string{"a": "ice-ready", "b": "probing", "c": "failed"}
	got := buildPeerList(peers, states, "self")
	want := map[string]struct {
		state  string
		online bool
	}{"a": {"ice-ready", true}, "b": {"probing", false}, "c": {"failed", false}, "d": {"none", false}}
	for _, p := range got {
		w := want[p.AppID]
		if p.State != w.state || p.Online != w.online {
			t.Errorf("%s: state=%q online=%v, want %q %v", p.AppID, p.State, p.Online, w.state, w.online)
		}
	}
}

func TestBuildPeerList_NameFallsBackToAppIDAndListIsSorted(t *testing.T) {
	peers := []*infra.Peer{
		{AppID: "zeta", Name: "Zeta", Address: addr("10.96.0.2")},
		{AppID: "beta", Address: addr("10.96.0.3")}, // no name
		{AppID: "alpha", Name: "alpha", Address: addr("10.96.0.4")},
	}
	got := buildPeerList(peers, nil, "self")
	order := []string{got[0].Name, got[1].Name, got[2].Name}
	if order[0] != "alpha" || order[1] != "beta" || order[2] != "Zeta" {
		t.Fatalf("order = %v, want case-insensitive by name with the AppID standing in for a missing name", order)
	}
}

func TestPeerListJSON_IsAnArrayEvenWhenEmpty(t *testing.T) {
	if got := peerListJSON(nil, nil, "self"); got != "[]" {
		t.Fatalf("empty list = %q, want []", got)
	}
	var back []peerInfo
	if err := json.Unmarshal([]byte(peerListJSON([]*infra.Peer{{AppID: "a", Address: addr("10.96.0.2")}}, nil, "x")), &back); err != nil || len(back) != 1 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}
