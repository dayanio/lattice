// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package netstate owns every piece of network state lattice writes outside
// its own process — policy-routing tables, ip rules, iptables chains — so
// that all of it is (a) namespaced under LATTICE-* / a dedicated table and
// (b) removable by one idempotent sweep (`lattice net cleanup`) even when
// lattice itself will not start again.
//
// Design contract (2026-10-10-exit-node-crash-safety-design.md):
//   - the host's default route is never touched; exit takeover installs
//     0.0.0.0/1 + 128.0.0.0/1 into table 5180 via the tunnel interface, so
//     when the process dies the kernel deletes the interface AND the routes
//     that hang off it — an orphan ip rule pointing at an empty table falls
//     through to main and is harmless;
//   - iptables rules only ever live inside LATTICE-* chains; built-in chains
//     hold at most one jump rule each, so cleanup is "flush chains, remove
//     jumps" and can never damage rules owned by Docker/ufw/libvirt;
//   - every sweep command is guarded (del/flush + `|| true`) — cleanup is
//     idempotent and safe to run at any time, on any state.
package netstate

import (
	"fmt"
	"strings"
)

// Runner executes a command; injected so tests can record instead of run.
type Runner func(name string, args ...string) error

// RouteTable is the dedicated policy-routing table for lattice-managed
// routes (exit takeover). Numbered out of the extended range to avoid the
// well-known builtin tables (255 local, 254 main, 253 default).
const RouteTable = 5180

// ip rule priorities lattice owns. Grouped under the same 4-digit prefix as
// the table so `ip rule show` reads as one block and cleanup can target them
// by priority alone.
const (
	// RulePrioSelf keeps lattice's own underlay traffic (WireGuard UDP,
	// relay, control plane) on the main table: the engine runs as root, so
	// a uid rule is enough to keep the tunnel's own packets out of the
	// tunnel.
	RulePrioSelf = 4500
	// RulePrioLAN lets non-root processes keep using specific (LAN) routes
	// from the main table; only the default route (prefix length 0) is
	// suppressed so it can fall through to the tunnel table.
	RulePrioLAN = 5170
	// RulePrioTunnel sends everything else to the exit table.
	RulePrioTunnel = 5180
)

// iptables chains lattice owns. All lattice rules live inside these chains;
// the built-in chains only ever hold a single jump into them.
const (
	ChainForward = "LATTICE-FORWARD" // filter, jumped from FORWARD  (provider / gateway)
	ChainNAT     = "LATTICE-NAT"     // nat,     jumped from POSTROUTING
	ChainIngress = "LATTICE-INGRESS" // filter,  jumped from INPUT     (policy enforcer)
	ChainEgress  = "LATTICE-EGRESS"  // filter,  jumped from OUTPUT    (policy enforcer)
)

// ExitTakeoverCommands returns the idempotent command sequence that routes
// all non-root traffic through iface (the tunnel) without touching the
// host's default route. Guards (`-C ... || ...`) make repeated netmap
// applies a no-op.
func ExitTakeoverCommands(iface string) []string {
	return []string{
		// lattice's own sockets (root) keep using main — the tunnel's
		// underlay must never enter the tunnel.
		ruleAdd(fmt.Sprintf("priority %d uidrange 0-0 lookup main", RulePrioSelf)),
		// LAN / non-default routes still resolve from main for everyone.
		ruleAdd(fmt.Sprintf("priority %d lookup main suppress_prefixlength 0", RulePrioLAN)),
		// The remaining traffic consults the exit table…
		ruleAdd(fmt.Sprintf("priority %d lookup %d", RulePrioTunnel, RouteTable)),
		// …which carries the 0/1 + 128/1 takeover routes bound to the
		// tunnel interface. When the process dies the kernel removes the
		// interface and these routes with it, the table goes empty and
		// rule lookups fall through to main: internet restores itself.
		fmt.Sprintf("ip route replace 0.0.0.0/1 dev %s table %d", iface, RouteTable),
		fmt.Sprintf("ip route replace 128.0.0.0/1 dev %s table %d", iface, RouteTable),
	}
}

// ExitTakeoverRemoveCommands undoes ExitTakeoverCommands. Every command is
// guarded: removing state that is already gone must succeed.
func ExitTakeoverRemoveCommands() []string {
	return []string{
		fmt.Sprintf("ip rule del priority %d 2>/dev/null || true", RulePrioTunnel),
		fmt.Sprintf("ip rule del priority %d 2>/dev/null || true", RulePrioLAN),
		fmt.Sprintf("ip rule del priority %d 2>/dev/null || true", RulePrioSelf),
		fmt.Sprintf("ip route flush table %d 2>/dev/null || true", RouteTable),
	}
}

// CleanupCommands is the full sweep behind `lattice net cleanup` /
// systemd ExecStopPost: every piece of network state lattice may have
// written, removed by well-known markers (table number, rule priorities,
// chain names) — deliberately independent of any manifest file, so it works
// even when lattice will not start. Idempotent by construction.
//
// The interface name is fixed ("wf0", infra.getInterfaceName), so jumps
// attached with an interface qualifier (the policy enforcer's INPUT -i wf0
// style) are swept alongside the plain variants.
func CleanupCommands() []string {
	cmds := ExitTakeoverRemoveCommands()
	// Policy-enforcer chains (INPUT/OUTPUT) and gateway chains
	// (FORWARD/POSTROUTING): detach the jump (both the plain and the
	// interface-qualified variants), flush, delete.
	edge := "wf0"
	for _, c := range []struct {
		chain       string
		parent      string
		table       string
		qualifiedJa string
	}{
		{ChainIngress, "INPUT", "filter", fmt.Sprintf("-D INPUT -i %s -j %s 2>/dev/null || true", edge, ChainIngress)},
		{ChainEgress, "OUTPUT", "filter", fmt.Sprintf("-D OUTPUT -o %s -j %s 2>/dev/null || true", edge, ChainEgress)},
		{ChainForward, "FORWARD", "filter", ""},
		{ChainNAT, "POSTROUTING", "nat", ""},
	} {
		cmds = append(cmds,
			fmt.Sprintf("iptables -w 5 -t %[2]s -D %[1]s -j %[3]s 2>/dev/null || true", c.parent, c.table, c.chain),
		)
		if c.qualifiedJa != "" {
			cmds = append(cmds, fmt.Sprintf("iptables -w 5 -t %s %s", c.table, c.qualifiedJa))
		}
		cmds = append(cmds,
			fmt.Sprintf("iptables -w 5 -t %s -F %s 2>/dev/null || true", c.table, c.chain),
			fmt.Sprintf("iptables -w 5 -t %s -X %s 2>/dev/null || true", c.table, c.chain),
		)
	}
	return cmds
}

// Execute runs commands, failing on the first real error. Commands carry
// their own guards (`|| true`), so "already clean" never fails.
func Execute(r Runner, cmds []string) error {
	for _, cmd := range cmds {
		if err := r("/bin/sh", "-c", cmd); err != nil {
			return fmt.Errorf("netstate: %q: %w", cmd, err)
		}
	}
	return nil
}

func ruleAdd(spec string) string {
	return fmt.Sprintf("ip rule add %s 2>/dev/null || ip rule replace %s", spec, spec)
}

// IsExitAllowedIPs reports whether a netmap peer's AllowedIPs field carries
// the exit takeover ("0.0.0.0/0"), optionally alongside IPv6.
func IsExitAllowedIPs(allowedIPs string) bool {
	for _, cidr := range strings.Split(allowedIPs, ",") {
		if strings.TrimSpace(cidr) == "0.0.0.0/0" {
			return true
		}
	}
	return false
}
