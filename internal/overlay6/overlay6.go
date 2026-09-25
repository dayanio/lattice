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

// Package overlay6 derives the overlay IPv6 address of a node from its overlay
// IPv4 address. The overlay is dual-stack by construction: every node's IPv6
// is the network's ULA /64 prefix with the node's IPv4 as the low 32 bits, so
// no allocator or database column is needed and any party that knows a peer's
// IPv4 can compute its IPv6.
package overlay6

import (
	"errors"
	"net/netip"
	"strings"
)

// DefaultPrefixString is the overlay ULA /64. It sits in the project's
// fd6c:7270::/32 ULA space but in its own /48, apart from the relay's
// pseudo-endpoint addresses (those are outer endpoints, this is the inner
// overlay).
const DefaultPrefixString = "fd6c:7270:6c74::/64"

var defaultPrefix = netip.MustParsePrefix(DefaultPrefixString)

// DefaultPrefix returns the overlay IPv6 prefix.
func DefaultPrefix() netip.Prefix { return defaultPrefix }

// MeshCIDR is the overlay IPv6 prefix as a CIDR string, for firewall/NAT rules.
func MeshCIDR() string { return DefaultPrefixString }

// FromV4 returns prefix with v4 as its low 32 bits. prefix must be IPv6 and no
// longer than /96 so the low 32 bits are free.
func FromV4(prefix netip.Prefix, v4 netip.Addr) (netip.Addr, error) {
	if !prefix.Addr().Is6() || prefix.Addr().Is4In6() || prefix.Bits() > 96 {
		return netip.Addr{}, errors.New("overlay6: prefix must be an IPv6 prefix of at most /96")
	}
	if !v4.Is4() {
		return netip.Addr{}, errors.New("overlay6: not an IPv4 address")
	}
	b := prefix.Masked().Addr().As16()
	a := v4.As4()
	copy(b[12:], a[:])
	return netip.AddrFrom16(b), nil
}

// Derive maps an overlay IPv4 string ("10.96.0.4") to its overlay IPv6 string
// ("fd6c:7270:6c74::a60:4") under the default prefix. ok is false when v4 is
// not a valid IPv4 address.
func Derive(v4 string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(v4))
	if err != nil || !a.Is4() {
		return "", false
	}
	v6, err := FromV4(defaultPrefix, a)
	if err != nil {
		return "", false
	}
	return v6.String(), true
}

// GatewayAddr is the address the overlay's synthetic ICMPv6 errors come from
// (<prefix>::1). It corresponds to IPv4 0.0.0.1, which is outside 10/8, so it
// can never collide with a node.
func GatewayAddr() netip.Addr {
	b := defaultPrefix.Masked().Addr().As16()
	b[15] = 1
	return netip.AddrFrom16(b)
}

// Contains reports whether addr lies inside the overlay prefix.
func Contains(addr netip.Addr) bool { return defaultPrefix.Contains(addr) }

// IsIPv6CIDR reports whether s is an IPv6 CIDR such as "::/0" or "fd00::1/128".
func IsIPv6CIDR(s string) bool {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	return err == nil && p.Addr().Is6() && !p.Addr().Is4In6()
}

// HostAllowedIPs returns the AllowedIPs of a peer whose overlay IPv4 is v4:
// "<v4>/32", plus "<v6>/128" when dual is true. It returns "" for an invalid
// v4 so callers keep their existing "no address yet" behaviour.
func HostAllowedIPs(v4 string, dual bool) string {
	a, err := netip.ParseAddr(strings.TrimSpace(v4))
	if err != nil || !a.Is4() {
		return ""
	}
	out := a.String() + "/32"
	if dual {
		if v6, ok := Derive(v4); ok {
			out += "," + v6 + "/128"
		}
	}
	return out
}

// HostFromAllowedIPs finds the node's own overlay IPv6 in a comma-separated
// AllowedIPs list: the first "<addr>/128" whose address is inside the overlay
// prefix.
func HostFromAllowedIPs(allowed string) (netip.Addr, bool) {
	for _, part := range strings.Split(allowed, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if p.Addr().Is6() && p.Bits() == 128 && Contains(p.Addr()) {
			return p.Addr(), true
		}
	}
	return netip.Addr{}, false
}
