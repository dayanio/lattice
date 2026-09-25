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
	"os"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/overlay6"
)

// --- test helpers -----------------------------------------------------------

// ipv6Packet builds a minimal IPv6 packet: header plus payload.
func ipv6Packet(src, dst string, next byte, payload []byte) []byte {
	p := make([]byte, ipv6HeaderLen+len(payload))
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:6], uint16(len(payload)))
	p[6] = next
	p[7] = 64
	s, d := netip.MustParseAddr(src).As16(), netip.MustParseAddr(dst).As16()
	copy(p[8:24], s[:])
	copy(p[24:40], d[:])
	copy(p[40:], payload)
	return p
}

const (
	ovSrc = "fd6c:7270:6c74::a60:4" // this node's overlay IPv6
	gwAdr = "fd6c:7270:6c74::1"     // the synthetic ICMPv6 source
	inet6 = "2606:4700:4700::1111"  // a global destination
)

func tcpSyn() []byte { return make([]byte, 20) }

// verifyICMP6Checksum recomputes the checksum over pseudo-header + message
// INCLUDING the transmitted checksum; a valid packet sums to 0xffff.
func verifyICMP6Checksum(t *testing.T, pkt []byte) {
	t.Helper()
	msg := pkt[ipv6HeaderLen:]
	var src, dst [16]byte
	copy(src[:], pkt[8:24])
	copy(dst[:], pkt[24:40])
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
	if sum != 0xffff {
		t.Fatalf("ICMPv6 checksum invalid: folded sum = %#x, want 0xffff", sum)
	}
}

// newV6TUN returns a packetTUN whose egress is a pipe, plus the read end.
func newV6TUN(t *testing.T, mode v6Mode) (*packetTUN, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() }) //nolint:errcheck
	pt := newPacketTUN("lattice", 1280, int(w.Fd()))
	t.Cleanup(func() { pt.Close() }) //nolint:errcheck
	pt.SetIPv6(mode)
	return pt, r
}

// --- ICMPv6 construction -----------------------------------------------------

// The expected checksum was computed by an independent implementation (Python)
// over the same bytes, so it catches mistakes shared by the Go code and its own
// verifier above.
func TestBuildICMPv6Unreachable_KnownVector(t *testing.T) {
	invoking := ipv6Packet(ovSrc, inet6, 6, tcpSyn())
	// Make the payload deterministic: bytes 0..19, as in the reference vector.
	for i := 0; i < 20; i++ {
		invoking[ipv6HeaderLen+i] = byte(i)
	}
	got := buildICMPv6Unreachable(overlay6.GatewayAddr(), netip.MustParseAddr(ovSrc), invoking)

	if want := uint16(0xcef2); binary.BigEndian.Uint16(got[ipv6HeaderLen+2:ipv6HeaderLen+4]) != want {
		t.Fatalf("checksum = %#x, want %#x", binary.BigEndian.Uint16(got[ipv6HeaderLen+2:ipv6HeaderLen+4]), want)
	}
	verifyICMP6Checksum(t, got)
}

func TestBuildICMPv6Unreachable_Structure(t *testing.T) {
	invoking := ipv6Packet(ovSrc, inet6, 6, tcpSyn())
	got := buildICMPv6Unreachable(overlay6.GatewayAddr(), netip.MustParseAddr(ovSrc), invoking)

	if got[0]>>4 != 6 || got[6] != protoICMPv6 {
		t.Fatalf("not an IPv6/ICMPv6 packet: %x", got[:8])
	}
	if src, _ := netip.AddrFromSlice(got[8:24]); src.String() != gwAdr {
		t.Errorf("source = %s, want the overlay gateway %s", src, gwAdr)
	}
	if dst, _ := netip.AddrFromSlice(got[24:40]); dst.String() != ovSrc {
		t.Errorf("destination = %s, want the invoking packet's source %s", dst, ovSrc)
	}
	if got[ipv6HeaderLen] != 1 || got[ipv6HeaderLen+1] != 0 {
		t.Errorf("type/code = %d/%d, want 1/0 (destination unreachable, no route)", got[ipv6HeaderLen], got[ipv6HeaderLen+1])
	}
	if int(binary.BigEndian.Uint16(got[4:6])) != len(got)-ipv6HeaderLen {
		t.Errorf("payload length field = %d, packet carries %d", binary.BigEndian.Uint16(got[4:6]), len(got)-ipv6HeaderLen)
	}
	// The whole invoking packet is quoted after the 8-byte ICMPv6 header.
	if string(got[ipv6HeaderLen+icmp6HeaderLen:]) != string(invoking) {
		t.Error("the invoking packet must be quoted in full when it fits")
	}
	verifyICMP6Checksum(t, got)
}

