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
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/miekg/dns"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

// packetTUN implements wireguard-go's tun.Device on top of packet
// hand-off to and from the Apple Network Extension (NEPacketFlow).
//
// Wire direction (relative to the WG device):
//
//	Read  ← inbound  ← Swift: IP packets the system sent into the tunnel
//	Write → outbound → Swift: decrypted packets to hand back to the system
type packetTUN struct {
	name    string
	mtu     int
	inbound chan []byte // Swift → WG (to be encrypted): fed by SendPacketBatch
	fd      *os.File    // WG → Swift (decrypted): 4-byte little-endian length
	// framed writes; the Swift side batch-reads and writePackets — the
	// wireguard-apple socketpair pattern (no per-packet bridge crossings).
	events   chan tun.Event
	closedCh chan struct{}
	mu       sync.Mutex
	once     sync.Once

	// dropped counts packets discarded because a queue was full.
	dropped uint64
	// fdErr remembers the first egress write failure for diagnostics.
	fdErr error

	// Custom name answers for server-pushed records — not used yet; the
	// interceptor gates on peerSource (see WriteInbound).
	dnsResolver func(qname string) (string, bool)
	// Upstream resolvers for non-lattice queries (exit-node DNS takeover):
	// system resolvers, parsed once from /etc/resolv.conf. Empty until the
	// first lookup succeeds; a built-in CN fallback list is used then.
	upstreamDNS []string
	// peerSource 提供当前组网设备表（名字 → overlay 地址）。
	peerSource func() []*infra.Peer
	// ifaceIndex finds the tunnel interface the upstream DNS sockets bind to
	// (0 = none, i.e. not in exit mode). It is a field so tests can pin it
	// instead of depending on whether the host happens to have an overlay
	// interface up (a developer machine with Lattice connected does).
	ifaceIndex func() int
	// localIP 本节点的 overlay 地址；Write 方向只投递 dst=本机 的包。
	// 目的地址非本机的包（内核 ICMP 错误风暴/环路包）若照常投递会经
	// 路由表再进隧道，形成自放大循环直到管道被挤死。
	localIP atomic.Value // net.IP
}

// SetDNSResolver wires the *.lattice name resolver (LatticeDNS).
func (t *packetTUN) SetDNSResolver(r func(qname string) (string, bool)) {
	t.dnsResolver = r
}

// SetPeerSource wires the current mesh peer table used for name resolution.
func (t *packetTUN) SetPeerSource(src func() []*infra.Peer) {
	t.peerSource = src
}

func (t *packetTUN) peerAddresses() []*infra.Peer {
	if t.peerSource == nil {
		return nil
	}
	return t.peerSource()
}

// SetUpstreamDNS overrides the resolvers used for non-lattice queries
// (exit-node DNS takeover, design doc §四). When unset, the host's
// resolvers from /etc/resolv.conf are used with a CN public fallback.
func (t *packetTUN) SetUpstreamDNS(servers []string) {
	t.upstreamDNS = servers
}

// SetLocalIP records this node's overlay address for the egress filter.
func (t *packetTUN) SetLocalIP(ip net.IP) {
	t.localIP.Store(ip)
}

// isLocalDst reports whether the decrypted packet is addressed into the
// mesh overlay (this node or any mesh peer). Packets addressed OUTSIDE the
// overlay arriving on this path are loop artifacts (kernel ICMP error storms)
// and must be dropped — delivering them feeds the self-amplifying loop.
func (t *packetTUN) isLocalDst(pkt []byte) bool {
	ip, _ := t.localIP.Load().(net.IP)
	if ip == nil || len(pkt) < 20 {
		return true // unknown local IP: fail open (deliver)
	}
	dst := net.IP(pkt[16:20]).To4()
	if dst == nil {
		return true
	}
	return dst[0] == 10 && dst[1] == 96
}

// dnsServers lazily resolves the upstream list once; the result is cached.
func (t *packetTUN) dnsServers() []string {
	if t.upstreamDNS != nil {
		return t.upstreamDNS
	}
	var out []string
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "nameserver ") {
				if ns := strings.TrimSpace(strings.TrimPrefix(line, "nameserver ")); ns != "" {
					out = append(out, ns)
				}
			}
		}
	}
	if len(out) == 0 {
		out = []string{"223.5.5.5", "119.29.29.29"}
	}
	t.upstreamDNS = out
	return out
}

