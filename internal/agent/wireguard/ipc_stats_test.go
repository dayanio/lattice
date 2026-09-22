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

package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func keyOf(b byte) wgtypes.Key {
	var k wgtypes.Key
	for i := range k {
		k[i] = b
	}
	return k
}

func hexOf(k wgtypes.Key) string { return hex.EncodeToString(k[:]) }
func b64Of(k wgtypes.Key) string { return base64.StdEncoding.EncodeToString(k[:]) }

// ipcSample is what IpcGet prints: hex keys, one section per peer.
func ipcSample(a, b wgtypes.Key) string {
	return fmt.Sprintf(`private_key=%s
listen_port=51820
public_key=%s
protocol_version=1
endpoint=45.8.204.88:54886
last_handshake_time_sec=1789805000
last_handshake_time_nsec=250000000
tx_bytes=1200
rx_bytes=3400
persistent_keepalive_interval=25
allowed_ip=10.96.0.4/32
public_key=%s
protocol_version=1
endpoint=[fd6c:7270::e833:e4a3]:51820
last_handshake_time_sec=0
last_handshake_time_nsec=0
tx_bytes=7
rx_bytes=9
allowed_ip=10.96.0.9/32
`, hexOf(keyOf(0xaa)), hexOf(a), hexOf(b))
}

func TestPeerStatsFromIpc_FindsTheRequestedPeer(t *testing.T) {
	a, b := keyOf(1), keyOf(2)
	hs, rx, ep, err := PeerStatsFromIpc(ipcSample(a, b), b64Of(a))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1789805000, 250000000); !hs.Equal(want) {
		t.Errorf("handshake = %v, want %v", hs, want)
	}
	if rx != 3400 {
		t.Errorf("rx = %d, want 3400 (the first peer's, not the second's)", rx)
	}
	if ep == nil || ep.String() != "45.8.204.88:54886" {
		t.Errorf("endpoint = %v, want 45.8.204.88:54886", ep)
	}
}

func TestPeerStatsFromIpc_SecondPeerAndRelayEndpoint(t *testing.T) {
	a, b := keyOf(1), keyOf(2)
	hs, rx, ep, err := PeerStatsFromIpc(ipcSample(a, b), b64Of(b))
	if err != nil {
		t.Fatal(err)
	}
	if !hs.IsZero() {
		t.Errorf("a peer that never handshook must report the zero time, got %v", hs)
	}
	if rx != 9 {
		t.Errorf("rx = %d, want 9", rx)
	}
	if ep == nil || ep.String() != "[fd6c:7270::e833:e4a3]:51820" {
		t.Errorf("endpoint = %v, want the relay's fake address", ep)
	}
}

func TestPeerStatsFromIpc_UnknownPeer(t *testing.T) {
	if _, _, _, err := PeerStatsFromIpc(ipcSample(keyOf(1), keyOf(2)), b64Of(keyOf(9))); err == nil {
		t.Fatal("an unconfigured peer must be an error, like wgctrl reported")
	}
}

func TestPeerStatsFromIpc_PeerWithoutEndpointYet(t *testing.T) {
	a := keyOf(1)
	ipc := fmt.Sprintf("public_key=%s\ntx_bytes=1\nrx_bytes=2\nallowed_ip=10.96.0.4/32\n", hexOf(a))
	_, rx, ep, err := PeerStatsFromIpc(ipc, b64Of(a))
	if err != nil || rx != 2 || ep != nil {
		t.Fatalf("rx=%d ep=%v err=%v, want rx 2, no endpoint, no error", rx, ep, err)
	}
}

func TestPeerStatsFromIpc_RejectsAMalformedKey(t *testing.T) {
	if _, _, _, err := PeerStatsFromIpc(ipcSample(keyOf(1), keyOf(2)), "not-a-key"); err == nil {
		t.Fatal("expected an error for an invalid public key")
	}
}

// Lines this parser does not know, and junk, must not break it.
func TestPeerStatsFromIpc_IgnoresUnknownAndMalformedLines(t *testing.T) {
	a := keyOf(1)
	ipc := fmt.Sprintf("errno=0\npublic_key=%s\nfuture_field=x\nnoequals\nrx_bytes=notanumber\nrx_bytes=42\n", hexOf(a))
	if _, rx, _, err := PeerStatsFromIpc(ipc, b64Of(a)); err != nil || rx != 42 {
		t.Fatalf("rx=%d err=%v, want 42 and no error", rx, err)
	}
}

func TestPeerStatsAllFromIpc(t *testing.T) {
	a, b := keyOf(1), keyOf(2)
	all := PeerStatsAllFromIpc(ipcSample(a, b))
	if len(all) != 2 {
		t.Fatalf("peers = %d, want 2", len(all))
	}
	got := all[hexOf(a)]
	if got.RxBytes != 3400 || got.TxBytes != 1200 {
		t.Errorf("peer a counters rx/tx = %d/%d, want 3400/1200", got.RxBytes, got.TxBytes)
	}
	if want := time.Unix(1789805000, 250000000); !got.LastHandshake.Equal(want) {
		t.Errorf("peer a handshake = %v, want %v", got.LastHandshake, want)
	}
	other := all[hexOf(b)]
	if other.RxBytes != 9 || other.TxBytes != 7 || !other.LastHandshake.IsZero() {
		t.Errorf("peer b = %+v, want rx 9 tx 7, no handshake", other)
	}
}

func TestPeerStatsAllFromIpc_EmptyAndDeviceOnlyLines(t *testing.T) {
	if all := PeerStatsAllFromIpc(""); len(all) != 0 {
		t.Fatalf("empty ipc = %v, want no peers", all)
	}
	// Device-level lines before any peer section must not become a peer.
	if all := PeerStatsAllFromIpc("errno=0\nprivate_key=abc=\nlisten_port=51820\nrx_bytes=99\n"); len(all) != 0 {
		t.Fatalf("device-only ipc = %v, want no peers", all)
	}
}
