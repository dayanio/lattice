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

package split

import (
	"net"
	"reflect"
	"testing"
)

func TestCNSet_ContainsAndRoutes(t *testing.T) {
	s := ParseCNSet("# header\n1.0.1.0/24\n1.0.2.0/23\n\n114.112.0.0/12\n")
	if s.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", s.Len())
	}
	for _, in := range []string{"1.0.1.5", "1.0.3.255", "114.114.114.114"} {
		if !s.Contains(net.ParseIP(in)) {
			t.Errorf("Contains(%s) = false, want true", in)
		}
	}
	for _, out := range []string{"1.0.0.255", "1.0.4.0", "8.8.8.8", "114.128.0.0"} {
		if s.Contains(net.ParseIP(out)) {
			t.Errorf("Contains(%s) = true, want false", out)
		}
	}
	want := []string{"1.0.1.0/24", "1.0.2.0/23", "114.112.0.0/12"}
	if got := s.Routes(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes(0) = %v, want %v", got, want)
	}
}

func TestCNSet_DropsPrivateAndReservedBlocks(t *testing.T) {
	// Includes a block that merely OVERLAPS a reserved range (0.0.0.0/1 spans 10/8 etc.).
	s := ParseCNSet("10.96.0.0/24\n192.168.1.0/24\n100.64.0.0/10\n127.0.0.0/8\n169.254.0.0/16\n172.16.0.0/12\n224.0.0.0/3\n0.0.0.0/1\n1.0.1.0/24\n")
	want := []string{"1.0.1.0/24"}
	if got := s.Routes(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes(0) = %v, want %v (reserved/private blocks must never be excluded)", got, want)
	}
	if s.Contains(net.ParseIP("10.96.0.1")) {
		t.Fatal("10.96.0.1 (the tunnel DNS/overlay) must never be in the CN set")
	}
}

func TestCNSet_SkipsGarbageAndNestedBlocks(t *testing.T) {
	s := ParseCNSet("not-a-cidr\n2001:db8::/32\n1.0.0.0/33\n1.0.0.0/16\n1.0.1.0/24\n1.0.1.0/24\n")
	want := []string{"1.0.0.0/16"} // the /24s are nested inside the /16
	if got := s.Routes(0); !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes(0) = %v, want %v", got, want)
	}
}

func TestCNSet_RoutesBudgetKeepsLargestBlocks(t *testing.T) {
	s := ParseCNSet("1.0.0.0/24\n2.0.0.0/16\n3.0.0.0/24\n4.0.0.0/12\n5.0.0.0/24\n")
	got := s.Routes(2)
	want := []string{"2.0.0.0/16", "4.0.0.0/12"} // the two largest, in address order
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Routes(2) = %v, want %v", got, want)
	}
	if got := s.Routes(99); len(got) != 5 {
		t.Fatalf("Routes(99) has %d entries, want all 5", len(got))
	}
}

func TestCNSet_NilAndEmptyAreSafe(t *testing.T) {
	var nilSet *CNSet
	if nilSet.Contains(net.ParseIP("1.0.1.5")) || nilSet.Len() != 0 || nilSet.Routes(0) != nil {
		t.Fatal("a nil CNSet must behave as an empty set")
	}
	empty := ParseCNSet("")
	if empty.Contains(net.ParseIP("1.0.1.5")) || empty.Len() != 0 || len(empty.Routes(0)) != 0 {
		t.Fatal("an empty CNSet must exclude nothing")
	}
	if empty.Contains(net.ParseIP("2001:db8::1")) {
		t.Fatal("IPv6 addresses are never in the set")
	}
}
