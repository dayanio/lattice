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
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// relayChallenge is one in-flight authentication attempt: the relay's
// ephemeral X25519 secret, generated fresh per connection. The secret is
// never persisted and never leaves the process; replay is structurally
// impossible because the challenge never repeats.
type relayChallenge struct {
	// ephemeralSecret is the random X25519 scalar behind challengePub.
	ephemeralSecret [KeySize]byte
}

var (
	// ErrAuthBadResponse covers every rejected proof: wrong length, wrong
	// key prefix, forged DH output, or an all-zero (low-order point) result.
	ErrAuthBadResponse = errors.New("lrp: peer-auth response invalid")
)

// newChallenge generates a fresh ephemeral X25519 keypair for one
// challenge. The public half is sent to the client; the scalar stays local.
func newChallenge() (challengePub [KeySize]byte, ch *relayChallenge, err error) {
	ch = &relayChallenge{}
	if _, err = rand.Read(ch.ephemeralSecret[:]); err != nil {
		return challengePub, nil, fmt.Errorf("lrp: generate auth scalar: %w", err)
	}
	pub, err := curve25519.X25519(ch.ephemeralSecret[:], curve25519.Basepoint)
	if err != nil {
		return challengePub, nil, fmt.Errorf("lrp: derive auth public key: %w", err)
	}
	copy(challengePub[:], pub)
	return challengePub, ch, nil
}

// verifyResponse validates an AuthResponse payload
// (clientPublicKey || DH result) against the challenge secret and the
// claimed peer ID. claimedID is the uint32 carried in the LRP header,
// which by wire convention equals the low 32 bits of PeerID, i.e. the
// big-endian value of public key bytes 4..8 (PeerID is the big-endian
// first 8 bytes of the public key; the LRP header truncates it to its
// low 32 bits).
func (ch *relayChallenge) verifyResponse(claimedID uint32, payload []byte) error {
	if len(payload) != AuthResponsePayload {
		return fmt.Errorf("%w: payload %d bytes, want %d", ErrAuthBadResponse, len(payload), AuthResponsePayload)
	}

	var clientPub, clientDH [KeySize]byte
	copy(clientPub[:], payload[:KeySize])
	copy(clientDH[:], payload[KeySize:])

	// Self-certifying bind: the claimed ID must be derived from the very
	// public key the client is proving possession of.
	if peerIDFromPublicKey(clientPub) != claimedID {
		return fmt.Errorf("%w: public key does not match claimed peer id", ErrAuthBadResponse)
	}

	shared, err := curve25519.X25519(ch.ephemeralSecret[:], clientPub[:])
	if err != nil {
		return fmt.Errorf("%w: ecdh failed: %v", ErrAuthBadResponse, err)
	}
	// All-zero shared secret means the client sent a low-order point —
	// a proof of nothing.
	var zero [KeySize]byte
	if subtle.ConstantTimeCompare(shared, zero[:]) == 1 {
		return fmt.Errorf("%w: low-order point", ErrAuthBadResponse)
	}
	if subtle.ConstantTimeCompare(shared, clientDH[:]) != 1 {
		return fmt.Errorf("%w: dh mismatch", ErrAuthBadResponse)
	}
	return nil
}

// answerChallenge is the client side: given the relay's ephemeral public
// key and the client's WireGuard private key, produce the AuthResponse
// payload (clientPublicKey || DH result).
func answerChallenge(relayChallengePub [KeySize]byte, clientPrivate [KeySize]byte) ([AuthResponsePayload]byte, error) {
	var resp [AuthResponsePayload]byte

	clientPub, err := curve25519.X25519(clientPrivate[:], curve25519.Basepoint)
	if err != nil {
		return resp, fmt.Errorf("lrp: derive public key: %w", err)
	}
	shared, err := curve25519.X25519(clientPrivate[:], relayChallengePub[:])
	if err != nil {
		return resp, fmt.Errorf("lrp: ecdh failed: %w", err)
	}
	copy(resp[:KeySize], clientPub)
	copy(resp[KeySize:], shared)
	return resp, nil
}

// peerIDFromPublicKey returns the uint32 the LRP header carries for a peer
// with the given public key: the low 32 bits of PeerID (big-endian first 8
// bytes of the key), i.e. the big-endian value of key bytes 4..8.
func peerIDFromPublicKey(pub [KeySize]byte) uint32 {
	return binary.BigEndian.Uint32(pub[4:8])
}