func TestBuildICMPv6Unreachable_TruncatesToMinimumMTU(t *testing.T) {
	invoking := ipv6Packet(ovSrc, inet6, 17, make([]byte, 3000))
	got := buildICMPv6Unreachable(overlay6.GatewayAddr(), netip.MustParseAddr(ovSrc), invoking)
	if len(got) != icmp6MaxPacket {
		t.Fatalf("len = %d, want exactly %d (the IPv6 minimum MTU)", len(got), icmp6MaxPacket)
	}
	verifyICMP6Checksum(t, got)
}

func TestBuildICMPv6Unreachable_OddLengthChecksum(t *testing.T) {
	// A quoted packet of odd length exercises the trailing-byte padding.
	invoking := ipv6Packet(ovSrc, inet6, 6, make([]byte, 21))
	got := buildICMPv6Unreachable(overlay6.GatewayAddr(), netip.MustParseAddr(ovSrc), invoking)
	if len(got)%2 == 0 {
		t.Fatalf("test setup: expected an odd-length message, got %d", len(got))
	}
	verifyICMP6Checksum(t, got)
}

// --- who gets an answer ------------------------------------------------------

func TestICMP6ReplyFor_Eligibility(t *testing.T) {
	icmpErr := ipv6Packet(ovSrc, inet6, protoICMPv6, []byte{1, 0, 0, 0, 0, 0, 0, 0}) // an ICMPv6 error
	echo := ipv6Packet(ovSrc, inet6, protoICMPv6, []byte{128, 0, 0, 0, 0, 0, 0, 0})  // echo request (informational)

	cases := []struct {
		name   string
		packet []byte
		want   bool
	}{
		{"tcp to a global address", ipv6Packet(ovSrc, inet6, 6, tcpSyn()), true},
		{"udp to a global address", ipv6Packet(ovSrc, inet6, 17, make([]byte, 12)), true},
		{"ping to a global address", echo, true},
		{"to a ULA (a LAN address that cannot be reached through the tunnel)", ipv6Packet(ovSrc, "fd12:3456::1", 6, tcpSyn()), true},
		{"to link-local", ipv6Packet(ovSrc, "fe80::1", 6, tcpSyn()), false},
		{"to multicast", ipv6Packet(ovSrc, "ff02::1", 17, make([]byte, 12)), false},
		{"to loopback", ipv6Packet(ovSrc, "::1", 6, tcpSyn()), false},
		{"to the unspecified address", ipv6Packet(ovSrc, "::", 6, tcpSyn()), false},
		{"from the unspecified address", ipv6Packet("::", inet6, 17, make([]byte, 12)), false},
		{"from a link-local source", ipv6Packet("fe80::2", inet6, 6, tcpSyn()), false},
		{"an ICMPv6 error (never answer an error with an error)", icmpErr, false},
		{"truncated header", make([]byte, 20), false},
		{"an IPv4 packet", []byte{0x45, 0, 0, 20, 0, 0, 0, 0, 64, 6, 0, 0, 10, 96, 0, 4, 1, 1, 1, 1}, false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := icmp6ReplyFor(c.packet) != nil; got != c.want {
			t.Errorf("%s: reply=%v, want %v", c.name, got, c.want)
		}
	}
}

// --- the packet path ---------------------------------------------------------

func TestWriteInbound_BlackholeAnswersWithICMPv6AndNeverQueues(t *testing.T) {
	pt, r := newV6TUN(t, v6Blackhole)
	if err := pt.WriteInbound(ipv6Packet(ovSrc, inet6, 6, tcpSyn())); err != nil {
		t.Fatal(err)
	}
	reply, err := readFramed(r)
	if err != nil {
		t.Fatalf("expected an ICMPv6 reply on the egress: %v", err)
	}
	if reply[6] != protoICMPv6 || reply[ipv6HeaderLen] != 1 {
		t.Fatalf("reply is not an ICMPv6 unreachable: %x", reply[:48])
	}
	verifyICMP6Checksum(t, reply)
	select {
	case p := <-pt.inbound:
		t.Fatalf("a blackholed packet must not reach WireGuard, got %x", p)
	default:
	}
	if sent, _ := pt.IPv6Stats(); sent != 1 {
		t.Errorf("sent counter = %d, want 1", sent)
	}
}

func TestWriteInbound_TunnelModeForwardsIPv6ToWireGuard(t *testing.T) {
	pt, _ := newV6TUN(t, v6Tunnel)
	pkt := ipv6Packet(ovSrc, inet6, 6, tcpSyn())
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-pt.inbound:
		if string(got) != string(pkt) {
			t.Fatal("the packet must reach WireGuard unchanged")
		}
	default:
		t.Fatal("tunnel mode must queue the IPv6 packet for WireGuard")
	}
}

