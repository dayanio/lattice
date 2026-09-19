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

const upgradeMaxInterval = 30 * time.Minute

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
// Only the initiator schedules: a restart makes the remote side restart too,
// so both sides doing it would collide. The retry is a full probe restart
// (the signaling has no "upgrade only" flag), which costs a brief tunnel
// interruption, so attempts back off exponentially.
func (p *Probe) scheduleUpgrade() {
	if !isInitiator(p.localId, p.remoteId) {
		return
	}
	p.upgradeMu.Lock()
	defer p.upgradeMu.Unlock()
	if p.upgradeTimer != nil {
		p.upgradeTimer.Stop()
	}
	epoch := p.epoch.Load()
	p.upgradeTimer = time.AfterFunc(upgradeDelay(p.upgradeTries), func() { p.tryUpgrade(epoch) })
}

func (p *Probe) tryUpgrade(epoch uint64) {
	if p.epoch.Load() != epoch || p.sm.Current() != StateLRPReady {
		return
	}
	p.upgradeMu.Lock()
	p.upgradeTries++
	tries := p.upgradeTries
	p.upgradeMu.Unlock()
	p.log.Info("relayed for a while, retrying a direct connection", "remoteId", p.remoteId.AppID, "attempt", tries)
	if p.upgradeRestart != nil {
		p.upgradeRestart()
		return
	}
	p.restart()
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
