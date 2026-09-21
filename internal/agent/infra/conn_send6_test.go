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
	"net"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// TestSend6AfterSend4PoolPoison is the regression test for the IPv6 send
// path: send4 re-slices the pooled UDPAddr's IP to 4 bytes before putting
// it back, and send6 used to copy into that truncated slice without
// restoring the length or setting the port — silently corrupting the v6
// destination and port. After a send4-style pool poisoning, a v6 packet
// must still arrive at the endpoint's exact address and port.
func TestSend6AfterSend4PoolPoison(t *testing.T) {
	receiver, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("udp6 loopback unavailable: %v", err)
	}
	defer receiver.Close() //nolint:errcheck

	sender, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("udp6 loopback unavailable: %v", err)
	}
	defer sender.Close() //nolint:errcheck

	b := NewBind(&BindConfig{})

	// Poison the pool exactly the way send4 does: truncate IP to 4 bytes
	// (cap stays 16) and put it back.
	ua := b.udpAddrPool.Get().(*net.UDPAddr)
	ua.IP = ua.IP[:4]
	b.udpAddrPool.Put(ua)

	ep := &RelayEndpoint{
		Addr:          receiver.LocalAddr().(*net.UDPAddr).AddrPort(),
		TransportType: ICE,
	}
	if err := b.send6(sender, ipv6.NewPacketConn(sender), ep, [][]byte{[]byte("wg-packet")}); err != nil {
		t.Fatalf("send6: %v", err)
	}

	_ = receiver.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 64)
	n, _, err := receiver.ReadFromUDP(got)
	if err != nil {
		t.Fatalf("no packet received on v6 endpoint (send6 corrupted addr/port?): %v", err)
	}
	if string(got[:n]) != "wg-packet" {
		t.Fatalf("payload mismatch: %q", string(got[:n]))
	}
}

// TestSend4ThenSend6 exercises the real v4→v6 pool cycling end to end.
func TestSend4ThenSend6(t *testing.T) {
	rx4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Skipf("udp4 loopback unavailable: %v", err)
	}
	defer rx4.Close() //nolint:errcheck

	rx6, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("udp6 loopback unavailable: %v", err)
	}
	defer rx6.Close() //nolint:errcheck

	tx4, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Skipf("udp4 loopback unavailable: %v", err)
	}
	defer tx4.Close() //nolint:errcheck

	tx6, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
	if err != nil {
		t.Skipf("udp6 loopback unavailable: %v", err)
	}
	defer tx6.Close() //nolint:errcheck

	b := NewBind(&BindConfig{})

	// v4 first (leaves the pooled addr with a 4-byte IP + the v4 port)...
	ep4 := &RelayEndpoint{Addr: rx4.LocalAddr().(*net.UDPAddr).AddrPort(), TransportType: ICE}
	if err := b.send4(tx4, ipv4.NewPacketConn(tx4), ep4, [][]byte{[]byte("v4")}); err != nil {
		t.Fatalf("send4: %v", err)
	}
	// ...then v6 must still land on the right address/port.
	ep6 := &RelayEndpoint{Addr: rx6.LocalAddr().(*net.UDPAddr).AddrPort(), TransportType: ICE}
	if err := b.send6(tx6, ipv6.NewPacketConn(tx6), ep6, [][]byte{[]byte("v6")}); err != nil {
		t.Fatalf("send6: %v", err)
	}

	_ = rx6.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, 64)
	n, _, err := rx6.ReadFromUDP(got)
	if err != nil {
		t.Fatalf("no v6 packet after v4 pool reuse: %v", err)
	}
	if string(got[:n]) != "v6" {
		t.Fatalf("v6 payload mismatch: %q", string(got[:n]))
	}
}
