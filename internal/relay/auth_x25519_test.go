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
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"

	"golang.org/x/crypto/curve25519"
)

func testKey(t *testing.T) [KeySize]byte {
	t.Helper()
	var k [KeySize]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestAuthHappyPath(t *testing.T) {
	clientPriv := testKey(t)

	challengePub, ch, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}

	resp, err := answerChallenge(challengePub, clientPriv)
	if err != nil {
		t.Fatal(err)
	}

	clientPubPub, err := curve25519.X25519(clientPriv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	var clientPubArr [KeySize]byte
	copy(clientPubArr[:], clientPubPub)
	claimedID := peerIDFromPublicKey(clientPubArr)

	if err := ch.verifyResponse(claimedID, resp[:]); err != nil {
		t.Fatalf("honest proof rejected: %v", err)
	}
}

func TestAuthRejectsWrongKeyForClaimedID(t *testing.T) {
	// Attacker holds its own key but claims a victim's ID.
	attackerPriv := testKey(t)
	victimPriv := testKey(t)
	victimPub, err := curve25519.X25519(victimPriv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	victimID := binary.BigEndian.Uint32(victimPub[KeySize-4:])

	challengePub, ch, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}
	resp, err := answerChallenge(challengePub, attackerPriv)
	if err != nil {
		t.Fatal(err)
	}

	if err := ch.verifyResponse(victimID, resp[:]); !errors.Is(err, ErrAuthBadResponse) {
		t.Fatalf("forged proof with wrong prefix must be rejected, got: %v", err)
	}
}

func TestAuthRejectsForgedDH(t *testing.T) {
	clientPriv := testKey(t)
	challengePub, ch, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}

	resp, err := answerChallenge(challengePub, clientPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientPubPub, _ := curve25519.X25519(clientPriv[:], curve25519.Basepoint)
	claimedID := binary.BigEndian.Uint32(clientPubPub[KeySize-4:])

	// Tamper with the DH half.
	resp[KeySize] ^= 0x01
	if err := ch.verifyResponse(claimedID, resp[:]); !errors.Is(err, ErrAuthBadResponse) {
		t.Fatalf("tampered DH must be rejected, got: %v", err)
	}
}

func TestAuthRejectsLowOrderPoint(t *testing.T) {
	clientPriv := testKey(t)
	challengePub, ch, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}
	_ = challengePub

	// All-zero DH result (simulates a low-order point input).
	var resp [AuthResponsePayload]byte
	clientPub, err := curve25519.X25519(clientPriv[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	copy(resp[:KeySize], clientPub)

	var clientPubArr [KeySize]byte
	copy(clientPubArr[:], clientPub)
	claimedID := peerIDFromPublicKey(clientPubArr)
	if err := ch.verifyResponse(claimedID, resp[:]); !errors.Is(err, ErrAuthBadResponse) {
		t.Fatalf("all-zero DH must be rejected, got: %v", err)
	}
}

func TestAuthRejectsWrongPayloadSize(t *testing.T) {
	_, ch, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.verifyResponse(1, make([]byte, 63)); !errors.Is(err, ErrAuthBadResponse) {
		t.Fatalf("short payload must be rejected, got: %v", err)
	}
}

func TestAuthChallengesAreUnique(t *testing.T) {
	a, _, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := newChallenge()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two challenges must never be identical (replay protection)")
	}
}

func TestPeerIDFromPublicKeyRoundTrip(t *testing.T) {
	pub := testKey(t)
	// PeerID = BigEndian first 8 bytes; LRP header carries the low 32 bits.
	peerID := binary.BigEndian.Uint64(pub[:8])
	if got := peerIDFromPublicKey(pub); got != uint32(peerID) {
		t.Fatalf("peerID mismatch: got %d want %d", got, uint32(peerID))
	}
}