func TestWriteInbound_OffModeBehavesAsBefore(t *testing.T) {
	pt, r := newV6TUN(t, v6Off)
	pkt := ipv6Packet(ovSrc, inet6, 6, tcpSyn())
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pt.inbound: // queued for WireGuard, exactly as before IPv6 support
	default:
		t.Fatal("off mode must not intercept IPv6")
	}
	_ = r
}

func TestWriteInbound_IPv4IsUntouchedInEveryMode(t *testing.T) {
	for _, mode := range []v6Mode{v6Off, v6Tunnel, v6Blackhole} {
		pt, _ := newV6TUN(t, mode)
		v4 := []byte{0x45, 0, 0, 20, 0, 0, 0, 0, 64, 6, 0, 0, 10, 96, 0, 4, 1, 1, 1, 1}
		if err := pt.WriteInbound(v4); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-pt.inbound:
			if string(got) != string(v4) {
				t.Errorf("mode %d: IPv4 packet altered", mode)
			}
		default:
			t.Errorf("mode %d: IPv4 packet was not queued", mode)
		}
	}
}

func TestWriteInbound_BlackholeIneligiblePacketsAreDroppedSilently(t *testing.T) {
	pt, r := newV6TUN(t, v6Blackhole)
	for _, dst := range []string{"fe80::1", "ff02::1"} {
		if err := pt.WriteInbound(ipv6Packet(ovSrc, dst, 17, make([]byte, 12))); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-pt.inbound:
		t.Fatal("blackholed packets must never be queued, eligible or not")
	default:
	}
	_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := readFramed(r); err == nil {
		t.Fatal("no reply may be sent for link-local or multicast destinations")
	}
	if sent, dropped := pt.IPv6Stats(); sent != 0 || dropped != 2 {
		t.Errorf("stats = sent %d, dropped %d; want 0 and 2", sent, dropped)
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newRateLimiter(3)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.allow() {
			t.Fatalf("event %d within the limit was refused", i)
		}
	}
	if l.allow() {
		t.Fatal("the fourth event in the same second must be refused")
	}
	now = now.Add(500 * time.Millisecond)
	if l.allow() {
		t.Fatal("still inside the window: must be refused")
	}
	now = now.Add(600 * time.Millisecond)
	if !l.allow() {
		t.Fatal("a new window must start allowing again")
	}
}

