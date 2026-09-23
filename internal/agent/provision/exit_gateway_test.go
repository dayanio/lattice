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
	"errors"
	"strings"
	"testing"
)

func TestExitGatewayCommands(t *testing.T) {
	cmds := ExitGatewayCommands("lattice0", "10.96.0.0/24")
	joined := strings.Join(cmds, "\n")
	for _, want := range []string{
		"net.ipv4.ip_forward=1",
		"FORWARD -i lattice0 -j ACCEPT",
		"FORWARD -o lattice0",
		"ESTABLISHED,RELATED",
		"POSTROUTING -o \"$DEV\" -s 10.96.0.0/24 -j MASQUERADE",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("commands missing %q:\n%s", want, joined)
		}
	}
	// 每条都必须是 check→add 幂等对。
	for _, cmd := range cmds {
		if strings.Contains(cmd, "iptables") && !strings.Contains(cmd, " -C ") {
			t.Errorf("iptables rule not idempotent (no -C check): %s", cmd)
		}
	}
}

func TestMeshCIDRFromAddr(t *testing.T) {
	cases := map[string]string{
		"10.96.0.2": "10.96.0.0/24",
		"10.96.0.6": "10.96.0.0/24",
		"not-an-ip": "",
		"::1":       "",
	}
	for addr, want := range cases {
		if got := MeshCIDRFromAddr(addr); got != want {
			t.Errorf("MeshCIDRFromAddr(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestEnsureExitGateway(t *testing.T) {
	var ran []string
	runner := func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}

	if err := EnsureExitGateway(runner, "lattice0", "10.96.0.0/24", "linux"); err != nil {
		t.Fatalf("EnsureExitGateway: %v", err)
	}
	if len(ran) != 4 {
		t.Errorf("expected 4 commands on linux, got %d", len(ran))
	}

	ran = nil
	if err := EnsureExitGateway(runner, "lattice0", "10.96.0.0/24", "darwin"); err != nil {
		t.Fatalf("EnsureExitGateway(darwin): %v", err)
	}
	if len(ran) != 0 {
		t.Errorf("expected no-op on non-linux, ran %v", ran)
	}

	ran = nil
	if err := EnsureExitGateway(runner, "", "10.96.0.0/24", "linux"); err != nil {
		t.Fatalf("EnsureExitGateway(empty dev): %v", err)
	}
	if len(ran) != 0 {
		t.Errorf("expected no-op for empty dev, ran %v", ran)
	}

	ran = nil
	fail := func(name string, args ...string) error { return errors.New("boom") }
	if err := EnsureExitGateway(fail, "lattice0", "10.96.0.0/24", "linux"); err == nil {
		t.Error("expected error when a rule fails")
	}
}
