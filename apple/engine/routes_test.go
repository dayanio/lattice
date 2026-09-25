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
	"testing"

	"github.com/alatticeio/lattice/apple/engine/split"
	"github.com/alatticeio/lattice/internal/agent/infra"
)

func addr(s string) *string { return &s }

func TestComputeExtraRoutes_SkipsPlainSlash32Peers(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "plain", Address: addr("10.96.0.2"), AllowedIPs: "10.96.0.2/32"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 0 {
		t.Fatalf("computeExtraRoutes() = %v, want empty (no extra routes)", got)
	}
}

func TestComputeExtraRoutes_IncludesExpandedRoutes(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "plain", Address: addr("10.96.0.2"), AllowedIPs: "10.96.0.2/32"},
		{Name: "gw", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,192.168.1.0/24"},
	}
	got := computeExtraRoutes(peers)
	want := []string{"192.168.1.0/24"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("computeExtraRoutes() = %v, want %v", got, want)
	}
}

func TestComputeExtraRoutes_DedupesAcrossPeers(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "gw1", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,0.0.0.0/0"},
		{Name: "gw2", Address: addr("10.96.0.5"), AllowedIPs: "10.96.0.5/32,0.0.0.0/0"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Fatalf("computeExtraRoutes() = %v, want [0.0.0.0/0] deduped", got)
	}
}

func TestComputeExtraRoutes_KeepsNonPeerSlash32(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "gw", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,8.8.8.8/32"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 1 || got[0] != "8.8.8.8/32" {
		t.Fatalf("computeExtraRoutes() = %v, want [8.8.8.8/32]", got)
	}
}

func TestComputeExtraRoutes_EmptyInput(t *testing.T) {
	got := computeExtraRoutes(nil)
	if len(got) != 0 {
		t.Fatalf("computeExtraRoutes(nil) = %v, want empty", got)
	}
}

func testSet() *split.CNSet {
	return split.ParseCNSet("1.0.1.0/24\n2.0.0.0/16\n114.112.0.0/12\n")
}

func TestRoutesPayload_LegacyArrayWhenNothingExcluded(t *testing.T) {
	got, err := routesPayload([]string{"192.168.1.0/24"}, nil, "")
	if err != nil || got != `["192.168.1.0/24"]` {
		t.Fatalf("routesPayload() = %q, %v; want the legacy JSON array", got, err)
	}
	got, _ = routesPayload(nil, nil, "")
	if got != `[]` {
		t.Fatalf("routesPayload(nil, nil, \"\") = %q, want []", got)
	}
}

func TestRoutesPayload_ObjectWhenExcluded(t *testing.T) {
	got, err := routesPayload([]string{"0.0.0.0/0"}, []string{"1.0.1.0/24"}, "")
	want := `{"included":["0.0.0.0/0"],"excluded":["1.0.1.0/24"]}`
	if err != nil || got != want {
		t.Fatalf("routesPayload() = %q, %v; want %q", got, err, want)
	}
}

func TestRoutesSnapshot_OnlyExcludesWhenEnabledAndExitSelected(t *testing.T) {
	exit := []string{"0.0.0.0/0"}
	subnet := []string{"192.168.1.0/24"}

	// switch off → legacy array, nothing excluded
	if got, _ := routesSnapshot(exit, false, testSet(), 0, ""); got != `["0.0.0.0/0"]` {
		t.Errorf("switch off: %q", got)
	}
	// switch on but no exit node selected → nothing excluded
	if got, _ := routesSnapshot(subnet, true, testSet(), 0, ""); got != `["192.168.1.0/24"]` {
		t.Errorf("no exit node: %q", got)
	}
	// switch on with an exit node → object with the CN blocks
	got, _ := routesSnapshot(exit, true, testSet(), 0, "")
	want := `{"included":["0.0.0.0/0"],"excluded":["1.0.1.0/24","2.0.0.0/16","114.112.0.0/12"]}`
	if got != want {
		t.Errorf("switch on: %q, want %q", got, want)
	}
	// an empty or nil set degrades to "no split" instead of failing
	if got, _ := routesSnapshot(exit, true, split.ParseCNSet(""), 0, ""); got != `["0.0.0.0/0"]` {
		t.Errorf("empty set: %q", got)
	}
	if got, _ := routesSnapshot(exit, true, nil, 0, ""); got != `["0.0.0.0/0"]` {
		t.Errorf("nil set: %q", got)
	}
}

