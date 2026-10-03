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
	"net"
	"sync"
	"sync/atomic"
)

// Stream abstract the exact transport protocol
type Stream interface {
	Read(p []byte) (n int, err error)
	Write(p []byte) (n int, err error)
	Close() error
	RemoteAddr() net.Addr
}

type Session struct {
	ID     uint64
	Stream Stream
	Type   string // TCP / QUIC / KCP

	// verified is set once the session completed the X25519 per-peer auth
	// handshake (ADR-0004). Guarded by SessionManager.mu.
	verified bool

	// ── Per-session forward queue (ADR-0005 F4) ──────────────────────────
	//
	// Relayed frames are handed to sendCh and written by the session's sole
	// writer goroutine (session_writer.go). A slow destination therefore
	// stalls only its own queue — senders to other peers are never blocked
	// behind it, and a full queue drops the new frame instead of blocking
	// the relaying peer's read loop (WireGuard retransmits; the relay path
	// is best-effort by design).
	//
	// The writer starts lazily on the first enqueued frame, so sessions
	// built as struct literals (tests) behave the same as newTCPSession.
	sendCh    chan []byte
	done      chan struct{}
	closeOnce sync.Once
	workOnce  sync.Once

	// dropped counts frames discarded because the destination's queue was
	// full (or the session was already shut down).
	dropped atomic.Uint64
}
