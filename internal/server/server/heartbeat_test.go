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

package server

import (
	"testing"

	managementnats "github.com/alatticeio/lattice/internal/server/nats"
)

// A heartbeat that carries ipv6Egress records the exit node's capability; one
// that omits it (an agent that predates the field) reads as "no".
func TestHeartbeat_RecordsIPv6Egress(t *testing.T) {
	s := &Server{presence: managementnats.NewNodePresenceStore()}

	if _, err := s.Heartbeat([]byte(`{"appId":"exit","configVersion":"v1","ipv6Egress":true}`)); err != nil {
		t.Fatal(err)
	}
	if !s.presence.IPv6Egress("exit") {
		t.Fatal("ipv6Egress=true in the heartbeat should be recorded")
	}

	if _, err := s.Heartbeat([]byte(`{"appId":"exit","configVersion":"v2"}`)); err != nil {
		t.Fatal(err)
	}
	if s.presence.IPv6Egress("exit") {
		t.Fatal("a heartbeat without the field must clear the capability")
	}
	if status, _ := s.presence.GetStatus("exit"); status != "online" {
		t.Fatalf("status = %q, want online: heartbeats must still count as presence", status)
	}
}

func TestHeartbeat_MalformedPayloadIsAnError(t *testing.T) {
	s := &Server{presence: managementnats.NewNodePresenceStore()}
	if _, err := s.Heartbeat([]byte("not json")); err == nil {
		t.Fatal("expected an error for a malformed heartbeat")
	}
}