func newPacketTUN(name string, mtu int, egressFD int) *packetTUN {
	if mtu <= 0 {
		mtu = 1280
	}
	t := &packetTUN{
		name:     name,
		mtu:      mtu,
		inbound:  make(chan []byte, 512),
		events:   make(chan tun.Event, 4),
		closedCh: make(chan struct{}),

		ifaceIndex: tunnelIfaceIndex,
	}
	if egressFD > 0 {
		f := os.NewFile(uintptr(egressFD), "ne-tun-egress")
		// Non-blocking: when the Swift reader stalls, writes must DROP (wireguard
		// semantics), never block the WG routines — a blocking write here froze
		// the whole engine when the NE completion handler didn't fire.
		if f != nil {
			if rc, ferr := f.SyscallConn(); ferr == nil {
				_ = rc.Control(func(fd uintptr) {
					_ = unix.SetNonblock(int(fd), true) //nolint:errcheck
				})
			}
		}
		t.fd = f
	}
	t.events <- tun.EventUp
	return t
}

// writeFramed hands one decrypted packet to the Swift side as ONE datagram
// on a SOCK_DGRAM socketpair (message-preserving: no framing, no partial
// writes — a stream socketpair desynced when a non-blocking write dropped
// half a frame and Swift blocked forever on a bogus length).
func (t *packetTUN) writeFramed(pkt []byte) error {
	if t.fd == nil {
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
		return nil
	}
	if _, err := t.fd.Write(pkt); err != nil {
		t.mu.Lock()
		t.dropped++
		if t.fdErr == nil {
			t.fdErr = err
		}
		t.mu.Unlock()
	}
	return nil
}

// readFramed is the test-side inverse of writeFramed (datagram read).
func readFramed(f *os.File) ([]byte, error) {
	pkt := make([]byte, 65535)
	n, err := f.Read(pkt)
	if err != nil {
		return nil, err
	}
	return pkt[:n], nil
}

// Read implements tun.Device: blocks until one packet is available from Swift.
func (t *packetTUN) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	select {
	case pkt := <-t.inbound:
		n := copy(bufs[0][offset:], pkt)
		sizes[0] = n
		return 1, nil
	case <-t.closedCh:
		return 0, errors.New("packet TUN closed")
	}
}

// Write implements tun.Device: queues decrypted packets for delivery to Swift.
// Under backpressure packets are dropped (the overlay is UDP-like; higher
// layers retransmit) rather than blocking the WG routine.
func (t *packetTUN) Write(bufs [][]byte, offset int) (int, error) {
	for _, buf := range bufs {
		raw := buf[offset:]
		// 出口模式（IPv4-only 数据面）：丢弃来自 peer 的 IPv6 包（v6 黑洞），
		// 客户端的 Happy Eyeballs 会快速回退 IPv4 经出口转发，避免 v6 直连
		// 绕过出口。非出口场景下 peer 不会送来 v6（隧道本身只含 IPv4 路由）。
		if len(raw) >= 1 && raw[0]>>4 == 6 {
			t.mu.Lock()
			t.dropped++
			t.mu.Unlock()
			continue
		}
		// 非 dst=本机 的包（如内核 ICMP 错误风暴）不再投递回系统，否则会被
		// 路由表再次送进隧道，自放大挤死整条管道。
		if !t.isLocalDst(raw) {
			t.mu.Lock()
			t.dropped++
			t.mu.Unlock()
			continue
		}
		pkt := make([]byte, len(raw))
		copy(pkt, raw)
		if err := t.writeFramed(pkt); err != nil {
			t.mu.Lock()
			t.dropped++
			t.mu.Unlock()
		}
	}
	return len(bufs), nil
}

// WriteInbound injects one packet from the NE flow into the WG device.
// Called from Swift (gomobile-exported via Engine.SendPacket).
func (t *packetTUN) WriteInbound(packet []byte) error {
	select {
	case <-t.closedCh:
		return errors.New("packet TUN closed")
	default:
	}
	// LatticeDNS: answer *.lattice DNS queries locally instead of tunneling,
	// once a peer table is wired (engine.go does this right after the node
	// comes up). Without a table there is nothing to resolve from.
	if t.peerSource != nil {
		if resp, handled := t.interceptDNS(packet); handled {
			if resp != nil {
				if err := t.writeFramed(resp); err != nil {
					t.mu.Lock()
					t.dropped++
					t.mu.Unlock()
				}
			}
			return nil
		}
	}
	pkt := make([]byte, len(packet))
	copy(pkt, packet)
	select {
	case t.inbound <- pkt:
		return nil
	case <-t.closedCh:
		return errors.New("packet TUN closed")
	default:
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
		return nil
	}
}

// tunnelDNSIP is the resolver address the client points the system at
// (NEDNSSettings on the Swift side); queries to it are answered by the engine.
var tunnelDNSIP = net.IPv4(10, 96, 0, 1)

