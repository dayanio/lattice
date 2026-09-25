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
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/miekg/dns"
)

// buildDNSQuery 构造一条手机发出的 DNS 查询 IP 包（IPv4+UDP+DNS）。
func buildDNSQuery(t *testing.T, qname, clientIP, serverIP string, clientPort uint16) []byte {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(qname), dns.TypeA)
	payload, err := m.Pack()
	if err != nil {
		t.Fatalf("pack query: %v", err)
	}
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp[0:2], clientPort)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(udp[8:], payload)
	t.Logf("buildDNSQuery: payload=%d udpHdr=% x clientPort=%d", len(payload), udp[0:4], clientPort)

	ip := make([]byte, 20)
	ip[0] = 0x45 // IPv4 + IHL 5
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(udp)))
	ip[8] = 64
	ip[9] = 17
	copy(ip[12:16], net.ParseIP(clientIP).To4())
	copy(ip[16:20], net.ParseIP(serverIP).To4())
	return append(ip, udp...)
}

// newTestTUN 建立带 egress 管道的 TUN；返回 TUN 和应答读取端。
func newTestTUN(t *testing.T) (*packetTUN, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	pt := newPacketTUN("lattice", 1280, int(w.Fd()))
	// Hermetic: pretend no tunnel interface exists, whatever the host has up.
	pt.ifaceIndex = func() int { return 0 }
	t.Cleanup(func() { r.Close(); w.Close(); _ = pt.Close() })
	return pt, r
}

// waitReply 从 egress 管道读取一帧应答，返回原始包与解析出的 DNS 消息。
func waitReply(t *testing.T, r *os.File) ([]byte, *dns.Msg) {
	t.Helper()
	type res struct {
		pkt []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		pkt, err := readFramed(r)
		ch <- res{pkt, err}
	}()
	select {
	case rs := <-ch:
		if rs.err != nil {
			t.Fatalf("read framed reply: %v", rs.err)
		}
		if len(rs.pkt) < 28 {
			t.Fatalf("response too short: %d", len(rs.pkt))
		}
		m := new(dns.Msg)
		if err := m.Unpack(rs.pkt[28:]); err != nil {
			t.Fatalf("unpack DNS response: %v", err)
		}
		return rs.pkt, m
	case <-time.After(5 * time.Second):
		t.Fatal("no DNS response was injected")
		return nil, nil
	}
}

func TestLatticeDNS_InterceptsLatticeQuery(t *testing.T) {
	pt, r := newTestTUN(t)
	pt.SetPeerSource(func() []*infra.Peer {
		addr := "10.96.0.2"
		return []*infra.Peer{{Name: "MacBook Pro", Address: &addr}}
	})
	pt.SetDNSResolver(func(qname string) (string, bool) {
		name := strings.TrimSuffix(strings.ToLower(qname), ".")
		if !strings.HasSuffix(name, ".lattice") {
			return "", false
		}
		for _, p := range pt.peerAddresses() {
			candidate := strings.ToLower(p.Name) + ".lattice"
			if candidate == name && p.Address != nil {
				return *p.Address, true
			}
		}
		return "", false
	})

	pkt := buildDNSQuery(t, "macbook-pro.lattice", "10.96.0.8", "10.96.0.1", 54321)
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	resp, m := waitReply(t, r)
	if m.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want NOERROR", m.Rcode)
	}
	if len(m.Answer) != 1 {
		t.Fatalf("want 1 answer, got %d", len(m.Answer))
	}
	if a, ok := m.Answer[0].(*dns.A); !ok || a.A.String() != "10.96.0.2" {
		t.Fatalf("answer = %+v, want A 10.96.0.2", m.Answer[0])
	}
	// 应答包的源地址应是查询的目的地址（隧道 DNS），目的地址是手机。
	if got := net.IP(resp[12:16]).String(); got != "10.96.0.1" {
		t.Fatalf("response src = %s, want 10.96.0.1", got)
	}
	if got := net.IP(resp[16:20]).String(); got != "10.96.0.8" {
		t.Fatalf("response dst = %s, want 10.96.0.8", got)
	}
}

