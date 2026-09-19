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
	"testing"
	"time"
)

func TestRelayFailLimiter_LogsOncePerDestinationPerWindow(t *testing.T) {
	var l relayFailLimiter
	t0 := time.Unix(1000, 0)

	if !l.allow(7, t0) {
		t.Fatal("the first failure must be logged")
	}
	if l.allow(7, t0.Add(10*time.Second)) {
		t.Fatal("a second failure for the same destination inside the window must be suppressed")
	}
	if !l.allow(8, t0.Add(10*time.Second)) {
		t.Fatal("another destination has its own window")
	}
	if !l.allow(7, t0.Add(relayFailLogEvery)) {
		t.Fatal("the destination must be logged again once the window has passed")
	}
}

func TestRelayFailLimiter_ForgetsExpiredEntriesWhenFull(t *testing.T) {
	var l relayFailLimiter
	t0 := time.Unix(1000, 0)
	for i := uint32(0); i < relayFailLogMax; i++ {
		l.allow(i, t0)
	}
	l.allow(relayFailLogMax+1, t0.Add(2*relayFailLogEvery))
	if n := len(l.last); n > 2 {
		t.Fatalf("tracked destinations = %d, want the expired ones dropped", n)
	}
}
