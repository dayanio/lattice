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

package signal

import (
	"encoding/json"
	"strings"
	"testing"
)

// ADR-0007 marks upgrade-probe signaling with fields old peers never send.
// They must be omitempty on the wire and silently ignored on decode, so a
// mixed fleet degrades to today's behavior instead of breaking handshake.
func TestUpgradeProbeFieldsRoundTrip(t *testing.T) {
	marked := SignalPacket{
		Type:      PacketType_HANDSHAKE_SYN,
		Handshake: &Handshake{IsUpgradeProbe: true, AttemptID: "a1"},
	}
	data, err := json.Marshal(marked)
	if err != nil {
		t.Fatal(err)
	}
	var back SignalPacket
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.GetHandshake() == nil || !back.GetHandshake().IsUpgradeProbe || back.GetHandshake().AttemptID != "a1" {
		t.Fatalf("marked SYN did not survive the round trip: %+v", back)
	}

	offer := SignalPacket{Type: PacketType_OFFER, Offer: &Offer{Ufrag: "u", AttemptID: "a1"}}
	data, err = json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	var backOffer SignalPacket
	if err := json.Unmarshal(data, &backOffer); err != nil {
		t.Fatal(err)
	}
	if backOffer.GetOffer() == nil || backOffer.GetOffer().AttemptID != "a1" {
		t.Fatalf("marked OFFER did not survive the round trip: %+v", backOffer)
	}
}

func TestPlainPacketsHaveNoUpgradeFieldsOnTheWire(t *testing.T) {
	plain := SignalPacket{Type: PacketType_HANDSHAKE_SYN, Handshake: &Handshake{Timestamp: 123}}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"is_upgrade_probe"`, `"attempt_id"`} {
		if strings.Contains(string(data), field) {
			t.Fatalf("plain packet carries %s: %s", field, data)
		}
	}

	offer := SignalPacket{Type: PacketType_OFFER, Offer: &Offer{Ufrag: "u", Pwd: "p"}}
	data, err = json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"attempt_id"`) {
		t.Fatalf("plain OFFER carries attempt_id: %s", data)
	}
}

// An old peer's packets (no new fields at all) decode fine.
func TestLegacyPacketDecodes(t *testing.T) {
	legacy := []byte(`{"type":1,"handshake":{"timestamp":1}}`)
	var p SignalPacket
	if err := json.Unmarshal(legacy, &p); err != nil {
		t.Fatal(err)
	}
	if p.GetHandshake() == nil || p.GetHandshake().IsUpgradeProbe || p.GetHandshake().AttemptID != "" {
		t.Fatalf("legacy SYN decoded with upgrade fields set: %+v", p)
	}
}
