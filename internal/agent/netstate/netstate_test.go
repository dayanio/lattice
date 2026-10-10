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

package netstate

import (
	"errors"
	"strings"
	"testing"
)

func TestExitTakeoverCommands(t *testing.T) {
	cmds := ExitTakeoverCommands("wf0")
	joined := strings.Join(cmds, "\n")

	for _, want := range []string{
		"ip rule add priority 4500 uidrange 0-0 lookup main",
		"ip rule add priority 5170 lookup main suppress_prefixlength 0",
		"ip rule add priority 5180 lookup 5180",
		"ip route replace 0.0.0.0/1 dev wf0 table 5180",
		"ip route replace 128.0.0.0/1 dev wf0 table 5180",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ExitTakeoverCommands missing %q:\n%s", want, joined)
		}
	}
	// The default route must never appear: the takeover is 0/1+128/1 only.
	if strings.Contains(joined, "default") {
		t.Errorf("ExitTakeoverCommands must not touch the default route:\n%s", joined)
	}
}

func TestRemoveAndCleanupCommandsAreGuarded(t *testing.T) {
	all := append(ExitTakeoverRemoveCommands(), CleanupCommands()...)
	for _, cmd := range all {
		switch {
		case strings.Contains(cmd, "del "), strings.Contains(cmd, "flush "),
			strings.Contains(cmd, " -F "), strings.Contains(cmd, " -X "), strings.Contains(cmd, " -D "):
			if !strings.Contains(cmd, "|| true") && !strings.Contains(cmd, "2>/dev/null") {
				t.Errorf("remove/cleanup command must be guarded, got: %s", cmd)
			}
		}
	}
}

func TestCleanupCommandsCoverAllLatticeChains(t *testing.T) {
	joined := strings.Join(CleanupCommands(), "\n")
	for _, chain := range []string{ChainIngress, ChainEgress, ChainForward, ChainNAT} {
		if !strings.Contains(joined, chain) {
			t.Errorf("CleanupCommands does not sweep chain %s", chain)
		}
	}
	// Jumps must be detached from the built-in chains.
	for _, parent := range []string{"INPUT", "OUTPUT", "FORWARD", "POSTROUTING"} {
		if !strings.Contains(joined, "-D "+parent) {
			t.Errorf("CleanupCommands does not detach jump from %s", parent)
		}
	}
}

func TestCleanupCommandsSweepTableAndRules(t *testing.T) {
	joined := strings.Join(CleanupCommands(), "\n")
	for _, want := range []string{
		"ip rule del priority 4500",
		"ip rule del priority 5170",
		"ip rule del priority 5180",
		"ip route flush table 5180",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("CleanupCommands missing %q", want)
		}
	}
}

func TestIsExitAllowedIPs(t *testing.T) {
	cases := map[string]bool{
		"0.0.0.0/0":          true,
		"0.0.0.0/0, ::/0":    true,
		"0.0.0.0/0,::/0":     true,
		"10.96.0.0/24":       false,
		"":                   false,
		"10.0.0.0/0.0.0.0/0": false,
	}
	for in, want := range cases {
		if got := IsExitAllowedIPs(in); got != want {
			t.Errorf("IsExitAllowedIPs(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestExecuteStopsOnRealError(t *testing.T) {
	var ran []string
	r := func(name string, args ...string) error {
		ran = append(ran, strings.Join(args, " "))
		if strings.Contains(strings.Join(args, " "), "boom") {
			return errors.New("exit status 1")
		}
		return nil
	}
	err := Execute(r, []string{"cmd-a", "cmd-boom", "cmd-c"})
	if err == nil {
		t.Fatal("Execute should fail on a real error")
	}
	if len(ran) != 2 {
		t.Errorf("Execute should stop at the failing command, ran %d", len(ran))
	}
}

func TestExecuteIgnoresGuardedFailures(t *testing.T) {
	// A sweep command ending in `|| true` can never fail the sequence:
	// the shell absorbs the del error, so Execute sees success.
	r := func(name string, args ...string) error { return nil }
	if err := Execute(r, CleanupCommands()); err != nil {
		t.Errorf("CleanupCommands should run clean: %v", err)
	}
}
