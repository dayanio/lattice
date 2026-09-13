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
	"errors"
	"os"
	"sync"

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
	closed   bool
	once     sync.Once

	// dropped counts packets discarded because a queue was full.
	dropped uint64
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

// PopOutbound removes one decrypted packet for Swift delivery.
// ok is false when no packet is pending or the TUN is closed.
func (t *packetTUN) PopOutbound() ([]byte, bool) {
	select {
	case pkt := <-t.outbound:
		return pkt, true
	case <-t.closedCh:
		return nil, false
	default:
		return nil, false
	}
}

// File implements tun.Device: no OS file descriptor backs this TUN, so the
// nil result tells wireguard-go to skip fd-specific fast paths.
func (t *packetTUN) File() *os.File { return nil }

// MTU implements tun.Device.
func (t *packetTUN) MTU() (int, error) { return t.mtu, nil }

// Name implements tun.Device.
func (t *packetTUN) Name() (string, error) { return t.name, nil }

// Events implements tun.Device.
func (t *packetTUN) Events() <-chan tun.Event { return t.events }

// BatchSize implements tun.Device: single-packet reads across the bind bridge.
func (t *packetTUN) BatchSize() int { return 1 }

// Close implements tun.Device.
func (t *packetTUN) Close() error {
	t.once.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		close(t.closedCh)
	})
	return nil
}

// Dropped returns the number of packets dropped due to full queues.
func (t *packetTUN) Dropped() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// Compile-time interface check.
var _ tun.Device = (*packetTUN)(nil)
