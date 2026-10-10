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

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/netstate"
	"github.com/spf13/cobra"
)

// netCmd exposes the crash-safety tooling from
// docs/superpowers/specs/2026-10-10-exit-node-crash-safety-design.md:
// every piece of host network state lattice writes (policy table 5180,
// LATTICE-* iptables chains) can be swept by `lattice net cleanup` even
// when lattice itself will not start again.
func netCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "net",
		Short: "Host network state diagnostics and repair",
		Long: `Inspect and sweep the host network state lattice applies outside its
own process: policy-routing table 5180, its ip rules, and the LATTICE-*
iptables chains. The sweep is idempotent and guarded — safe to run at any
time, even when lattice is not running.`,
	}
	cmd.AddCommand(netCleanupCmd(), netStatusCmd())
	return cmd
}

func netCleanupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cleanup",
		Short: "Sweep all lattice network state (routes, ip rules, iptables chains)",
		Long: `Removes every piece of host network state lattice may have written:
the exit-takeover ip rules (priorities 4500/5170/5180), policy table 5180,
and the LATTICE-FORWARD / LATTICE-NAT / LATTICE-INGRESS / LATTICE-EGRESS
iptables chains including their single jumps from the built-in chains.

Idempotent and safe offline — this is what systemd runs on ExecStopPost and
ExecStartPre, and the command to reach for when lattice crashed and left the
network in a bad state.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "linux" {
				fmt.Println("nothing to clean: lattice writes no host network state on", runtime.GOOS)
				return nil
			}
			if err := netstate.Cleanup(infra.ExecCommand); err != nil {
				return err
			}
			fmt.Println("lattice network state swept: table 5180, exit-takeover ip rules and LATTICE-* chains removed")
			return nil
		},
	}
}

func netStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show lattice-owned host network state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "linux" {
				fmt.Println("lattice writes no host network state on", runtime.GOOS)
				return nil
			}
			for _, q := range []struct {
				title, cmd string
			}{
				{"ip rules (lattice priorities 4500/5170/5180)", "ip rule show | grep -E '45[0-9]{2}|51[78]0' || true"},
				{"routes in table 5180", "ip route show table 5180 || true"},
				{"iptables chains and rules", "iptables-save 2>/dev/null | grep -i lattice || true"},
			} {
				out, err := exec.Command("/bin/sh", "-c", q.cmd).CombinedOutput()
				if err != nil {
					return err
				}
				fmt.Printf("== %s ==\n%s\n", q.title, strings.TrimRight(string(out), "\n"))
			}
			if b, err := os.ReadFile("/var/lib/lattice/net-state.json"); err == nil {
				fmt.Printf("== last applied state ==\n%s\n", string(b))
			}
			return nil
		},
	}
}
