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
	"net/netip"
	"strings"
	"testing"
)

func TestExitGateway6Commands(t *testing.T) {
	cmds := ExitGateway6Commands("lattice0", "fd6c:7270:6c74::/64")

	// accept_ra must be set BEFORE forwarding is turned on, or a SLAAC host
	// loses its default route.
	raIdx, fwdIdx := -1, -1
	for i, c := range cmds {
		if strings.Contains(c, "accept_ra=2") {
			raIdx = i
		}
		if strings.Contains(c, "net.ipv6.conf.all.forwarding=1") {
			fwdIdx = i
		}
	}
	if raIdx < 0 || fwdIdx < 0 || raIdx > fwdIdx {
		t.Fatalf("accept_ra=2 (%d) must come before enabling forwarding (%d):\n%s", raIdx, fwdIdx, strings.Join(cmds, "\n"))
	}

	joined := strings.Join(cmds, "\n")
	for _, want := range []string{
		"ip6tables -w 5 -A FORWARD -i lattice0 -j ACCEPT",
		"FORWARD -o lattice0 -m conntrack --ctstate ESTABLISHED,RELATED",
		"-t nat -A POSTROUTING -o \"$WAN\" -s fd6c:7270:6c74::/64 -j MASQUERADE",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("commands missing %q:\n%s", want, joined)
		}
	}
	// The WAN interface comes from the IPv6 default route, not the IPv4 one.
	if strings.Contains(joined, "ip route show default") && !strings.Contains(joined, "ip -6 route show default") {
		t.Error("WAN lookup must use the IPv6 default route")
	}
	// Every ip6tables rule is a check-then-add pair, so repeated runs stay clean.
	for _, c := range cmds {
		if strings.Contains(c, "ip6tables") && !strings.Contains(c, " -C ") {
			t.Errorf("ip6tables rule not idempotent (no -C check): %s", c)
		}
	}
}

func TestEnsureExitGateway6(t *testing.T) {
	var ran []string
	runner := func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	if err := EnsureExitGateway6(runner, "lattice0", "linux"); err != nil {
		t.Fatalf("EnsureExitGateway6: %v", err)
	}
	if len(ran) != 5 {
		t.Errorf("expected 5 commands on linux, got %d: %v", len(ran), ran)
	}
	for _, r := range ran {
		if !strings.HasPrefix(r, "/bin/sh -c ") {
			t.Errorf("unexpected runner call: %s", r)
		}
	}

	for _, tc := range []struct{ dev, goos string }{
		{"lattice0", "darwin"}, {"lattice0", "windows"}, {"", "linux"},
	} {
		ran = nil
		if err := EnsureExitGateway6(runner, tc.dev, tc.goos); err != nil || len(ran) != 0 {
			t.Errorf("%+v: want silent no-op, got err=%v ran=%v", tc, err, ran)
		}
	}
}

func TestEnsureExitGateway6_StopsAtFirstFailure(t *testing.T) {
	calls := 0
	runner := func(string, ...string) error {
		calls++
		return errors.New("no IPv6 default route")
	}
	err := EnsureExitGateway6(runner, "lattice0", "linux")
	if err == nil {
		t.Fatal("expected the failure to be reported")
	}
	if calls != 1 {
		t.Errorf("must stop at the first failing command (the accept_ra step), ran %d", calls)
	}
}

func TestApplyOverlay6(t *testing.T) {
	var ran []string
	runner := func(name string, args ...string) error {
		ran = append(ran, strings.Join(args, " "))
		return nil
	}
	addr := netip.MustParseAddr("fd6c:7270:6c74::a60:4")
	if err := ApplyOverlay6(runner, "lattice0", addr, "linux"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-c ip -6 addr replace fd6c:7270:6c74::a60:4/128 dev lattice0",
		"-c ip -6 route replace fd6c:7270:6c74::/64 dev lattice0",
	}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %v, want %v", ran, want)
	}

	for _, tc := range []struct {
		dev, goos string
		addr      netip.Addr
	}{
		{"lattice0", "darwin", addr},
		{"lattice0", "windows", addr},
		{"", "linux", addr},
		{"lattice0", "linux", netip.MustParseAddr("10.96.0.4")},
		{"lattice0", "linux", netip.Addr{}},
	} {
		ran = nil
		if err := ApplyOverlay6(runner, tc.dev, tc.addr, tc.goos); err != nil || len(ran) != 0 {
			t.Errorf("%+v: want silent no-op, got err=%v ran=%v", tc, err, ran)
		}
	}
}
