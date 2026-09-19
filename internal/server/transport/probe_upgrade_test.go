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

package transport

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestUpgradeDelay_DoublesAndCaps(t *testing.T) {
	prev := upgradeBaseInterval
	upgradeBaseInterval = 2 * time.Minute
	t.Cleanup(func() { upgradeBaseInterval = prev })

	for attempts, want := range map[int]time.Duration{
		0: 2 * time.Minute, 1: 4 * time.Minute, 2: 8 * time.Minute,
		3: 16 * time.Minute, 4: upgradeMaxInterval, 10: upgradeMaxInterval, 200: upgradeMaxInterval,
	} {
		if got := upgradeDelay(attempts); got != want {
			t.Errorf("upgradeDelay(%d) = %v, want %v", attempts, got, want)
		}
	}
}

func fastUpgrade(t *testing.T) {
	t.Helper()
	prev := upgradeBaseInterval
	upgradeBaseInterval = 20 * time.Millisecond
	t.Cleanup(func() { upgradeBaseInterval = prev })
}

// newUpgradeProbe builds a probe whose upgrade restart is observable. The
// initiator is the side with the numerically larger peer ID.
func newUpgradeProbe(t *testing.T, initiator bool, fired *atomic.Int32) *Probe {
	t.Helper()
	big := infra.NewPeerIdentity("big", wgtypes.Key{2})
	small := infra.NewPeerIdentity("small", wgtypes.Key{1})
	local, remote := small, big
	if initiator {
		local, remote = big, small
	}
	return &Probe{
		sm:             NewStateMachine(StateProbing),
		localId:        local,
		remoteId:       remote,
		log:            log.GetLogger("test-probe"),
		upgradeRestart: func() { fired.Add(1) },
	}
}

func TestUpgrade_RelayedInitiatorRetriesDirect(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, true, &fired)

	p.onSuccess(&mockTransport{tp: infra.LRP, addr: "fake"})

	waitUntil(t, time.Second, func() bool { return fired.Load() == 1 }, "a relayed initiator must retry a direct connection")
}

func TestUpgrade_ResponderNeverInitiates(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, false, &fired)

	p.onSuccess(&mockTransport{tp: infra.LRP, addr: "fake"})

	time.Sleep(150 * time.Millisecond)
	if n := fired.Load(); n != 0 {
		t.Fatalf("responder fired %d upgrade restart(s); only the initiator may, or both sides would restart at once", n)
	}
}

func TestUpgrade_DirectConnectionSchedulesNothing(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, true, &fired)

	p.onSuccess(&mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"})

	time.Sleep(150 * time.Millisecond)
	if n := fired.Load(); n != 0 {
		t.Fatalf("a direct connection fired %d upgrade restart(s)", n)
	}
}

// The ICE dial that races the relay can win late; that upgrade must cancel the
// pending retry instead of restarting a connection that is already direct.
func TestUpgrade_LateICEWinCancelsTheRetry(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, true, &fired)

	p.onSuccess(&mockTransport{tp: infra.LRP, addr: "fake"})
	if err := p.handleUpgradeTransport(&mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)
	if n := fired.Load(); n != 0 {
		t.Fatalf("retry fired %d time(s) after the connection was already upgraded", n)
	}
}

func TestUpgrade_StaleTimerIsIgnoredAfterRestart(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, true, &fired)

	p.onSuccess(&mockTransport{tp: infra.LRP, addr: "fake"})
	p.epoch.Add(1) // any restart bumps the epoch

	time.Sleep(150 * time.Millisecond)
	if n := fired.Load(); n != 0 {
		t.Fatalf("stale timer fired %d time(s)", n)
	}
}

func TestUpgrade_AttemptsResetOnceDirect(t *testing.T) {
	fastUpgrade(t)
	var fired atomic.Int32
	p := newUpgradeProbe(t, true, &fired)

	p.onSuccess(&mockTransport{tp: infra.LRP, addr: "fake"})
	waitUntil(t, time.Second, func() bool { return fired.Load() == 1 }, "first retry")
	p.upgradeMu.Lock()
	tries := p.upgradeTries
	p.upgradeMu.Unlock()
	if tries != 1 {
		t.Fatalf("upgradeTries = %d after one retry, want 1", tries)
	}

	if err := p.handleUpgradeTransport(&mockTransport{tp: infra.ICE, addr: "1.2.3.4:5"}); err != nil {
		t.Fatal(err)
	}
	p.upgradeMu.Lock()
	tries = p.upgradeTries
	p.upgradeMu.Unlock()
	if tries != 0 {
		t.Fatalf("upgradeTries = %d after reaching direct, want the backoff reset to 0", tries)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}
