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

package infra

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"
)

// Path echo: a 13-byte request/response exchanged over the shared UDP socket
// (and answered by the mux itself, never reaching WireGuard) that tells whether
// a direct path works in BOTH directions. WireGuard cannot: it stays silent
// when idle, and keepalives on both sides postpone each other.
const (
	pathEchoMagic = "LTPE"
	pathEchoPing  = byte(1)
	pathEchoPong  = byte(2)
	pathEchoSize  = len(pathEchoMagic) + 1 + 8
	pongPerSecond = 100
)

// ErrPathEchoLost means no echo reply arrived in time.
var ErrPathEchoLost = errors.New("path echo: no reply")

// encodePathEcho builds an echo packet.
func encodePathEcho(kind byte, nonce uint64) []byte {
	pkt := make([]byte, pathEchoSize)
	copy(pkt, pathEchoMagic)
	pkt[len(pathEchoMagic)] = kind
	binary.BigEndian.PutUint64(pkt[len(pathEchoMagic)+1:], nonce)
	return pkt
}

// decodePathEcho parses an echo packet; ok is false for anything else,
// including WireGuard and STUN packets. The packet is exactly 13 bytes and
// starts with 'L', which is neither a WireGuard message type (1 to 4) nor a
// STUN header (top two bits zero, magic cookie at offset 4).
func decodePathEcho(pkt []byte) (kind byte, nonce uint64, ok bool) {
	if len(pkt) != pathEchoSize || string(pkt[:len(pathEchoMagic)]) != pathEchoMagic {
		return 0, 0, false
	}
	kind = pkt[len(pathEchoMagic)]
	if kind != pathEchoPing && kind != pathEchoPong {
		return 0, 0, false
	}
	return kind, binary.BigEndian.Uint64(pkt[len(pathEchoMagic)+1:]), true
}

// pathEchoState tracks in-flight pings and bounds pong replies.
type pathEchoState struct {
	mu      sync.Mutex
	pending map[uint64]chan struct{}
	window  time.Time
	sent    int
}

// allowPong reports whether another pong may be sent now: a fixed one-second
// window with a budget, so an echo responder cannot be used as a reflector.
func (s *pathEchoState) allowPong(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Sub(s.window) >= time.Second {
		s.window, s.sent = now, 0
	}
	if s.sent >= pongPerSecond {
		return false
	}
	s.sent++
	return true
}

// handlePathEcho consumes echo packets, answering pings and completing pending
// pings. It reports whether the packet was an echo packet.
func (f *FilteringUDPMux) handlePathEcho(pkt []byte, from net.Addr) bool {
	kind, nonce, ok := decodePathEcho(pkt)
	if !ok {
		return false
	}
	switch kind {
	case pathEchoPing:
		if f.echo.allowPong(time.Now()) {
			_, _ = f.realConn.WriteTo(encodePathEcho(pathEchoPong, nonce), from)
		}
	case pathEchoPong:
		f.echo.mu.Lock()
		if ch, waiting := f.echo.pending[nonce]; waiting {
			delete(f.echo.pending, nonce)
			close(ch)
		}
		f.echo.mu.Unlock()
	}
	return true
}

// Ping sends an echo request to addr over the shared socket and waits for the
// reply, returning the round-trip time. It fails when no reply arrives within
// timeout or ctx ends. A reply proves the path works both ways, which is
// what a WireGuard endpoint alone cannot show.
func (f *FilteringUDPMux) Ping(ctx context.Context, addr *net.UDPAddr, timeout time.Duration) (time.Duration, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	nonce := binary.BigEndian.Uint64(b[:])
	done := make(chan struct{})

	f.echo.mu.Lock()
	if f.echo.pending == nil {
		f.echo.pending = make(map[uint64]chan struct{})
	}
	f.echo.pending[nonce] = done
	f.echo.mu.Unlock()
	defer func() {
		f.echo.mu.Lock()
		delete(f.echo.pending, nonce)
		f.echo.mu.Unlock()
	}()

	start := time.Now()
	if _, err := f.realConn.WriteTo(encodePathEcho(pathEchoPing, nonce), addr); err != nil {
		return 0, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return time.Since(start), nil
	case <-timer.C:
		return 0, ErrPathEchoLost
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-f.stopCh:
		return 0, net.ErrClosed
	}
}
