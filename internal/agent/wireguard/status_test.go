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
	"bytes"
	"net"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestTransportDescription(t *testing.T) {
	for state, want := range map[string]string{
		"ice-ready": "ice-ready (direct)",
		"relay-ready": "relay-ready (relayed)",
		"probing":   "probing",
		"failed":    "failed",
		"closed":    "closed",
		"none":      "none",
	} {
		if got := transportDescription(state); got != want {
			t.Errorf("transportDescription(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestWritePeerShowsNameAndTransportWhenLabelled(t *testing.T) {
	var buf bytes.Buffer
	writePeer(&buf, wgtypes.Peer{}, &PeerLabel{Name: "macbook-pro.local", Transport: "ice-ready"})

	out := buf.String()
	for _, want := range []string{
		"  Name      : macbook-pro.local\n",
		"  Transport : ice-ready (direct)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWritePeerOmitsNameAndTransportWithoutLabel(t *testing.T) {
	var buf bytes.Buffer
	writePeer(&buf, wgtypes.Peer{}, nil)

	out := buf.String()
	for _, unwanted := range []string{"Name", "Transport"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("unlabelled peer output contains %q:\n%s", unwanted, out)
		}
	}
}

func TestWriteUnmatchedPeersListsOnlyPeersWithoutSession(t *testing.T) {
	labels := map[string]PeerLabel{
		"key-connected": {Name: "cloud-node-1", Transport: "ice-ready"},
		"key-phone":     {Name: "iPhone", Transport: "failed"},
		"key-old":       {Name: "mac-cloud-node-1", Transport: "probing"},
	}
	seen := map[string]bool{"key-connected": true}

	var buf bytes.Buffer
	writeUnmatchedPeers(&buf, labels, seen)

	out := buf.String()
	if strings.Contains(out, "ice-ready") {
		t.Errorf("peer with a WireGuard session must not be listed:\n%s", out)
	}
	iPhone := strings.Index(out, "iPhone")
	old := strings.Index(out, "mac-cloud-node-1")
	if iPhone < 0 || old < 0 {
		t.Fatalf("unmatched peers missing:\n%s", out)
	}
	if iPhone > old {
		t.Errorf("peers must be sorted by name (iPhone before mac-cloud-node-1):\n%s", out)
	}
	if !strings.Contains(out, "failed") || !strings.Contains(out, "probing") {
		t.Errorf("transport states missing:\n%s", out)
	}
}

func TestWriteUnmatchedPeersPrintsNothingWhenAllHaveSessions(t *testing.T) {
	var buf bytes.Buffer
	writeUnmatchedPeers(&buf, map[string]PeerLabel{"k": {Name: "a", Transport: "ice-ready"}}, map[string]bool{"k": true})
	if buf.Len() != 0 {
		t.Errorf("expected no output, got:\n%s", buf.String())
	}
}

var (
	relayEndpoint  = &net.UDPAddr{IP: net.ParseIP("fd6c:7270::e833:e4a3"), Port: 51820}
	directEndpoint = &net.UDPAddr{IP: net.ParseIP("45.8.204.88"), Port: 35062}
)

func TestActualPath(t *testing.T) {
	for name, tc := range map[string]struct {
		ep   *net.UDPAddr
		want string
	}{
		"no endpoint yet":       {nil, ""},
		"public v4 address":     {directEndpoint, "direct"},
		"public v6 address":     {&net.UDPAddr{IP: net.ParseIP("2409:8a20::1"), Port: 51820}, "direct"},
		"relay fake address":    {relayEndpoint, "relayed"},
		"private v4 is direct":  {&net.UDPAddr{IP: net.ParseIP("192.168.1.8"), Port: 51820}, "direct"},
		"other ULA is not fake": {&net.UDPAddr{IP: net.ParseIP("fd00::1"), Port: 51820}, "direct"},
	} {
		if got := actualPath(tc.ep); got != tc.want {
			t.Errorf("%s: actualPath(%v) = %q, want %q", name, tc.ep, got, tc.want)
		}
	}
}

func TestWritePeerReportsTheActualPath(t *testing.T) {
	var buf bytes.Buffer
	writePeer(&buf, wgtypes.Peer{Endpoint: relayEndpoint}, nil)
	if !strings.Contains(buf.String(), "  Path      : relayed\n") {
		t.Errorf("relayed endpoint not reported:\n%s", buf.String())
	}

	buf.Reset()
	writePeer(&buf, wgtypes.Peer{Endpoint: directEndpoint}, nil)
	if !strings.Contains(buf.String(), "  Path      : direct\n") {
		t.Errorf("direct endpoint not reported:\n%s", buf.String())
	}
}

func TestWritePeerFlagsAProbeThatDisagreesWithTheEndpoint(t *testing.T) {
	// The probe says direct, but WireGuard is talking through the relay.
	var buf bytes.Buffer
	writePeer(&buf, wgtypes.Peer{Endpoint: relayEndpoint}, &PeerLabel{Name: "x", Transport: "ice-ready"})
	out := buf.String()
	if !strings.Contains(out, "  Path      : relayed\n") || !strings.Contains(out, "differs from the transport state") {
		t.Errorf("mismatch must be called out:\n%s", out)
	}

	// Agreeing states must not print the note.
	for _, c := range []struct {
		ep    *net.UDPAddr
		state string
	}{{directEndpoint, "ice-ready"}, {relayEndpoint, "relay-ready"}} {
		buf.Reset()
		writePeer(&buf, wgtypes.Peer{Endpoint: c.ep}, &PeerLabel{Name: "x", Transport: c.state})
		if strings.Contains(buf.String(), "differs from the transport state") {
			t.Errorf("state %s with endpoint %v must not be flagged:\n%s", c.state, c.ep, buf.String())
		}
	}
}
