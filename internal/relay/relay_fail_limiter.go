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

package relay

import (
	"sync"
	"time"
)

const (
	relayFailLogEvery = 30 * time.Second
	relayFailLogMax   = 1024
)

// relayFailLimiter rate-limits the "relay failed" warning per destination. A
// peer that is offline but still being signaled (a probe sends about one packet
// every 2 s) would otherwise fill the log with one line per packet.
type relayFailLimiter struct {
	mu   sync.Mutex
	last map[uint32]time.Time
}

// allow reports whether a failure for the destination should be logged now.
func (l *relayFailLimiter) allow(to uint32, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = make(map[uint32]time.Time)
	}
	if at, ok := l.last[to]; ok && now.Sub(at) < relayFailLogEvery {
		return false
	}
	if len(l.last) >= relayFailLogMax {
		for id, at := range l.last {
			if now.Sub(at) >= relayFailLogEvery {
				delete(l.last, id)
			}
		}
	}
	l.last[to] = now
	return true
}
