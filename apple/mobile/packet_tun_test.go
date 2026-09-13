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

package mobile

import (
	"bytes"
	"testing"
)

func TestPacketTUN_WriteRead(t *testing.T) {
	pt := NewPacketTUN(1280)
	defer pt.Close()

	// Simulate: Swift writes a packet from the NE flow → Go reads it.
	packet := []byte{0x45, 0x00, 0x00, 0x28}
	if err := pt.WritePacket(packet); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Simulate the WG engine: pop from outbound, push back as decrypted.
	popped, ok := pt.PopOutbound()
	if !ok {
		t.Fatal("PopOutbound should return the written packet")
	}
	pt.PushDecrypted(popped)

	got, err := pt.ReadPacket()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, packet) {
		t.Fatal("packet content mismatch")
	}
}

func TestPacketTUN_Close(t *testing.T) {
	pt := NewPacketTUN(1280)
	pt.Close()

	if err := pt.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}

	if err := pt.WritePacket([]byte{0x45}); err == nil {
		t.Fatal("write after close should fail")
	}
	if _, err := pt.ReadPacket(); err == nil {
		t.Fatal("read after close should fail")
	}
	if _, ok := pt.PopOutbound(); ok {
		t.Fatal("PopOutbound after close should return false")
	}
}
