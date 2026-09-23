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

// Exit-node data plane (phase 2, Linux providers): when a node advertises
// routes it opts into gateway duty, so mesh peers that select it can reach
// the internet through it. See docs/superpowers/specs/
// 2026-09-23-exit-node-dataplane-design.md.

package provision

import (
	"fmt"
	"net"
)

// ExitGatewayCommands returns the idempotent commands that make dev forward
// mesh-sourced traffic (meshCIDR) out of the node's default WAN interface.
// Every rule is check→add so repeated netmap applications stay clean.
func ExitGatewayCommands(dev, meshCIDR string) []string {
	return []string{
		"sysctl -w net.ipv4.ip_forward=1",
		fmt.Sprintf("iptables -w 5 -C FORWARD -i %[1]s -j ACCEPT 2>/dev/null || iptables -w 5 -A FORWARD -i %[1]s -j ACCEPT", dev),
		fmt.Sprintf("iptables -w 5 -C FORWARD -o %[1]s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || iptables -w 5 -A FORWARD -o %[1]s -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT", dev),
		fmt.Sprintf("DEV=$(ip route show default | awk 'NR==1{print $5}'); iptables -w 5 -t nat -C POSTROUTING -o \"$DEV\" -s %[1]s -j MASQUERADE 2>/dev/null || iptables -w 5 -t nat -A POSTROUTING -o \"$DEV\" -s %[1]s -j MASQUERADE", meshCIDR),
	}
}

// MeshCIDRFromAddr derives the /24 mesh subnet from an overlay address
// ("10.96.0.2" → "10.96.0.0/24"). Empty on unparsable input.
func MeshCIDRFromAddr(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	v4 := ip.To4()
	if v4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
}

// EnsureExitGateway installs the exit-node forwarding rules. Linux only —
// callers may invoke unconditionally on every platform; other platforms are
// a silent no-op (the design doc covers macOS/Windows providers separately).
func EnsureExitGateway(runner func(name string, args ...string) error, dev, meshCIDR, goos string) error {
	if goos != "linux" || dev == "" || meshCIDR == "" {
		return nil
	}
	for _, cmd := range ExitGatewayCommands(dev, meshCIDR) {
		if err := runner("/bin/sh", "-c", cmd); err != nil {
			return fmt.Errorf("exit gateway rule %q: %w", cmd, err)
		}
	}
	return nil
}
