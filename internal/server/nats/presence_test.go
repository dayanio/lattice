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
	"sync"
	"testing"
	"time"
)

type presenceEvent struct {
	appID  string
	online bool
}

func recordChanges(s *NodePresenceStore) (*[]presenceEvent, *sync.Mutex) {
	var mu sync.Mutex
	var events []presenceEvent
	s.SetOnChange(func(appID string, online bool) {
		mu.Lock()
		events = append(events, presenceEvent{appID, online})
		mu.Unlock()
	})
	return &events, &mu
}

// The first heartbeat (after a server restart every node starts unknown) and
// a heartbeat after an offline period are "came online" transitions;
// heartbeats while already online are not.
func TestPresence_OnChangeFiresOnlyOnTransitions(t *testing.T) {
	s := NewNodePresenceStore()
	events, mu := recordChanges(s)

	s.Update("exit")
	s.Update("exit")
	s.UpdateWithVersion("exit", "v1")

	mu.Lock()
	defer mu.Unlock()
	if len(*events) != 1 || (*events)[0] != (presenceEvent{"exit", true}) {
		t.Fatalf("want exactly one online event, got %+v", *events)
	}
}

// A node whose heartbeats stopped is reported offline once, by the sweep, and
// a later heartbeat reports it online again.
func TestPresence_SweepReportsOfflineOnceThenOnlineAgain(t *testing.T) {
	s := NewNodePresenceStore()
	events, mu := recordChanges(s)

	s.Update("exit")
	s.Update("other")

	later := time.Now().Add(offlineThreshold + time.Second)
	s.sweepAt(later)
	s.sweepAt(later.Add(time.Minute)) // already reported: no repeat

	mu.Lock()
	offline := 0
	for _, e := range *events {
		if !e.online {
			offline++
		}
	}
	mu.Unlock()
	if offline != 2 {
		t.Fatalf("want one offline event per node (2), got %d: %+v", offline, *events)
	}

	s.Update("exit")
	mu.Lock()
	defer mu.Unlock()
	last := (*events)[len(*events)-1]
	if last != (presenceEvent{"exit", true}) {
		t.Fatalf("want exit back online, got %+v", last)
	}
}

// Sweep must not flag a node that heartbeated recently.
func TestPresence_SweepKeepsFreshNodesOnline(t *testing.T) {
	s := NewNodePresenceStore()
	events, mu := recordChanges(s)

	s.Update("exit")
	s.sweepAt(time.Now().Add(offlineThreshold / 2))

	mu.Lock()
	defer mu.Unlock()
	for _, e := range *events {
		if !e.online {
			t.Fatalf("fresh node reported offline: %+v", *events)
		}
	}
}

// A node's reported IPv6 egress capability is remembered, and the callback fires
// only when it actually flips. "No" is the default, so a node's first report of
// "no" (every heartbeat from a non-exit or an old agent) changes nothing.
func TestPresence_IPv6EgressFiresOnlyOnChange(t *testing.T) {
	s := NewNodePresenceStore()
	var mu sync.Mutex
	var got []presenceEvent
	s.SetOnIPv6EgressChange(func(appID string, egress bool) {
		mu.Lock()
		got = append(got, presenceEvent{appID, egress})
		mu.Unlock()
	})

	s.UpdateIPv6Egress("exit", false) // default: not a change
	s.UpdateIPv6Egress("exit", true)  // gained
	s.UpdateIPv6Egress("exit", true)  // repeat
	s.UpdateIPv6Egress("exit", false) // lost
	s.UpdateIPv6Egress("exit", false) // repeat

	mu.Lock()
	defer mu.Unlock()
	want := []presenceEvent{{"exit", true}, {"exit", false}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("callback events = %+v, want %+v", got, want)
	}
}

func TestPresence_IPv6EgressIsPerNode(t *testing.T) {
	s := NewNodePresenceStore()
	s.UpdateIPv6Egress("exit-a", true)
	if !s.IPv6Egress("exit-a") {
		t.Fatal("exit-a should report egress")
	}
	if s.IPv6Egress("exit-b") || s.IPv6Egress("never-seen") {
		t.Fatal("other nodes must default to no egress")
	}
	s.UpdateIPv6Egress("exit-a", false)
	if s.IPv6Egress("exit-a") {
		t.Fatal("egress should be gone after the node reports false")
	}
}
