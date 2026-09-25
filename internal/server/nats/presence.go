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

package nats

import (
	"context"
	"sync"
	"time"
)

// offlineThreshold is how long since the last heartbeat before a node is considered offline.
// Heartbeat interval is 30s, so 3 missed heartbeats = offline.
const offlineThreshold = 90 * time.Second

// NodePresenceStore is a thread-safe in-memory store that tracks the last
// heartbeat timestamp for each agent node identified by its AppID.
type NodePresenceStore struct {
	mu       sync.RWMutex
	m        map[string]time.Time // appId -> lastHeartbeat
	versions map[string]string    // appId -> last reported netmap ConfigVersion
	// online is the last state reported through onChange, so transitions fire
	// once rather than on every heartbeat or sweep.
	online   map[string]bool
	onChange func(appId string, online bool)
	// ipv6Egress is the IPv6 egress capability each node last reported in its
	// heartbeat. Absent means "no", which is also what an agent that predates
	// the field implies.
	ipv6Egress         map[string]bool
	onIPv6EgressChange func(appId string, egress bool)
}

// NewNodePresenceStore creates an empty NodePresenceStore.
func NewNodePresenceStore() *NodePresenceStore {
	return &NodePresenceStore{
		m:        make(map[string]time.Time),
		versions: make(map[string]string),
		online:   make(map[string]bool),

		ipv6Egress: make(map[string]bool),
	}
}

// SetOnChange registers a callback fired when a node comes online (first
// heartbeat, or one after an offline period) or goes offline (detected by
// Run/sweep). It is called outside the store's lock; a nil fn disables it.
func (s *NodePresenceStore) SetOnChange(fn func(appId string, online bool)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// SetOnIPv6EgressChange registers a callback fired when a node's reported IPv6
// egress capability flips. It is called outside the store's lock; a nil fn
// disables it.
func (s *NodePresenceStore) SetOnIPv6EgressChange(fn func(appId string, egress bool)) {
	s.mu.Lock()
	s.onIPv6EgressChange = fn
	s.mu.Unlock()
}

// UpdateIPv6Egress records the IPv6 egress capability a node reported with its
// heartbeat. A node's first report of "no" is not a change (that is the default),
// and repeating the current value never fires the callback.
func (s *NodePresenceStore) UpdateIPv6Egress(appId string, egress bool) {
	s.mu.Lock()
	changed := s.ipv6Egress[appId] != egress
	if egress {
		s.ipv6Egress[appId] = true
	} else {
		delete(s.ipv6Egress, appId)
	}
	fn := s.onIPv6EgressChange
	s.mu.Unlock()

	if changed && fn != nil {
		fn(appId, egress)
	}
}

// IPv6Egress reports whether the node last said it has a working IPv6 egress.
func (s *NodePresenceStore) IPv6Egress(appId string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ipv6Egress[appId]
}

// touch records a heartbeat and fires onChange when it is an offline→online
// transition (including a node's first heartbeat since the server started).
func (s *NodePresenceStore) touch(appId, version string) {
	s.mu.Lock()
	s.m[appId] = time.Now()
	if version != "" {
		s.versions[appId] = version
	}
	came := !s.online[appId]
	s.online[appId] = true
	fn := s.onChange
	s.mu.Unlock()

	if came && fn != nil {
		fn(appId, true)
	}
}

// Run sweeps for nodes whose heartbeats stopped until ctx is cancelled, so
// offline transitions are reported even though no event marks them.
func (s *NodePresenceStore) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sweepAt(now)
		}
	}
}

// sweepAt reports every node that was online but has not heartbeated within
// offlineThreshold as of now. Each node is reported once until it returns.
func (s *NodePresenceStore) sweepAt(now time.Time) {
	var went []string
	s.mu.Lock()
	for appId, up := range s.online {
		if up && now.Sub(s.m[appId]) >= offlineThreshold {
			s.online[appId] = false
			went = append(went, appId)
		}
	}
	fn := s.onChange
	s.mu.Unlock()

	if fn == nil {
		return
	}
	for _, appId := range went {
		fn(appId, false)
	}
}

// Update records a heartbeat for the given appId at the current time.
func (s *NodePresenceStore) Update(appId string) {
	s.touch(appId, "")
}

// UpdateWithVersion records a heartbeat and the ConfigVersion the node
// reports as applied (delivery-tracking for policy convergence).
func (s *NodePresenceStore) UpdateWithVersion(appId, version string) {
	s.touch(appId, version)
}

// GetVersion returns the last netmap ConfigVersion the node reported applied.
func (s *NodePresenceStore) GetVersion(appId string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.versions[appId]
}

// GetStatus returns the online status and last-seen time for the given appId.
//
// Possible status values:
//   - "online"  — heartbeat received within the last 90 seconds
//   - "offline" — heartbeat was received before, but longer than 90 seconds ago
//   - "pending" — no heartbeat ever received (node registered but never connected)
func (s *NodePresenceStore) GetStatus(appId string) (status string, lastSeen *time.Time) {
	s.mu.RLock()
	t, ok := s.m[appId]
	s.mu.RUnlock()

	if !ok {
		return "pending", nil
	}

	if time.Since(t) < offlineThreshold {
		return "online", &t
	}
	return "offline", &t
}
