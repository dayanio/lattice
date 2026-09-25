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
	"context"
	"errors"
	"net"
	"sync"
	"testing"
)

// fakeConn satisfies net.Conn for a successful dial; the prober only closes it.
type fakeConn struct{ net.Conn }

func (fakeConn) Close() error { return nil }

type probeEnv struct {
	mu        sync.Mutex
	hasGlobal bool
	reachable map[string]bool // target -> connect succeeds
	isExit    bool
	changes   []bool
	dialed    []string
}

func newProbeEnv() *probeEnv {
	return &probeEnv{hasGlobal: true, isExit: true, reachable: map[string]bool{}}
}

func (e *probeEnv) prober(threshold int) *IPv6Prober {
	return NewIPv6Prober(IPv6ProberConfig{
		IsExit:        func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.isExit },
		HasGlobalIPv6: func() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.hasGlobal },
		Dial: func(_ context.Context, network, addr string) (net.Conn, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.dialed = append(e.dialed, network+" "+addr)
			if e.reachable[addr] {
				return fakeConn{}, nil
			}
			return nil, errors.New("unreachable")
		},
		OnChange:      func(v bool) { e.mu.Lock(); e.changes = append(e.changes, v); e.mu.Unlock() },
		Targets:       []string{"[a]:443", "[b]:443"},
		FailThreshold: threshold,
	})
}

func TestIPv6Prober_OneSuccessReportsEgress(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[b]:443"] = true // only the second target answers
	p := e.prober(2)
	p.step(context.Background())
	if !p.Egress() {
		t.Fatal("a single reachable target must report egress")
	}
	if len(e.changes) != 1 || !e.changes[0] {
		t.Fatalf("OnChange = %v, want [true]", e.changes)
	}
	for _, d := range e.dialed {
		if d[:4] != "tcp6" {
			t.Errorf("probe must dial tcp6, got %q", d)
		}
	}
}

// Withdrawing egress takes FailThreshold consecutive failures, so one dropped
// probe does not flip every consumer to the blackhole.
func TestIPv6Prober_HysteresisOnWithdraw(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[a]:443"] = true
	p := e.prober(2)
	p.step(context.Background()) // up
	e.reachable["[a]:443"] = false

	p.step(context.Background()) // failure 1
	if !p.Egress() {
		t.Fatal("one failed probe must not withdraw egress")
	}
	p.step(context.Background()) // failure 2
	if p.Egress() {
		t.Fatal("two consecutive failures must withdraw egress")
	}
	if len(e.changes) != 2 || e.changes[0] != true || e.changes[1] != false {
		t.Fatalf("OnChange = %v, want [true false]", e.changes)
	}
}

func TestIPv6Prober_SuccessResetsTheFailureCount(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[a]:443"] = true
	p := e.prober(2)
	p.step(context.Background()) // up
	e.reachable["[a]:443"] = false
	p.step(context.Background()) // failure 1
	e.reachable["[a]:443"] = true
	p.step(context.Background()) // success: counter back to zero
	e.reachable["[a]:443"] = false
	p.step(context.Background()) // failure 1 again, not 2
	if !p.Egress() {
		t.Fatal("failures separated by a success must not add up to a withdrawal")
	}
}

func TestIPv6Prober_RecoveryIsImmediate(t *testing.T) {
	e := newProbeEnv()
	p := e.prober(2)
	p.step(context.Background())
	p.step(context.Background())
	if p.Egress() {
		t.Fatal("never reachable: no egress")
	}
	e.reachable["[a]:443"] = true
	p.step(context.Background())
	if !p.Egress() {
		t.Fatal("one success after failures must report egress again")
	}
}

func TestIPv6Prober_NoGlobalAddressIsNoEgress(t *testing.T) {
	e := newProbeEnv()
	e.hasGlobal = false
	e.reachable["[a]:443"] = true // a connect would work, but there is no global address
	p := e.prober(1)
	p.step(context.Background())
	if p.Egress() {
		t.Fatal("no global IPv6 address must mean no egress")
	}
	if len(e.dialed) != 0 {
		t.Errorf("must not dial without a global address, dialed %v", e.dialed)
	}
}

func TestIPv6Prober_NonExitNeverProbesAndReportsNothing(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[a]:443"] = true
	e.isExit = false
	p := e.prober(1)
	p.step(context.Background())
	if p.Egress() || len(e.dialed) != 0 || len(e.changes) != 0 {
		t.Fatalf("non-exit: egress=%v dialed=%v changes=%v", p.Egress(), e.dialed, e.changes)
	}

	// An exit that stops being an exit drops its verdict at once (no hysteresis).
	e.isExit = true
	p.step(context.Background())
	if !p.Egress() {
		t.Fatal("expected egress once it is an exit")
	}
	e.isExit = false
	p.step(context.Background())
	if p.Egress() {
		t.Fatal("no longer an exit: egress must be withdrawn immediately")
	}
}

func TestIPv6Prober_OnChangeOnlyOnFlips(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[a]:443"] = true
	p := e.prober(1)
	for i := 0; i < 4; i++ {
		p.step(context.Background())
	}
	if len(e.changes) != 1 {
		t.Fatalf("repeated successes must fire OnChange once, got %v", e.changes)
	}
}

func TestHostHasGlobalIPv6_DoesNotPanic(t *testing.T) {
	// The answer depends on the machine running the test; only exercise the path.
	_ = hostHasGlobalIPv6()
}

// Egress is only reported once the gateway rules are in place, and a failed
// setup is retried on the next probe rather than reported as egress.
func TestIPv6Prober_SetupMustSucceedBeforeEgressIsReported(t *testing.T) {
	e := newProbeEnv()
	e.reachable["[a]:443"] = true
	setups, failSetup := 0, true
	p := NewIPv6Prober(IPv6ProberConfig{
		HasGlobalIPv6: func() bool { return true },
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			if e.reachable[addr] {
				return fakeConn{}, nil
			}
			return nil, errors.New("unreachable")
		},
		Setup: func() error {
			setups++
			if failSetup {
				return errors.New("ip6tables missing")
			}
			return nil
		},
		OnChange:      func(v bool) { e.changes = append(e.changes, v) },
		Targets:       []string{"[a]:443"},
		FailThreshold: 2,
	})

	p.step(context.Background())
	if p.Egress() || len(e.changes) != 0 {
		t.Fatalf("setup failed: egress must not be reported (egress=%v changes=%v)", p.Egress(), e.changes)
	}
	failSetup = false
	p.step(context.Background())
	if !p.Egress() || setups != 2 {
		t.Fatalf("setup succeeded on retry: egress=%v setups=%d", p.Egress(), setups)
	}
	p.step(context.Background())
	if setups != 2 {
		t.Errorf("setup must not rerun while egress is already reported, ran %d times", setups)
	}
}
