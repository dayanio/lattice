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

package overlay6

import (
	"net/netip"
	"strconv"
	"testing"
)

func TestDerive(t *testing.T) {
	cases := map[string]string{
		"10.96.0.4":   "fd6c:7270:6c74::a60:4",
		"10.96.0.2":   "fd6c:7270:6c74::a60:2",
		"10.96.0.255": "fd6c:7270:6c74::a60:ff",
		"10.96.1.1":   "fd6c:7270:6c74::a60:101",
	}
	for v4, want := range cases {
		got, ok := Derive(v4)
		if !ok || got != want {
			t.Errorf("Derive(%q) = %q, %v; want %q", v4, got, ok, want)
		}
	}
}

func TestDerive_RejectsNonIPv4(t *testing.T) {
	for _, in := range []string{"", "not-an-ip", "fd00::1", "::ffff:10.96.0.4", "10.96.0"} {
		if got, ok := Derive(in); ok {
			t.Errorf("Derive(%q) = %q, want failure", in, got)
		}
	}
}

func TestFromV4_RejectsBadPrefix(t *testing.T) {
	v4 := netip.MustParseAddr("10.96.0.4")
	for _, p := range []string{"10.0.0.0/8", "fd6c:7270:6c74::/112"} {
		if _, err := FromV4(netip.MustParsePrefix(p), v4); err == nil {
			t.Errorf("FromV4(prefix %s) should fail", p)
		}
	}
	if _, err := FromV4(DefaultPrefix(), netip.MustParseAddr("fd00::1")); err == nil {
		t.Error("FromV4 with an IPv6 \"v4\" should fail")
	}
}

func TestDerived_AlwaysInsidePrefixAndDistinct(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 2; i < 255; i++ {
		v6, ok := Derive("10.96.0." + strconv.Itoa(i))
		if !ok {
			t.Fatalf("Derive failed for .%d", i)
		}
		if !Contains(netip.MustParseAddr(v6)) {
			t.Errorf("%s is outside the overlay prefix", v6)
		}
		if _, dup := seen[v6]; dup {
			t.Errorf("%s derived twice", v6)
		}
		seen[v6] = struct{}{}
	}
}

func TestGatewayAddr_InsidePrefixAndNotANode(t *testing.T) {
	g := GatewayAddr()
	if g.String() != "fd6c:7270:6c74::1" {
		t.Fatalf("GatewayAddr() = %s", g)
	}
	if !Contains(g) {
		t.Fatal("gateway must be inside the overlay prefix")
	}
	// No node derives to ::1 because that would need IPv4 0.0.0.1.
	for i := 0; i < 256; i++ {
		v6, _ := Derive("10.96.0." + strconv.Itoa(i))
		if v6 == g.String() {
			t.Fatalf("node .%d collides with the gateway", i)
		}
	}
}

func TestIsIPv6CIDR(t *testing.T) {
	yes := []string{"::/0", "fd6c:7270:6c74::a60:4/128", "2001:db8::/32"}
	no := []string{"0.0.0.0/0", "10.96.0.4/32", "::ffff:10.0.0.0/104", "nope", ""}
	for _, s := range yes {
		if !IsIPv6CIDR(s) {
			t.Errorf("IsIPv6CIDR(%q) = false", s)
		}
	}
	for _, s := range no {
		if IsIPv6CIDR(s) {
			t.Errorf("IsIPv6CIDR(%q) = true", s)
		}
	}
}

func TestHostAllowedIPs(t *testing.T) {
	if got := HostAllowedIPs("10.96.0.4", false); got != "10.96.0.4/32" {
		t.Errorf("single stack = %q", got)
	}
	if got := HostAllowedIPs("10.96.0.4", true); got != "10.96.0.4/32,fd6c:7270:6c74::a60:4/128" {
		t.Errorf("dual stack = %q", got)
	}
	if got := HostAllowedIPs("", true); got != "" {
		t.Errorf("empty address should stay empty, got %q", got)
	}
	if got := HostAllowedIPs("garbage", true); got != "" {
		t.Errorf("invalid address should stay empty, got %q", got)
	}
}

func TestHostFromAllowedIPs(t *testing.T) {
	got, ok := HostFromAllowedIPs("10.96.0.4/32, fd6c:7270:6c74::a60:4/128 ,0.0.0.0/0,::/0")
	if !ok || got.String() != "fd6c:7270:6c74::a60:4" {
		t.Fatalf("HostFromAllowedIPs = %v, %v", got, ok)
	}
	for _, in := range []string{
		"", "10.96.0.4/32", "0.0.0.0/0,::/0",
		"2001:db8::1/128",          // a /128, but not inside the overlay prefix
		"fd6c:7270:6c74::a60:4/64", // inside the prefix, but not a host route
	} {
		if a, ok := HostFromAllowedIPs(in); ok {
			t.Errorf("HostFromAllowedIPs(%q) = %v, want none", in, a)
		}
	}
}
