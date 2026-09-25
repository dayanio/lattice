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

package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// A node that never started the probe (every non-exit, and every platform but
// Linux) reports no IPv6 egress.
func TestNodeIPv6Egress_FalseWithoutProber(t *testing.T) {
	var n Node
	if n.IPv6Egress() {
		t.Fatal("a node without a prober must report no egress")
	}
}

// The heartbeat only mentions ipv6Egress when it is true, so the payload from a
// non-exit node is byte-for-byte what older servers already parse.
func TestHeartbeatPayload_IPv6EgressOmittedWhenFalse(t *testing.T) {
	off, err := json.Marshal(heartbeatPayload{AppID: "n1", ConfigVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(off), "ipv6Egress") {
		t.Fatalf("false must be omitted: %s", off)
	}
	on, err := json.Marshal(heartbeatPayload{AppID: "n1", ConfigVersion: "v1", IPv6Egress: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(on), `"ipv6Egress":true`) {
		t.Fatalf("true must be present: %s", on)
	}
}
