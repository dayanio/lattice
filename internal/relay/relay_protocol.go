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

// The Ferry (摆渡) relay protocol — Lattice's designated relay for
// encrypted WireGuard packets, the counterpart of Tailscale's DERP.

package relay

import (
	"encoding/binary"
	"errors"
)

const HeaderSize = 12

// MaxRegisterPayload bounds the Register frame payload (the shared auth
// token). Anything larger is a protocol violation — the connection is
// rejected before any sized allocation happens, so a crafted header cannot
// force the relay into a huge allocation.
const MaxRegisterPayload = 512

// MaxForwardPayload bounds Forward/Probe payload sizes on the relay server
// read path. WireGuard packets are at most 65535 bytes; anything claiming to
// be larger is a protocol violation.
const MaxForwardPayload = 64 << 10

// Commands
const (
	Register  uint8 = 0x01
	Forward   uint8 = 0x02
	KeepAlive uint8 = 0x03
	Probe     uint8 = 0x04

	// Per-peer authentication handshake (ADR-0004). The relay challenges
	// with an ephemeral X25519 public key; the client answers with
	// DH(client private key, relay ephemeral public key) prefixed by its
	// own public key. A session becomes usable only after the proof
	// verifies (or immediately, in lenient mode, for legacy clients).
	AuthChallenge uint8 = 0x05
	AuthResponse  uint8 = 0x06
)

// KeySize is the WireGuard / X25519 key length used by the auth handshake.
const KeySize = 32

// AuthResponsePayload is the exact AuthResponse payload size:
// client public key (32 B) || DH result (32 B).
const AuthResponsePayload = 2 * KeySize

// Header is the 12-byte Relay frame header (little-endian).
// Offset 0-1:   Seq        — frame sequence number
// Offset 2-5:   PayloadLen — payload size in bytes
// Offset 6:     Cmd        — command byte
// Offset 7-10:  ToID       — target peer ID (uint32)
// Offset 11:    Reserved   — must be 0
type Header struct {
	Seq        uint16
	PayloadLen uint32
	Cmd        uint8
	ToID       uint32
	Reserved   uint8
}

func (h *Header) Marshal() []byte {
	buf := make([]byte, HeaderSize)
	binary.LittleEndian.PutUint16(buf[0:2], h.Seq)
	binary.LittleEndian.PutUint32(buf[2:6], h.PayloadLen)
	buf[6] = h.Cmd
	binary.LittleEndian.PutUint32(buf[7:11], h.ToID)
	buf[11] = h.Reserved
	return buf
}

// MarshalInto writes the header into an existing buffer (must be >= HeaderSize).
func (h *Header) MarshalInto(buf []byte) {
	binary.LittleEndian.PutUint16(buf[0:2], h.Seq)
	binary.LittleEndian.PutUint32(buf[2:6], h.PayloadLen)
	buf[6] = h.Cmd
	binary.LittleEndian.PutUint32(buf[7:11], h.ToID)
	buf[11] = h.Reserved
}

func Unmarshal(data []byte) (*Header, error) {
	if len(data) < HeaderSize {
		return nil, errors.New("relay: header too short")
	}
	h := &Header{}
	h.Seq = binary.LittleEndian.Uint16(data[0:2])
	h.PayloadLen = binary.LittleEndian.Uint32(data[2:6])
	h.Cmd = data[6]
	h.ToID = binary.LittleEndian.Uint32(data[7:11])
	h.Reserved = data[11]
	return h, nil
}

// stampSender rewrites the ToID field of a frame about to be relayed to the
// SENDER's ID. Clients register with ToID = their own ID and address frames
// with ToID = the target; a relayed Forward frame carries nothing else, so the
// receiver (and WireGuard's roaming endpoint) identifies the peer from this
// field. Without the rewrite it holds the receiver's own ID.
func stampSender(frame []byte, fromID uint64) {
	binary.LittleEndian.PutUint32(frame[7:11], uint32(fromID))
}
