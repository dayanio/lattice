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
