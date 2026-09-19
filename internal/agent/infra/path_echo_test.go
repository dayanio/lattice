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
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestPathEchoCodec(t *testing.T) {
	pkt := encodePathEcho(pathEchoPing, 0xDEADBEEF12345678)
	if len(pkt) != pathEchoSize {
		t.Fatalf("packet is %d bytes, want %d", len(pkt), pathEchoSize)
	}
	kind, nonce, ok := decodePathEcho(pkt)
	if !ok || kind != pathEchoPing || nonce != 0xDEADBEEF12345678 {
		t.Fatalf("round trip = kind %d nonce %x ok %v", kind, nonce, ok)
	}
}

// Anything that is not an echo packet must be left for WireGuard or STUN.
func TestPathEchoCodecRejectsOtherTraffic(t *testing.T) {
	wireguard := append([]byte{1, 0, 0, 0}, make([]byte, 144)...) // handshake initiation
	transport := append([]byte{4, 0, 0, 0}, make([]byte, 60)...)
	stun := append([]byte{0x00, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42}, make([]byte, 12)...)
	good := encodePathEcho(pathEchoPing, 1)

	for name, pkt := range map[string][]byte{
		"wireguard handshake": wireguard,
		"wireguard data":      transport,
		"stun binding":        stun,
		"empty":               nil,
		"truncated":           good[:pathEchoSize-1],
		"too long":            append(append([]byte{}, good...), 0),
		"wrong magic":         append([]byte("XXXX"), good[4:]...),
		"unknown kind":        append(append(append([]byte{}, good[:4]...), 9), good[5:]...),
	} {
		if _, _, ok := decodePathEcho(pkt); ok {
			t.Errorf("%s decoded as an echo packet", name)
		}
	}
}

func TestPongBudgetIsBounded(t *testing.T) {
	var s pathEchoState
	now := time.Unix(1_000_000, 0)
	allowed := 0
	for i := 0; i < pongPerSecond*3; i++ {
		if s.allowPong(now) {
			allowed++
		}
	}
	if allowed != pongPerSecond {
		t.Fatalf("allowed %d pongs in one second, want %d", allowed, pongPerSecond)
	}
	if !s.allowPong(now.Add(1100 * time.Millisecond)) {
		t.Fatal("the budget must refill in the next window")
	}
}

func newEchoMux(t *testing.T) (*FilteringUDPMux, *net.UDPAddr, chan PassThroughPacket) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	m := NewFilteringUDPMux(conn, nil)
	pass := make(chan PassThroughPacket, 16)
	m.SetPassThrough(pass)
	m.Start()
	t.Cleanup(func() { _ = m.Close() })
	return m, conn.LocalAddr().(*net.UDPAddr), pass
}

func TestPing_ReachesAPeerMuxAndBack(t *testing.T) {
	a, _, _ := newEchoMux(t)
	_, bAddr, _ := newEchoMux(t)

	rtt, err := a.Ping(context.Background(), bAddr, time.Second)
	if err != nil {
		t.Fatalf("ping a live peer: %v", err)
	}
	if rtt <= 0 || rtt > time.Second {
		t.Fatalf("implausible rtt %v", rtt)
	}
}

func TestPing_TimesOutWhenNobodyAnswers(t *testing.T) {
	a, _, _ := newEchoMux(t)
	dead, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close() //nolint:errcheck // a bound socket that never replies

	start := time.Now()
	if _, err := a.Ping(context.Background(), dead.LocalAddr().(*net.UDPAddr), 150*time.Millisecond); err == nil {
		t.Fatal("ping to a silent address must fail")
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > time.Second {
		t.Fatalf("returned after %v, want about the 150ms timeout", d)
	}
}

func TestPing_HonoursContextCancellation(t *testing.T) {
	a, _, _ := newEchoMux(t)
	dead, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer dead.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := a.Ping(ctx, dead.LocalAddr().(*net.UDPAddr), time.Minute)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("ping ignored its context: err=%v after %v", err, time.Since(start))
	}
}

// The echo must never swallow real WireGuard traffic sharing the socket.
func TestEchoDoesNotSwallowWireGuardPackets(t *testing.T) {
	_, bAddr, bPass := newEchoMux(t)
	sender, err := net.DialUDP("udp4", nil, bAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close() //nolint:errcheck

	wg := append([]byte{4, 0, 0, 0}, bytes.Repeat([]byte{7}, 60)...)
	if _, err := sender.Write(wg); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bPass:
		if !bytes.Equal(got.Data, wg) {
			t.Fatalf("passed through %d bytes, want the original packet", len(got.Data))
		}
	case <-time.After(time.Second):
		t.Fatal("a WireGuard packet did not reach the pass-through channel")
	}
}

// A ping must be answered by the mux, not leaked to WireGuard as garbage.
func TestPingIsNotForwardedToWireGuard(t *testing.T) {
	a, _, _ := newEchoMux(t)
	_, bAddr, bPass := newEchoMux(t)

	if _, err := a.Ping(context.Background(), bAddr, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bPass:
		t.Fatalf("an echo packet leaked to WireGuard: %x", got.Data)
	case <-time.After(100 * time.Millisecond):
	}
}
