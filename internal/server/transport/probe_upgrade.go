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

import "time"

// upgradeBaseInterval is the first wait before a relayed initiator retries a
// direct connection; it doubles per failed attempt up to upgradeMaxInterval.
var upgradeBaseInterval = 2 * time.Minute

// UpgradeDisabled turns the periodic relay→direct upgrade retry off entirely.
// Since ADR-0007 the retry is make-before-break (a shadow ICE probe beside
// the relay path), so it no longer interrupts traffic; the kill switch is
// kept for field diagnosis.
var UpgradeDisabled = false

const upgradeMaxInterval = 30 * time.Minute

// upgradeSignalRetry is how soon a retry that found signaling down is tried
// again; it does not count as an attempt.
var upgradeSignalRetry = 15 * time.Second

// upgradeDelay is the wait before retry number attempts+1: base, 2*base, ...
// capped at upgradeMaxInterval.
func upgradeDelay(attempts int) time.Duration {
	d := upgradeBaseInterval
	for i := 0; i < attempts && d < upgradeMaxInterval; i++ {
		d *= 2
	}
	return min(d, upgradeMaxInterval)
}

// scheduleUpgrade arms a retry of the direct (ICE) path while this probe is
// carried by the relay. When ICE fails during the initial race the probe
// stays relayed until something else forces a restart, i.e. traffic keeps
// flowing through the relay even after the direct path becomes usable again.
//
// Only the initiator schedules: the responder opens its own shadow when the
// marked SYN arrives, so both sides doing it would collide. The retry is a
// make-before-break background probe (ADR-0007) that never touches the relay
// path, but each attempt still costs a signaling round and an ICE run, so
// attempts back off exponentially.
func (p *Probe) scheduleUpgrade() {
	if UpgradeDisabled {
		p.log.Debug("upgrade retry disabled by kill switch", "remoteId", p.remoteId.AppID)
		return
	}
	if !isInitiator(p.localId, p.remoteId) {
		p.log.Debug("upgrade retry skipped: responder", "remoteId", p.remoteId.AppID)
		return
	}
	p.upgradeMu.Lock()
	defer p.upgradeMu.Unlock()
	p.armUpgradeLocked(upgradeDelay(p.upgradeTries))
}

func (p *Probe) armUpgradeLocked(delay time.Duration) {
	if p.upgradeTimer != nil {
		p.upgradeTimer.Stop()
	}
	epoch := p.epoch.Load()
	p.log.Info("upgrade retry armed", "remoteId", p.remoteId.AppID, "delay", delay, "tries", p.upgradeTries, "epoch", epoch)
	p.upgradeTimer = time.AfterFunc(delay, func() { p.tryUpgrade(epoch) })
}

func (p *Probe) tryUpgrade(epoch uint64) {
	if cur := p.epoch.Load(); cur != epoch {
		p.log.Debug("upgrade retry skipped: epoch moved", "remoteId", p.remoteId.AppID, "armed", epoch, "current", cur)
		return
	}
	if state := p.sm.Current(); state != StateRelayReady {
		p.log.Debug("upgrade retry skipped: not relay-ready", "remoteId", p.remoteId.AppID, "state", state)
		return
	}
	// The attempt renegotiates over signaling. With signaling down (e.g.
	// right after a network switch) it cannot complete — but unlike the old
	// restart-based retry it would no longer tear anything down; it is
	// simply pointless, so wait for signaling instead of burning an attempt.
	if cs, ok := p.signal.(interface{ Connected() bool }); ok && !cs.Connected() {
		p.log.Info("upgrade retry waits for signaling", "remoteId", p.remoteId.AppID, "recheck", upgradeSignalRetry)
		p.upgradeMu.Lock()
		p.armUpgradeLocked(upgradeSignalRetry)
		p.upgradeMu.Unlock()
		return
	}
	p.upgradeMu.Lock()
	p.upgradeTries++
	tries := p.upgradeTries
	p.upgradeMu.Unlock()
	p.log.Info("probing a direct connection in the background", "remoteId", p.remoteId.AppID, "attempt", tries)
	if p.startUpgradeShadow != nil {
		p.startUpgradeShadow(epoch)
		return
	}
	p.beginUpgradeAttempt(epoch)
}

// cancelUpgrade stops any pending retry. reset also clears the backoff, for
// when a direct connection was reached.
func (p *Probe) cancelUpgrade(reset bool) {
	p.upgradeMu.Lock()
	defer p.upgradeMu.Unlock()
	if p.upgradeTimer != nil {
		p.upgradeTimer.Stop()
		p.upgradeTimer = nil
	}
	if reset {
		p.upgradeTries = 0
	}
}