func TestLatticeDNS_NonLatticeForwardedToUpstream(t *testing.T) {
	pt, r := newTestTUN(t)
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })
	// 上游指向 127.0.0.2（无监听，查询超时）：转发器应回 SERVFAIL 应答。
	pt.SetUpstreamDNS([]string{"127.0.0.2"})

	pkt := buildDNSQuery(t, "example.com", "10.96.0.8", "10.96.0.1", 54321)
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	_, m := waitReply(t, r)
	if m.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode = %d, want SERVFAIL", m.Rcode)
	}
	select {
	case <-pt.inbound:
		t.Fatal("forwarded upstream reply must not be re-injected into WireGuard")
	default:
	}
}

func TestLatticeDNS_AAAAEmptyAnswer(t *testing.T) {
	pt, r := newTestTUN(t)
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })

	// 构造 AAAA 查询（IPv4-only 出口：AAAA 一律空应答，客户端回退 A/IPv4）。
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn("example.com"), dns.TypeAAAA)
	payload, err := m.Pack()
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	udp := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint16(udp[0:2], 54321)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(udp[8:], payload)
	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(udp)))
	ip[8] = 64
	ip[9] = 17
	copy(ip[12:16], net.ParseIP("10.96.0.8").To4())
	copy(ip[16:20], net.ParseIP("10.96.0.1").To4())
	pkt := append(append([]byte{}, ip...), udp...)

	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	_, rm := waitReply(t, r)
	if rm.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %d, want NOERROR", rm.Rcode)
	}
	if len(rm.Answer) != 0 {
		t.Fatalf("AAAA answer must be empty, got %d records", len(rm.Answer))
	}
}

func TestPacketTUN_DropsPeerIPv6(t *testing.T) {
	pt, r := newTestTUN(t)

	// IPv6 包（版本号 6）从 peer 方向进入：出口模式下应被丢弃（v6 黑洞）。
	v6 := make([]byte, 40)
	v6[0] = 0x60
	if _, err := pt.Write([][]byte{v6}, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if d := pt.Dropped(); d != 1 {
		t.Fatalf("dropped = %d, want 1", d)
	}
	// egress 管道里不应有任何包（readFramed 会阻塞到超时）。
	go func() {
		time.Sleep(300 * time.Millisecond)
		r.Close()
	}()
	if _, err := readFramed(r); err == nil {
		t.Fatal("IPv6 packet must not be delivered to the system")
	}
}

func TestLatticeDNS_UnknownNameReturnsNXDOMAIN(t *testing.T) {
	pt, r := newTestTUN(t)
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })

	if err := pt.WriteInbound(buildDNSQuery(t, "ghost.lattice", "10.96.0.8", "10.96.0.1", 54321)); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	_, m := waitReply(t, r)
	if m.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d, want NXDOMAIN", m.Rcode)
	}
}

// The forwarder's own upstream queries are bound to the tunnel, so they come
// back through the TUN addressed to the real resolver. Only queries sent to the
// tunnel's DNS address (10.96.0.1) are ours to answer; anything else must travel
// on as ordinary traffic. Intercepting by port alone re-forwarded every
// upstream query forever and flooded the tunnel with thousands of packets a
// second, which killed the extension seconds after a global route came up.
func TestLatticeDNS_QueryToOtherResolverIsNotIntercepted(t *testing.T) {
	pt, r := newTestTUN(t)
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })
	pt.SetUpstreamDNS([]string{"127.0.0.2"})

	pkt := buildDNSQuery(t, "example.com", "10.96.0.8", "8.8.8.8", 54321)
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	select {
	case got := <-pt.inbound:
		if string(got) != string(pkt) {
			t.Fatal("packet to another resolver must reach WireGuard unchanged")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("packet to another resolver was swallowed instead of passed through")
	}

	// And nothing may have been answered locally or forwarded.
	if err := r.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if reply, err := readFramed(r); err == nil {
		t.Fatalf("unexpected local reply (%d bytes) to a query addressed to another resolver", len(reply))
	}
}