// interceptLatticeDNS answers UDP DNS queries for *.lattice names from the
// current peer table. Returns (responsePacket, true) when the packet was a
// lattice query answered locally; (nil, false) means "forward to WireGuard
// unchanged" (non-DNS, non-lattice, or malformed — fail-open).
func (t *packetTUN) interceptDNS(packet []byte) ([]byte, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(packet[0]&0x0f) * 4
	if len(packet) < ihl+8 || packet[9] != 17 { // proto != UDP
		return nil, false
	}
	srcIP := net.IP(append([]byte(nil), packet[12:16]...))
	dstIP := net.IP(append([]byte(nil), packet[16:20]...))
	udp := packet[ihl:]
	if len(udp) < 12 {
		return nil, false
	}
	srcPort := binary.BigEndian.Uint16(udp[0:2])
	dstPort := binary.BigEndian.Uint16(udp[2:4])
	if dstPort != 53 {
		return nil, false
	}
	// Only queries sent to the tunnel's own DNS address are ours to answer. The
	// forwarder's upstream queries are bound to the tunnel and come back through
	// here addressed to the real resolver; intercepting those too re-forwards
	// them forever (thousands of packets a second, extension dead in seconds).
	if !dstIP.Equal(tunnelDNSIP) {
		return nil, false
	}

	query := new(dns.Msg)
	if err := query.Unpack(udp[8:]); err != nil {
		return nil, false
	}
	if len(query.Question) == 0 {
		return nil, false
	}
	q := query.Question[0]
	raw := strings.ToLower(strings.TrimSuffix(strings.ToLower(q.Name), "."))
	if !strings.HasSuffix(raw, ".lattice") {
		// IPv4-only 出口：AAAA 一律空应答（NOERROR 无记录），客户端回退
		// A/IPv4，防止解析层引导 v6 直连绕过出口。
		if q.Qtype == dns.TypeAAAA {
			empty := new(dns.Msg)
			empty.SetRcode(query, dns.RcodeSuccess)
			payload, err := empty.Pack()
			if err != nil {
				return nil, false
			}
			return swapUDPReply(packet, ihl, srcIP, dstIP, srcPort, dstPort, payload), true
		}
		// 出口节点 DNS 接管（§四）：非 lattice 的 A 查询经本机系统 DNS 转发
		// （Clash TUN 接管后按规则解析/分流），应答异步送回查询方。
		if servers := t.dnsServers(); len(servers) > 0 {
			t.forwardDNSAsync(query, packet, srcIP, dstIP, srcPort, dstPort, servers)
			return nil, true
		}
		return nil, false
	}
	name := infra.NormalizeAppID(strings.TrimSuffix(raw, ".lattice"))

	reply := new(dns.Msg)
	reply.SetReply(query)
	reply.RecursionAvailable = true
	resolved := false
	for _, p := range t.peerAddresses() {
		if p.Address == nil || *p.Address == "" {
			continue
		}
		candidate := infra.NormalizeAppID(p.Name)
		if strings.EqualFold(candidate, name) {
			rr, err := dns.NewRR(fmt.Sprintf("%s 10 IN A %s", strings.TrimSuffix(q.Name, "."), *p.Address))
			if err == nil {
				reply.Answer = append(reply.Answer, rr)
				resolved = true
			}
			break
		}
	}
	if !resolved {
		reply.Rcode = dns.RcodeNameError
	}

	dnsPayload, err := reply.Pack()
	if err != nil {
		return nil, false
	}
	return swapUDPReply(packet, ihl, srcIP, dstIP, srcPort, dstPort, dnsPayload), true
}

