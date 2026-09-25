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

package relay

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Several goroutines writing whole frames to one ReadWriterConn must not race
// or interleave. The relay server does exactly that: a session's own
// goroutine (sendFrame) and other peers relaying to it (Session.write) share
// one bufio-backed stream. The stream must stay a clean sequence of frames.
func TestReadWriterConn_ConcurrentFramesStayIntact(t *testing.T) {
	const (
		writers  = 8
		perWrite = 150
		payload  = 200
	)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	rw := &ReadWriterConn{
		Conn:       server,
		ReadWriter: bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)),
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			body := bytes.Repeat([]byte{byte(w + 1)}, payload)
			for i := 0; i < perWrite; i++ {
				if err := sendFrame(rw, uint8(w+1), body); err != nil {
					t.Errorf("writer %d: sendFrame: %v", w, err)
					return
				}
			}
		}(w)
	}

	counts := make(map[uint8]int)
	done := make(chan error, 1)
	go func() {
		hdr := make([]byte, HeaderSize)
		for n := 0; n < writers*perWrite; n++ {
			if _, err := io.ReadFull(client, hdr); err != nil {
				done <- err
				return
			}
			h, err := Unmarshal(hdr)
			if err != nil {
				done <- err
				return
			}
			body := make([]byte, h.PayloadLen)
			if _, err := io.ReadFull(client, body); err != nil {
				done <- err
				return
			}
			want := bytes.Repeat([]byte{h.Cmd}, payload)
			if h.PayloadLen != payload || !bytes.Equal(body, want) {
				done <- io.ErrUnexpectedEOF // a frame carried another frame's bytes
				return
			}
			counts[h.Cmd]++
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream corrupted: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out reading frames")
	}
	wg.Wait()

	for w := 1; w <= writers; w++ {
		if counts[uint8(w)] != perWrite {
			t.Errorf("writer %d: got %d frames, want %d", w, counts[uint8(w)], perWrite)
		}
	}
}
