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
	"bytes"
	"io"
	"os"
	"testing"
)

func TestPacketTUN_DeviceContract(t *testing.T) {
	pt := newPacketTUN("lattice", 1280, 0)
	defer pt.Close() //nolint:errcheck

	if name, err := pt.Name(); err != nil || name != "lattice" {
		t.Fatalf("Name() = %q, %v", name, err)
	}
	if mtu, err := pt.MTU(); err != nil || mtu != 1280 {
		t.Fatalf("MTU() = %d, %v", mtu, err)
	}
	if pt.BatchSize() != 1 {
		t.Fatalf("BatchSize() = %d, want 1", pt.BatchSize())
	}
	if pt.File() != nil {
		t.Fatal("File() should be nil for a packet-backed TUN")
	}
	select {
	case <-pt.Events():
		// EventUp is queued on creation.
	default:
		t.Fatal("Events() should deliver EventUp after creation")
	}
}

func TestPacketTUN_SwiftToWGToSwift(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close() //nolint:errcheck
	defer w.Close() //nolint:errcheck
	pt := newPacketTUN("lattice", 1280, int(w.Fd()))
	defer pt.Close() //nolint:errcheck

	// Swift → Go: NE flow packet enters via WriteInbound, WG reads via Read.
	outgoing := []byte{0x45, 0x00, 0x00, 0x28}
	if err := pt.WriteInbound(outgoing); err != nil {
		t.Fatalf("WriteInbound: %v", err)
	}

	bufs := make([][]byte, 1)
	bufs[0] = make([]byte, 2000) // offset headroom
	sizes := make([]int, 1)
	n, err := pt.Read(bufs, sizes, 16)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if n != 1 || sizes[0] != len(outgoing) {
		t.Fatalf("Read n=%d size=%d, want 1/%d", n, sizes[0], len(outgoing))
	}
	if !bytes.Equal(bufs[0][16:16+sizes[0]], outgoing) {
		t.Fatal("Read returned corrupted packet")
	}

	// Go → Swift: WG writes decrypted packets, Swift reads framed from the
	// egress socketpair end.
	incoming := []byte{0x45, 0x00, 0x00, 0x34}
	wrote, err := pt.Write([][]byte{incoming}, 0)
	if err != nil || wrote != 1 {
		t.Fatalf("Write = %d, %v", wrote, err)
	}
	got, err := readFramed(r)
	if err != nil {
		t.Fatalf("readFramed: %v", err)
	}
	if !bytes.Equal(got, incoming) {
		t.Fatal("readFramed returned corrupted packet")
	}
}

func TestPacketTUN_CloseUnblocks(t *testing.T) {
	pt := newPacketTUN("lattice", 1280, 0)
	go func() {
		_ = pt.Close()
	}()
	_, err := pt.Read(make([][]byte, 1), make([]int, 1), 0)
	if err == nil {
		t.Fatal("Read after Close should fail")
	}
	if err := pt.WriteInbound([]byte{1}); err == nil {
		t.Fatal("WriteInbound after Close should fail")
	}
}

func TestPacketTUN_DropCounting(t *testing.T) {
	// No egress fd: every decrypted packet must drop (never block WG).
	pt := newPacketTUN("lattice", 1280, 0)
	defer pt.Close() //nolint:errcheck

	// Fill the outbound queue beyond capacity; excess must drop, not block.
	for i := 0; i < 600; i++ {
		_, _ = pt.Write([][]byte{[]byte("x")}, 0)
	}
	if pt.Dropped() == 0 {
		t.Fatal("expected drops when the queue is full")
	}
	if n, err := pt.Write([][]byte{{0x45}}, 0); err != nil || n != 1 {
		t.Fatalf("Write after drops = %d, %v", n, err)
	}
	_ = io.Discard
}
