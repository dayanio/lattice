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

// popDNS 等待拦截器回送的应答包并解析出 DNS 消息。
func popDNS(t *testing.T, pt *packetTUN) *dns.Msg {
	t.Helper()
	pt.PopOutbound() // 等待应答包
	// PopOutbound 阻塞取包；取到后从 outbound 再取会阻塞——所以直接从
	// outbound 队列取包的方式不可行，改为在上面先截获。这里用非阻塞方式。
	select {
	case pkt := <-pt.outbound:
		if len(pkt) < 28 {
			t.Fatalf("response packet too short: %d", len(pkt))
		}
		m := new(dns.Msg)
		if err := m.Unpack(pkt[20+8:]); err != nil {
			t.Fatalf("unpack DNS response: %v", err)
		}
		return m
	default:
		t.Fatal("no DNS response was injected")
		return nil
	}
}

func TestLatticeDNS_InterceptsLatticeQuery(t *testing.T) {
	pt := newPacketTUN("lattice", 1280)
	defer pt.Close() //nolint:errcheck
	pt.SetPeerSource(func() []*infra.Peer {
		addr := "10.96.0.2"
		return []*infra.Peer{{Name: "MacBook Pro", Address: &addr}}
	})
	pt.SetDNSResolver(nil)
	// resolver 本体在 interceptLatticeDNS 内部使用 peerAddresses —— 直接由
	// SetPeerSource 提供；SetDNSResolver 的开关由 engine 接线（此处默认 nil
	// 会跳过拦截，所以必须显式开启）。
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
	t.Logf("built packet len=%d udpHeader=% x", len(pkt), pkt[20:24])
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	t.Logf("after write: inbound-len=%d outbound-len=%d", len(pt.inbound), len(pt.outbound))

	select {
	case resp := <-pt.outbound:
		if len(resp) < 28 {
			t.Fatalf("response too short: %d", len(resp))
		}
		m := new(dns.Msg)
		if err := m.Unpack(resp[20+8:]); err != nil {
			t.Fatalf("unpack: %v", err)
		}
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
	case <-time.After(time.Second):
		t.Fatal("no DNS response injected")
	}
}

func TestLatticeDNS_NonLatticeForwardedToUpstream(t *testing.T) {
	pt := newPacketTUN("lattice", 1280)
	defer pt.Close() //nolint:errcheck
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })
	// 上游指向 127.0.0.2（无监听，查询超时）：转发器应回 SERVFAIL 应答。
	pt.SetUpstreamDNS([]string{"127.0.0.2"})

	pkt := buildDNSQuery(t, "example.com", "10.96.0.8", "10.96.0.1", 54321)
	if err := pt.WriteInbound(pkt); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	select {
	case resp := <-pt.outbound:
		m := new(dns.Msg)
		if err := m.Unpack(resp[28:]); err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if m.Rcode != dns.RcodeServerFailure {
			t.Fatalf("rcode = %d, want SERVFAIL", m.Rcode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no SERVFAIL reply from the upstream forwarder")
	}
	select {
	case <-pt.inbound:
		t.Fatal("forwarded upstream reply must not be re-injected into WireGuard")
	default:
	}
}

func TestLatticeDNS_AAAAEmptyAnswer(t *testing.T) {
	pt := newPacketTUN("lattice", 1280)
	defer pt.Close() //nolint:errcheck
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

	select {
	case resp := <-pt.outbound:
		rm := new(dns.Msg)
		if err := rm.Unpack(resp[28:]); err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if rm.Rcode != dns.RcodeSuccess {
			t.Fatalf("rcode = %d, want NOERROR", rm.Rcode)
		}
		if len(rm.Answer) != 0 {
			t.Fatalf("AAAA answer must be empty, got %d records", len(rm.Answer))
		}
	case <-time.After(time.Second):
		t.Fatal("no AAAA reply")
	}
}

func TestPacketTUN_DropsPeerIPv6(t *testing.T) {
	pt := newPacketTUN("lattice", 1280)
	defer pt.Close() //nolint:errcheck

	// IPv6 包（版本号 6）从 peer 方向进入：出口模式下应被丢弃（v6 黑洞）。
	v6 := make([]byte, 40)
	v6[0] = 0x60
	if _, err := pt.Write([][]byte{v6}, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if d := pt.Dropped(); d != 1 {
		t.Fatalf("dropped = %d, want 1", d)
	}
	select {
	case <-pt.outbound:
		t.Fatal("IPv6 packet must not be delivered to the system")
	default:
	}
}

func TestLatticeDNS_UnknownNameReturnsNXDOMAIN(t *testing.T) {
	pt := newPacketTUN("lattice", 1280)
	defer pt.Close() //nolint:errcheck
	pt.SetPeerSource(func() []*infra.Peer { return nil })
	pt.SetDNSResolver(func(string) (string, bool) { return "", false })

	if err := pt.WriteInbound(buildDNSQuery(t, "ghost.lattice", "10.96.0.8", "10.96.0.1", 54321)); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	select {
	case resp := <-pt.outbound:
		m := new(dns.Msg)
		if err := m.Unpack(resp[28:]); err != nil {
			t.Fatalf("unpack: %v", err)
		}
		if m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %d, want NXDOMAIN", m.Rcode)
		}
	case <-time.After(time.Second):
		t.Fatal("no NXDOMAIN response")
	}
}
