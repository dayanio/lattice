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

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/miekg/dns"
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
	name     string
	mtu      int
	inbound  chan []byte // Swift → WG (to be encrypted)
	outbound chan []byte // WG → Swift (decrypted)
	events   chan tun.Event
	closedCh chan struct{}
	mu       sync.Mutex
	once     sync.Once

	// dropped counts packets discarded because a queue was full.
	dropped uint64

	// LatticeDNS: resolves *.lattice query names to overlay IPv4s. nil 时
	// 不拦截，所有包照常进入 WireGuard。
	dnsResolver func(qname string) (string, bool)
	// peerSource 提供当前组网设备表（名字 → overlay 地址）。
	peerSource func() []*infra.Peer
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

func newPacketTUN(name string, mtu int) *packetTUN {
	if mtu <= 0 {
		mtu = 1280
	}
	t := &packetTUN{
		name:     name,
		mtu:      mtu,
		inbound:  make(chan []byte, 512),
		outbound: make(chan []byte, 512),
		events:   make(chan tun.Event, 4),
		closedCh: make(chan struct{}),
	}
	t.events <- tun.EventUp
	return t
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
		pkt := make([]byte, len(buf)-offset)
		copy(pkt, buf[offset:])
		select {
		case t.outbound <- pkt:
		case <-t.closedCh:
			return 0, errors.New("packet TUN closed")
		default:
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
	// LatticeDNS: answer *.lattice DNS queries locally instead of tunneling.
	if t.dnsResolver != nil {
		if resp, ok := t.interceptLatticeDNS(packet); ok {
			select {
			case t.outbound <- resp:
			case <-t.closedCh:
				return errors.New("packet TUN closed")
			default:
				t.mu.Lock()
				t.dropped++
				t.mu.Unlock()
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

// interceptLatticeDNS answers UDP DNS queries for *.lattice names from the
// current peer table. Returns (responsePacket, true) when the packet was a
// lattice query answered locally; (nil, false) means "forward to WireGuard
// unchanged" (non-DNS, non-lattice, or malformed — fail-open).
func (t *packetTUN) interceptLatticeDNS(packet []byte) ([]byte, bool) {
	fmt.Println("LATTICE-DBG-V2-ENTRY len", len(packet))
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

	// 构造响应包：源/目的 IP 与端口对调，重算 UDP 与 IPv4 校验和。
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

	return resp, true
}

// Dropped reports packets discarded due to queue backpressure.
func (t *packetTUN) Dropped() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// PopOutbound removes one decrypted packet for Swift delivery, blocking
// while the queue is empty (zero CPU wakeups when idle). ok is false when
// the TUN is closed.
func (t *packetTUN) PopOutbound() ([]byte, bool) {
	select {
	case pkt := <-t.outbound:
		return pkt, true
	case <-t.closedCh:
		return nil, false
	}
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
