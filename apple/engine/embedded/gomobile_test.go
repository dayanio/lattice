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

package embedded

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestEmbeddedConn_ReadWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	ec := &EmbeddedConn{conn: c1}
	defer ec.Close()
	defer c2.Close()

	// net.Pipe is synchronous: each direction needs exactly one concurrent
	// reader on the far end, or the writer blocks forever.

	// Inbound: peer writes, the wrapper reads.
	go c2.Write([]byte("ping"))
	buf := make([]byte, 4)
	if err := ec.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	n, err := ec.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "ping" {
		t.Errorf("expected %q, got %q", "ping", buf[:n])
	}

	// Outbound: the wrapper writes, the peer reads.
	got := make(chan []byte, 1)
	go func() {
		b := make([]byte, 4)
		io.ReadFull(c2, b)
		got <- b
	}()
	n, err = ec.Write([]byte("pong"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 4 {
		t.Errorf("expected 4 bytes written, got %d", n)
	}
	select {
	case b := <-got:
		if string(b) != "pong" {
			t.Errorf("expected %q on the peer side, got %q", "pong", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer never received the written bytes")
	}
}

func TestEmbeddedListener_Accept(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	el := &EmbeddedListener{ln: raw}
	defer el.Close()

	go func() {
		conn, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			return
		}
		conn.Write([]byte("hello"))
		conn.Close()
	}()

	ec, err := el.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	defer ec.Close()

	buf := make([]byte, 5)
	if err := ec.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if _, err := io.ReadFull(ec, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if string(buf) != "hello" {
		t.Errorf("expected %q, got %q", "hello", buf)
	}

	if err := el.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestEmbeddedEngine_DialConn_NotStarted(t *testing.T) {
	e := &EmbeddedEngine{cfg: Config{Name: DefaultName}}
	if _, err := e.DialConn("tcp", "10.96.0.2:80"); err == nil {
		t.Error("expected error from DialConn before Start")
	}
	if _, err := e.ListenTCP("tcp", ":8080"); err == nil {
		t.Error("expected error from ListenTCP before Start")
	}
}

func TestEmbeddedConn_ReadUpTo(t *testing.T) {
	c1, c2 := net.Pipe()
	ec := &EmbeddedConn{conn: c1}
	defer ec.Close()
	defer c2.Close()

	go c2.Write([]byte("ping"))
	buf, err := ec.ReadUpTo(16)
	if err != nil {
		t.Fatalf("ReadUpTo: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("expected %q, got %q", "ping", buf)
	}

	if _, err := ec.ReadUpTo(0); err == nil {
		t.Error("expected error for non-positive max")
	}
}
