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
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/config"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ── helpers ──────────────────────────────────────────────────────────────

// startTestRelay runs the relay's TCP upgrade handler on a real socket.
func startTestRelay(t *testing.T, requirePeerAuth bool) (*Server, *httptest.Server) {
	t.Helper()
	s := NewServer(&config.Config{LrpRequirePeerAuth: requirePeerAuth})
	ts := httptest.NewServer(http.HandlerFunc(s.boltUpgradeHandler))
	t.Cleanup(ts.Close)
	return s, ts
}

func dialUpgrade(t *testing.T, ts *httptest.Server) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck

	req, err := http.NewRequest("GET", "/lrp/v1/upgrade", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Upgrade", "lrp")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close() //nolint:errcheck
		t.Fatalf("upgrade failed: %v %+v", err, resp)
	}
	return conn, reader
}

func writeRegister(t *testing.T, conn net.Conn, claimedID uint32) {
	t.Helper()
	h := Header{Cmd: Register, ToID: claimedID}
	if _, err := conn.Write(h.Marshal()); err != nil {
		t.Fatal(err)
	}
}

func writeFrame(t *testing.T, conn net.Conn, cmd uint8, payload []byte) {
	t.Helper()
	h := Header{Cmd: cmd, PayloadLen: uint32(len(payload))}
	if _, err := conn.Write(h.Marshal()); err != nil {
		t.Fatal(err)
	}
	if len(payload) > 0 {
		if _, err := conn.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
}

func readFrame(t *testing.T, reader *bufio.Reader) (*Header, []byte) {
	t.Helper()
	head := make([]byte, HeaderSize)
	if _, err := io.ReadFull(reader, head); err != nil {
		t.Fatal(err)
	}
	h, err := Unmarshal(head)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, h.PayloadLen)
	if h.PayloadLen > 0 {
		if _, err := io.ReadFull(reader, payload); err != nil {
			t.Fatal(err)
		}
	}
	return h, payload
}

func newClientKey(t *testing.T) (wgtypes.Key, uint32) {
	t.Helper()
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	var priv [KeySize]byte
	copy(priv[:], key[:])
	var pub [KeySize]byte
	pubArr, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	copy(pub[:], pubArr)
	return key, binary.BigEndian.Uint32(pub[4:8])
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}

// ── tests ────────────────────────────────────────────────────────────────

// TestIntegration_StrictHonestClient: a client answering the challenge is
// registered and verified.
func TestIntegration_StrictHonestClient(t *testing.T) {
	srv, ts := startTestRelay(t, true)

	key, id := newClientKey(t)
	conn, reader := dialUpgrade(t, ts)
	writeRegister(t, conn, id)

	h, payload := readFrame(t, reader)
	if h.Cmd != AuthChallenge || len(payload) != KeySize {
		t.Fatalf("expected auth challenge, got cmd=%d len=%d", h.Cmd, len(payload))
	}
	var challenge [KeySize]byte
	copy(challenge[:], payload)
	resp, err := answerChallenge(challenge, [KeySize]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, conn, AuthResponse, resp[:])

	waitFor(t, 2*time.Second, func() bool {
		return srv.Manager().IsVerified(uint64(id))
	}, "verified session must appear after honest proof")
}

// TestIntegration_StrictAttackerRejected: a client holding a DIFFERENT key
// but claiming a victim's ID must be rejected and must not evict the
// victim's verified session.
func TestIntegration_StrictAttackerRejected(t *testing.T) {
	srv, ts := startTestRelay(t, true)

	// Victim registers honestly first.
	victimKey, victimID := newClientKey(t)
	vconn, vreader := dialUpgrade(t, ts)
	writeRegister(t, vconn, victimID)
	_, challengePayload := readFrame(t, vreader)
	var challenge [KeySize]byte
	copy(challenge[:], challengePayload)
	resp, err := answerChallenge(challenge, [KeySize]byte(victimKey))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, vconn, AuthResponse, resp[:])
	waitFor(t, 2*time.Second, func() bool {
		return srv.Manager().IsVerified(uint64(victimID))
	}, "victim session")

	// Attacker claims the victim's ID with its own key.
	attackerKey, _ := newClientKey(t)
	aconn, areader := dialUpgrade(t, ts)
	writeRegister(t, aconn, victimID)
	_, attackerChallenge := readFrame(t, areader)
	var achallenge [KeySize]byte
	copy(achallenge[:], attackerChallenge)
	aresp, err := answerChallenge(achallenge, [KeySize]byte(attackerKey))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, aconn, AuthResponse, aresp[:])

	// Server must close the attacker's connection (EOF on next read).
	_ = aconn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	if _, err := areader.ReadByte(); err == nil {
		t.Fatal("attacker connection must be closed by the relay")
	}
	_ = buf

	// Victim session must be untouched.
	if !srv.Manager().IsVerified(uint64(victimID)) {
		t.Fatal("victim session must survive the attacker attempt")
	}
}

// TestIntegration_LenientLegacyClient: with require-peer-auth off, a bare
// Register (no auth exchange) still gets a working session.
func TestIntegration_LenientLegacyClient(t *testing.T) {
	srv, ts := startTestRelay(t, false)

	key, id := newClientKey(t)
	_ = key
	conn, _ := dialUpgrade(t, ts)
	writeRegister(t, conn, id)

	waitFor(t, 2*time.Second, func() bool {
		return srv.Manager().Get(uint64(id)) != nil
	}, "legacy register must create a session in lenient mode")
}

// TestIntegration_LenientUpgrade: a new-style client on a lenient relay
// gets its session upgraded to verified.
func TestIntegration_LenientUpgrade(t *testing.T) {
	srv, ts := startTestRelay(t, false)

	key, id := newClientKey(t)
	conn, reader := dialUpgrade(t, ts)
	writeRegister(t, conn, id)

	h, payload := readFrame(t, reader)
	if h.Cmd != AuthChallenge {
		t.Fatalf("expected opportunistic auth challenge, got cmd=%d", h.Cmd)
	}
	var challenge [KeySize]byte
	copy(challenge[:], payload)
	resp, err := answerChallenge(challenge, [KeySize]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(t, conn, AuthResponse, resp[:])

	waitFor(t, 2*time.Second, func() bool {
		return srv.Manager().IsVerified(uint64(id))
	}, "session must be upgraded to verified in lenient mode")
}