func TestRoutesSnapshot_HonoursTheBudget(t *testing.T) {
	got, _ := routesSnapshot([]string{"0.0.0.0/0"}, true, testSet(), 1, "")
	want := `{"included":["0.0.0.0/0"],"excluded":["114.112.0.0/12"]}`
	if got != want {
		t.Fatalf("budget 1: %q, want %q", got, want)
	}
}

func TestSetSplitRouting_KicksOnlyOnChange(t *testing.T) {
	e := &Engine{routesKick: make(chan struct{}, 1)}
	e.SetSplitRouting(false) // already false: no change
	select {
	case <-e.routesKick:
		t.Fatal("no kick expected when the value did not change")
	default:
	}
	e.SetSplitRouting(true)
	select {
	case <-e.routesKick:
	default:
		t.Fatal("expected a kick when the switch turned on")
	}
	e.SetSplitRouting(true) // unchanged again
	select {
	case <-e.routesKick:
		t.Fatal("no kick expected for a repeated value")
	default:
	}
	if !e.splitEnabled() {
		t.Fatal("splitEnabled() = false after SetSplitRouting(true)")
	}
}

func TestComputeExtraRoutes_SkipsOverlayIPv6HostRoutesButKeepsDefault(t *testing.T) {
	peers := []*infra.Peer{
		{Name: "plain", Address: addr("10.96.0.2"), AllowedIPs: "10.96.0.2/32,fd6c:7270:6c74::a60:2/128"},
		{Name: "exit", Address: addr("10.96.0.4"), AllowedIPs: "10.96.0.4/32,fd6c:7270:6c74::a60:4/128,0.0.0.0/0,::/0"},
	}
	got := computeExtraRoutes(peers)
	want := []string{"0.0.0.0/0", "::/0"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("computeExtraRoutes() = %v, want %v (overlay /128 host routes are not extra routes)", got, want)
	}
}

func TestComputeExtraRoutes_KeepsForeignIPv6Routes(t *testing.T) {
	// A /128 outside the overlay prefix, and a non-host prefix inside it, are
	// real routes somebody advertised.
	peers := []*infra.Peer{
		{Name: "gw", Address: addr("10.96.0.5"), AllowedIPs: "10.96.0.5/32,2001:db8::1/128,fd6c:7270:6c74::/112"},
	}
	got := computeExtraRoutes(peers)
	if len(got) != 2 {
		t.Fatalf("computeExtraRoutes() = %v, want both foreign routes kept", got)
	}
}

func TestRoutesPayload_OverlayIPv6(t *testing.T) {
	// Overlay address but nothing excluded: an object, not the legacy array.
	got, err := routesPayload([]string{"0.0.0.0/0"}, nil, "fd6c:7270:6c74::a60:4")
	want := `{"included":["0.0.0.0/0"],"overlay6":"fd6c:7270:6c74::a60:4"}`
	if err != nil || got != want {
		t.Fatalf("routesPayload() = %q, %v; want %q", got, err, want)
	}
	// Both together.
	got, _ = routesPayload([]string{"0.0.0.0/0"}, []string{"1.0.1.0/24"}, "fd6c:7270:6c74::a60:4")
	want = `{"included":["0.0.0.0/0"],"excluded":["1.0.1.0/24"],"overlay6":"fd6c:7270:6c74::a60:4"}`
	if got != want {
		t.Fatalf("routesPayload() = %q; want %q", got, want)
	}
}

func TestRoutesSnapshot_PassesOverlay6Through(t *testing.T) {
	got, _ := routesSnapshot([]string{"0.0.0.0/0", "::/0"}, false, testSet(), 0, "fd6c:7270:6c74::a60:4")
	want := `{"included":["0.0.0.0/0","::/0"],"overlay6":"fd6c:7270:6c74::a60:4"}`
	if got != want {
		t.Fatalf("routesSnapshot() = %q; want %q", got, want)
	}
}