func TestWriteInbound_BlackholeIsRateLimited(t *testing.T) {
	pt, r := newV6TUN(t, v6Blackhole)
	frozen := time.Unix(2000, 0)
	pt.v6.limiter = newRateLimiter(2)
	pt.v6.limiter.now = func() time.Time { return frozen }

	for i := 0; i < 5; i++ {
		if err := pt.WriteInbound(ipv6Packet(ovSrc, inet6, 6, tcpSyn())); err != nil {
			t.Fatal(err)
		}
	}
	sent, dropped := pt.IPv6Stats()
	if sent != 2 || dropped != 3 {
		t.Fatalf("stats = sent %d, dropped %d; want 2 and 3", sent, dropped)
	}
	select {
	case <-pt.inbound:
		t.Fatal("rate-limited packets must still not be forwarded")
	default:
	}
	_ = r
}

// --- WG → OS direction -------------------------------------------------------

func TestWrite_IPv6DeliveryDependsOnMode(t *testing.T) {
	back := ipv6Packet(inet6, ovSrc, 6, tcpSyn())          // return traffic to us
	stray := ipv6Packet(inet6, "2001:db8::1", 6, tcpSyn()) // not addressed into the overlay
	cases := []struct {
		mode    v6Mode
		pkt     []byte
		deliver bool
	}{
		{v6Tunnel, back, true},
		{v6Tunnel, stray, false}, // isLocalDst: outside the overlay prefix is a loop artifact
		{v6Blackhole, back, false},
		{v6Off, back, false},
	}
	for _, c := range cases {
		pt, r := newV6TUN(t, c.mode)
		buf := append(make([]byte, 16), c.pkt...) // WireGuard hands buffers with an offset
		if _, err := pt.Write([][]byte{buf}, 16); err != nil {
			t.Fatal(err)
		}
		_ = r.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, err := readFramed(r)
		if (err == nil) != c.deliver {
			t.Errorf("mode %d, dst in overlay=%v: delivered=%v, want %v", c.mode, string(c.pkt) == string(back), err == nil, c.deliver)
		}
	}
}

func TestIsLocalDst_IPv6(t *testing.T) {
	pt := newPacketTUN("lattice", 1280, 0)
	defer pt.Close() //nolint:errcheck
	if !pt.isLocalDst(ipv6Packet(inet6, ovSrc, 6, tcpSyn())) {
		t.Error("a destination inside the overlay prefix is local")
	}
	if pt.isLocalDst(ipv6Packet(inet6, "2001:db8::1", 6, tcpSyn())) {
		t.Error("a destination outside the overlay prefix is not local")
	}
	if pt.isLocalDst(ipv6Packet(inet6, "fe80::1", 6, tcpSyn())) {
		t.Error("link-local is not local overlay traffic")
	}
	// A truncated IPv6 header must not be read as IPv4 (it would index pkt[16:20]).
	_ = pt.isLocalDst(append([]byte{0x60}, make([]byte, 30)...))
}

// --- mode selection ----------------------------------------------------------

func TestV6ModeFor(t *testing.T) {
	cases := []struct {
		name        string
		included    []string
		hasOverlay6 bool
		want        v6Mode
	}{
		{"exit with an IPv6 egress", []string{"0.0.0.0/0", "::/0"}, true, v6Tunnel},
		{"exit without an IPv6 egress", []string{"0.0.0.0/0"}, true, v6Blackhole},
		{"no exit selected", nil, true, v6Off},
		{"only a subnet route", []string{"192.168.1.0/24"}, true, v6Off},
		{"::/0 without an IPv4 default is not an exit", []string{"::/0"}, true, v6Off},
		{"control plane has IPv6 off (no overlay address)", []string{"0.0.0.0/0"}, false, v6Off},
		{"control plane has IPv6 off, even with ::/0", []string{"0.0.0.0/0", "::/0"}, false, v6Off},
	}
	for _, c := range cases {
		if got := v6ModeFor(c.included, c.hasOverlay6); got != c.want {
			t.Errorf("%s: v6ModeFor = %d, want %d", c.name, got, c.want)
		}
	}
}
