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
	"net"
	"sync"
)

// ReadWriterConn wrapper for missed data when hijack occurs, for using Read/Write fn.
//
// Its writer is a bufio.Writer, which is not safe for concurrent use, yet a
// relay session's stream is written from several goroutines (the session's
// own handler and every peer relaying to it). Write therefore holds a mutex
// for the whole write + flush, so each Write call lands on the wire whole.
type ReadWriterConn struct {
	net.Conn
	*bufio.ReadWriter

	wmu sync.Mutex
}

func (c *ReadWriterConn) Read(p []byte) (int, error) {
	return c.ReadWriter.Read(p)
}

func (c *ReadWriterConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n, err := c.ReadWriter.Write(p)
	if err != nil {
		return n, err
	}
	// ensure data is sent immediately, not left in bufio's write buffer
	return n, c.Flush()
}
