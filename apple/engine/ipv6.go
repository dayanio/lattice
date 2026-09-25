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

package engine

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/overlay6"
)

// v6Mode is what the engine does with IPv6 packets from the OS.
type v6Mode int32

const (
	// v6Off: no exit node is selected, or the control plane has IPv6 switched
	// off. The OS installs no IPv6 route into the tunnel, so no IPv6 packet
	// arrives; anything that does is handled as before (queued, and WireGuard
	// drops what no peer covers).
	v6Off v6Mode = iota
	// v6Tunnel: the exit node has a working IPv6 egress (its peers' AllowedIPs
	// include "::/0"). IPv6 packets go through WireGuard like IPv4 ones.
	v6Tunnel
	// v6Blackhole: an exit node is selected but it has no IPv6 egress. The OS
	// routes IPv6 into the tunnel so it cannot leak past the exit, and the
	// engine answers each packet with ICMPv6 "no route" so applications fall
	// back to IPv4 at once instead of waiting for a timeout.
	v6Blackhole
)

// v6ModeFor decides the mode from the routes the netmap gives this node.
// hasOverlay6 reports whether the netmap gave this node an overlay IPv6 address,
// which is the case only when the control plane has IPv6 turned on.
func v6ModeFor(included []string, hasOverlay6 bool) v6Mode {
	if !hasOverlay6 {
		return v6Off
	}
	var has4, has6 bool
	for _, r := range included {
		switch r {
		case "0.0.0.0/0":
			has4 = true
		case "::/0":
			has6 = true
		}
	}
	switch {
	case !has4:
		return v6Off
	case has6:
		return v6Tunnel
	default:
		return v6Blackhole
	}
}

const (
	// icmp6MaxPacket is the IPv6 minimum MTU, the largest packet an ICMPv6 error
	// may be (RFC 4443 §2.4).
	icmp6MaxPacket = 1280
	// icmp6RatePerSecond caps the ICMPv6 errors sent per second, so a port scan or
	// a runaway application cannot turn the engine into a packet generator.
	icmp6RatePerSecond = 100

	ipv6HeaderLen        = 40
	icmp6HeaderLen       = 8
	protoICMPv6          = 58
	icmp6TypeUnreachable = 1
	icmp6CodeNoRoute     = 0
)

// rateLimiter allows at most limit events per one-second window. The clock is
// injectable for tests.
type rateLimiter struct {
	mu          sync.Mutex
	limit       int
	now         func() time.Time
	windowStart time.Time
	count       int
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, now: time.Now}
}

func (r *rateLimiter) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.windowStart.IsZero() || now.Sub(r.windowStart) >= time.Second {
		r.windowStart = now
		r.count = 0
	}
	if r.count >= r.limit {
		return false
	}
	r.count++
	return true
}

// ipv6State is the packetTUN's IPv6 handling: the mode, this node's overlay
// address, and counters for the periodic telemetry line.
type ipv6State struct {
	mode    atomic.Int32
	limiter *rateLimiter
	sent    atomic.Uint64 // ICMPv6 errors synthesized
	limited atomic.Uint64 // blackholed packets that got no reply (rate limit or ineligible)
}

// SetIPv6 sets the IPv6 mode. It is called whenever the route set is
// re-evaluated, together with emitting the routes to the Swift side.
func (t *packetTUN) SetIPv6(mode v6Mode) {
	t.v6.mode.Store(int32(mode))
}

func (t *packetTUN) ipv6Mode() v6Mode { return v6Mode(t.v6.mode.Load()) }

// IPv6Stats returns the number of ICMPv6 errors synthesized and the number of
// blackholed packets that were dropped without a reply.
func (t *packetTUN) IPv6Stats() (sent, dropped uint64) {
	return t.v6.sent.Load(), t.v6.limited.Load()
}

// rejectIPv6 handles one outbound IPv6 packet in blackhole mode: it is never
// forwarded, and when eligible it is answered with ICMPv6 "no route to
// destination" straight back to the OS.
func (t *packetTUN) rejectIPv6(packet []byte) {
	reply := icmp6ReplyFor(packet)
	if reply == nil || !t.v6.limiter.allow() {
		t.v6.limited.Add(1)
		return
	}
	if err := t.writeFramed(reply); err == nil {
		t.v6.sent.Add(1)
	}
}

// icmp6ReplyFor builds the ICMPv6 Destination Unreachable (no route) answer for
// an outbound IPv6 packet, or nil when no answer should be sent. It answers
// only packets addressed to a routable unicast destination (not link-local,
// multicast, loopback or unspecified), never answers an ICMPv6 error (that could
// loop), and needs a usable unicast source to answer to.
func icmp6ReplyFor(packet []byte) []byte {
	if len(packet) < ipv6HeaderLen || packet[0]>>4 != 6 {
		return nil
	}
	src, ok1 := netip.AddrFromSlice(packet[8:24])
	dst, ok2 := netip.AddrFromSlice(packet[24:40])
	if !ok1 || !ok2 || !dst.IsGlobalUnicast() || !src.IsGlobalUnicast() {
		return nil
	}
	// Never answer an ICMPv6 error message with another one.
	if packet[6] == protoICMPv6 && len(packet) > ipv6HeaderLen && packet[ipv6HeaderLen] < 128 {
		return nil
	}
	return buildICMPv6Unreachable(overlay6.GatewayAddr(), src, packet)
}

// buildICMPv6Unreachable returns an IPv6 packet from gw to dst carrying an
// ICMPv6 Destination Unreachable (code 0, no route) that quotes as much of the
// invoking packet as fits in the IPv6 minimum MTU.
func buildICMPv6Unreachable(gw, dst netip.Addr, invoking []byte) []byte {
	quote := invoking
	if max := icmp6MaxPacket - ipv6HeaderLen - icmp6HeaderLen; len(quote) > max {
		quote = quote[:max]
	}
	msgLen := icmp6HeaderLen + len(quote)
	out := make([]byte, ipv6HeaderLen+msgLen)

	out[0] = 0x60 // version 6, traffic class and flow label zero
	binary.BigEndian.PutUint16(out[4:6], uint16(msgLen))
	out[6] = protoICMPv6
	out[7] = 64 // hop limit
	g, d := gw.As16(), dst.As16()
	copy(out[8:24], g[:])
	copy(out[24:40], d[:])

	msg := out[ipv6HeaderLen:]
	msg[0] = icmp6TypeUnreachable
	msg[1] = icmp6CodeNoRoute
	// msg[2:4] is the checksum, msg[4:8] is unused: both zero while summing.
	copy(msg[icmp6HeaderLen:], quote)
	binary.BigEndian.PutUint16(msg[2:4], icmp6Checksum(g, d, msg))
	return out
}

// icmp6Checksum is the ICMPv6 checksum: the one's complement of the one's
// complement sum over the IPv6 pseudo-header (source, destination, upper-layer
// length, next header = 58) and the message with its checksum field zeroed.
func icmp6Checksum(src, dst [16]byte, msg []byte) uint16 {
	var sum uint32
	add := func(b []byte) {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(b[i : i+2]))
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	add(src[:])
	add(dst[:])
	var tail [8]byte
	binary.BigEndian.PutUint32(tail[0:4], uint32(len(msg)))
	tail[7] = protoICMPv6
	add(tail[:])
	add(msg)
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
