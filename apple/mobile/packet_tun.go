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

// Package mobile provides the Lattice WireGuard engine for Apple platforms
// (iOS and macOS), designed to be embedded in a Network Extension
// (NEPacketTunnelProvider) via gomobile bind.
//
// Architecture: the Swift NE extension pumps IP packets between the
// NEPacketFlow and this package's PacketTUN. The Go engine runs WireGuard,
// signaling, and netmap convergence on those packets — the exact same
// code path as the Linux agent, minus the kernel TUN and iptables.
package mobile

import (
	"errors"
	"sync"
)

// PacketTUN is a channel-based packet bridge between the Apple
// Network Extension (NEPacketFlow) and the Go WireGuard engine.
//
//   - Swift → Go: WritePacket(packets from NE flow, destined for WG tunnel)
//   - Go → Swift: ReadPacket (WG decrypted output, destined for NE flow)
type PacketTUN struct {
	mu       sync.Mutex
	closed   bool
	inbound  chan []byte // Swift → Go (WG encrypt)
	outbound chan []byte // Go → Swift (WG decrypt)
	closedCh chan struct{}
	once     sync.Once
	mtu      int
}

// NewPacketTUN creates a PacketTUN with the given MTU.
func NewPacketTUN(mtu int) *PacketTUN {
	if mtu <= 0 {
		mtu = 1280
	}
	return &PacketTUN{
		inbound:  make(chan []byte, 256),
		outbound: make(chan []byte, 256),
		closedCh: make(chan struct{}),
		mtu:      mtu,
	}
}

// ReadPacket reads one packet from the WG tunnel (Go → Swift).
func (t *PacketTUN) ReadPacket() ([]byte, error) {
	if t.isClosed() {
		return nil, errors.New("packet TUN closed")
	}
	select {
	case pkt, ok := <-t.inbound:
		if !ok {
			return nil, errors.New("packet TUN closed")
		}
		return pkt, nil
	case <-t.closedCh:
		return nil, errors.New("packet TUN closed")
	}
}

// WritePacket injects a packet from the NE flow into the WG tunnel.
func (t *PacketTUN) WritePacket(packet []byte) error {
	if t.isClosed() {
		return errors.New("packet TUN closed")
	}
	select {
	case t.outbound <- packet:
		return nil
	case <-t.closedCh:
		return errors.New("packet TUN closed")
	}
}

func (t *PacketTUN) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// MTU returns the tunnel MTU.
func (t *PacketTUN) MTU() int { return t.mtu }

// Close shuts down the TUN; subsequent Read/Write return errors.
func (t *PacketTUN) Close() error {
	t.once.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		close(t.closedCh)
	})
	return nil
}

// BatchSize returns 1 (single-packet reads are the gomobile pattern).
func (t *PacketTUN) BatchSize() int { return 1 }

// PopOutbound pops one packet that Swift wrote (for the WG engine to encrypt).
// Returns nil if no packet is pending.
func (t *PacketTUN) PopOutbound() ([]byte, bool) {
	select {
	case pkt, ok := <-t.outbound:
		return pkt, ok
	default:
		return nil, false
	}
}

// PushDecrypted pushes a decrypted packet to the inbound queue (for Swift
// to read via the NE flow). Used by the WG engine after decryption.
func (t *PacketTUN) PushDecrypted(packet []byte) {
	select {
	case t.inbound <- packet:
	default:
	}
}
