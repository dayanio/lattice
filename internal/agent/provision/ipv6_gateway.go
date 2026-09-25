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

package provision

import (
	"fmt"
	"net/netip"

	"github.com/alatticeio/lattice/internal/overlay6"
)

// wan6Lookup prints the interface of the first IPv6 default route. It reads the
// "dev" field rather than a fixed column: a default route without a next hop
// ("default dev eth0 metric 1024") has no "via" and shifts the columns.
const wan6Lookup = `ip -6 route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}'`

// ExitGateway6Commands returns the idempotent commands that make dev forward
// mesh-sourced IPv6 traffic (meshCIDR6) out of the node's IPv6 WAN interface
// with NAT66, mirroring ExitGatewayCommands for IPv4.
//
// Order matters. Turning IPv6 forwarding on makes the kernel stop accepting
// router advertisements, so a host that gets its default route from SLAAC
// (most cloud VMs) would lose IPv6 the moment forwarding is enabled.
// accept_ra=2 on the WAN interface keeps accepting them, and is set first.
// The first command fails, and the rest are skipped, when there is no IPv6
// default route: the caller then treats the node as having no IPv6.
func ExitGateway6Commands(dev, meshCIDR6 string) []string {
	return []string{
		fmt.Sprintf(`WAN=$(%s); [ -n "$WAN" ] && sysctl -w "net.ipv6.conf.$WAN.accept_ra=2"`, wan6Lookup),
		"sysctl -w net.ipv6.conf.all.forwarding=1",
		fmt.Sprintf("ip6tables -w 5 -C FORWARD -i %[1]s -j ACCEPT 2>/dev/null || ip6tables -w 5 -A FORWARD -i %[1]s -j ACCEPT", dev),
		fmt.Sprintf("ip6tables -w 5 -C FORWARD -o %[1]s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || ip6tables -w 5 -A FORWARD -o %[1]s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT", dev),
		fmt.Sprintf(`WAN=$(%[1]s); ip6tables -w 5 -t nat -C POSTROUTING -o "$WAN" -s %[2]s -j MASQUERADE 2>/dev/null || ip6tables -w 5 -t nat -A POSTROUTING -o "$WAN" -s %[2]s -j MASQUERADE`, wan6Lookup, meshCIDR6),
	}
}

// EnsureExitGateway6 installs the IPv6 forwarding and NAT66 rules. Linux only:
// other platforms are a silent no-op, as with EnsureExitGateway.
func EnsureExitGateway6(runner func(name string, args ...string) error, dev, goos string) error {
	if goos != "linux" || dev == "" {
		return nil
	}
	for _, cmd := range ExitGateway6Commands(dev, overlay6.MeshCIDR()) {
		if err := runner("/bin/sh", "-c", cmd); err != nil {
			return fmt.Errorf("exit gateway ipv6 rule %q: %w", cmd, err)
		}
	}
	return nil
}

// ApplyOverlay6 gives dev its overlay IPv6 address and routes the overlay /64
// into it, so replies to mesh peers find their way back into the tunnel.
// Idempotent (`replace`). Linux only; other platforms are a no-op.
func ApplyOverlay6(runner func(name string, args ...string) error, dev string, addr netip.Addr, goos string) error {
	if goos != "linux" || dev == "" || !addr.Is6() {
		return nil
	}
	for _, cmd := range []string{
		fmt.Sprintf("ip -6 addr replace %s/128 dev %s", addr, dev),
		fmt.Sprintf("ip -6 route replace %s dev %s", overlay6.MeshCIDR(), dev),
	} {
		if err := runner("/bin/sh", "-c", cmd); err != nil {
			return fmt.Errorf("overlay ipv6 setup %q: %w", cmd, err)
		}
	}
	return nil
}