// swapUDPReply 构造 DNS 应答包：源/目的 IP 与端口对调、填入载荷，
// 重算 UDP 与 IPv4 校验和。lattice 本地应答与上游转发应答共用。
func swapUDPReply(packet []byte, ihl int, srcIP, dstIP net.IP, srcPort, dstPort uint16, dnsPayload []byte) []byte {
	total := ihl + 8 + len(dnsPayload)
	resp := make([]byte, total)
	resp[0] = 0x45
	binary.BigEndian.PutUint16(resp[2:4], uint16(total))
	resp[4], resp[5] = packet[4], packet[5] // 沿用查询包的 IP ID
	resp[6], resp[7] = 0x40, 0x00           // DF
	resp[8] = 64
	resp[9] = 17
	copy(resp[12:16], dstIP)
	copy(resp[16:20], srcIP)

	udpHdr := resp[ihl:]
	binary.BigEndian.PutUint16(udpHdr[0:2], uint16(dstPort))
	binary.BigEndian.PutUint16(udpHdr[2:4], uint16(srcPort))
	binary.BigEndian.PutUint16(udpHdr[4:6], uint16(8+len(dnsPayload)))
	copy(udpHdr[8:], dnsPayload)

	var sum uint32
	sum += uint32(uint16(dstIP[0])<<8 | uint16(dstIP[1]))
	sum += uint32(uint16(dstIP[2])<<8 | uint16(dstIP[3]))
	sum += uint32(uint16(srcIP[0])<<8 | uint16(srcIP[1]))
	sum += uint32(uint16(srcIP[2])<<8 | uint16(srcIP[3]))
	sum += 17
	sum += uint32(8 + len(dnsPayload))
	for i := 0; i < len(udpHdr); i += 2 {
		v := uint16(udpHdr[i]) << 8
		if i+1 < len(udpHdr) {
			v |= uint16(udpHdr[i+1])
		}
		sum += uint32(v)
	}
	for sum>>16 != 0 {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	binary.BigEndian.PutUint16(udpHdr[6:8], ^uint16(sum))

	var ipSum uint32
	for i := 0; i < 20; i += 2 {
		ipSum += uint32(resp[i])<<8 | uint32(resp[i+1])
	}
	for ipSum>>16 != 0 {
		ipSum = (ipSum >> 16) + (ipSum & 0xffff)
	}
	binary.BigEndian.PutUint16(resp[10:12], ^uint16(ipSum))
	return resp
}

// forwardDNSAsync 把非 lattice 的 DNS 查询异步转发给本机上游解析器。
// 出口模式下查询 socket 绑定到隧道接口（IP_BOUND_IF）：DNS 在出口侧解析，
// 不经本机路由表——本机 DNS 环境再糟（Clash TUN、飞行模式、错误的
// resolv.conf）也不影响，且不会与隧道路由相互打架。
func (t *packetTUN) forwardDNSAsync(query *dns.Msg, packet []byte, srcIP, dstIP net.IP, srcPort, dstPort uint16, servers []string) {
	go func() {
		client := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
		if idx := t.ifaceIndex(); idx != 0 {
			// Bound to the tunnel = exit mode: resolve at the exit side with
			// anycast resolvers instead of the local network's (CN) resolver —
			// querying 114 from the HK exit adds a CN roundtrip per lookup and
			// yields CN-CDN answers that then route badly through the exit.
			servers = []string{"8.8.8.8", "1.1.1.1"}
			client.Dialer = &net.Dialer{
				Timeout: 3 * time.Second,
				Control: boundToInterface(idx),
			}
		}
		var resp *dns.Msg
		for _, server := range servers {
			r, _, err := client.Exchange(query.Copy(), server+":53")
			if err == nil && r != nil {
				resp = r
				break
			}
		}
		if resp == nil {
			resp = new(dns.Msg)
			resp.SetRcode(query, dns.RcodeServerFailure)
		}
		// 只保留 A 记录（IPv4-only 出口）。
		filtered := resp.Answer[:0]
		for _, rr := range resp.Answer {
			if rr.Header().Rrtype != dns.TypeAAAA {
				filtered = append(filtered, rr)
			}
		}
		resp.Answer = filtered
		payload, err := resp.Pack()
		if err != nil {
			return
		}
		reply := swapUDPReply(packet, int(packet[0]&0x0f)*4, srcIP, dstIP, srcPort, dstPort, payload)
		_ = t.writeFramed(reply) // 本地合成应答，直接回手机，不经 WG 加密
	}()
}

// tunnelIfaceIndex 找到携带 overlay 地址（10.96.0.0/24）的 utun 接口索引，
// 供上游 DNS socket 做 IP_BOUND_IF 绑定；找不到返回 0（不绑定）。
func tunnelIfaceIndex() int {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if b := ipn.IP.To4(); b != nil && b[0] == 10 && b[1] == 96 {
				return ifc.Index
			}
		}
	}
	return 0
}

// Dropped reports packets discarded due to queue backpressure.
func (t *packetTUN) Dropped() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// File implements tun.Device: no OS file descriptor backs this TUN, so the
// nil result tells wireguard-go to skip fd-specific fast paths.
func (t *packetTUN) File() *os.File { return nil }

func (t *packetTUN) MTU() (int, error) { return t.mtu, nil }

func (t *packetTUN) Name() (string, error) { return t.name, nil }

func (t *packetTUN) Events() <-chan tun.Event { return t.events }

func (t *packetTUN) BatchSize() int { return 1 }

func (t *packetTUN) Close() error {
	t.once.Do(func() {
		close(t.closedCh)
		close(t.events)
	})
	return nil
}
