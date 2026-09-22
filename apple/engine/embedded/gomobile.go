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
	"context"
	"net"
	"time"
)

// This file wraps the engine's net.Conn/net.Listener values in concrete
// structs so gomobile can export them across the language boundary — gobind
// cannot bind Go interfaces like net.Conn. The EmbeddedEngine.Dial/Listen
// methods keep their raw signatures for pure Go callers; embedders going
// through gomobile use DialConn/ListenTCP below.

// EmbeddedConn is a connection on the overlay netstack, exported as a
// concrete type for gomobile. It implements net.Conn.
type EmbeddedConn struct {
	conn net.Conn
}

// Read implements net.Conn.
func (c *EmbeddedConn) Read(b []byte) (int, error) { return c.conn.Read(b) }

// Write implements net.Conn.
func (c *EmbeddedConn) Write(b []byte) (int, error) { return c.conn.Write(b) }

// Close implements net.Conn.
func (c *EmbeddedConn) Close() error { return c.conn.Close() }

// SetReadDeadline implements net.Conn.
func (c *EmbeddedConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline implements net.Conn.
func (c *EmbeddedConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// SetDeadline implements net.Conn.
func (c *EmbeddedConn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// EmbeddedListener is a TCP listener on the overlay netstack, exported as a
// concrete type for gomobile. It implements net.Listener.
type EmbeddedListener struct {
	ln net.Listener
}

// Accept blocks until the next overlay connection arrives and returns it
// wrapped as an EmbeddedConn.
func (l *EmbeddedListener) Accept() (*EmbeddedConn, error) {
	conn, err := l.ln.Accept()
	if err != nil {
		return nil, err
	}
	return &EmbeddedConn{conn: conn}, nil
}

// Close stops listening. Already-accepted EmbeddedConn values stay usable.
func (l *EmbeddedListener) Close() error { return l.ln.Close() }

// DialConn dials a remote overlay address and returns the connection as an
// EmbeddedConn. It is the gomobile-facing form of EmbeddedEngine.Dial: the
// raw Dial signature (context.Context in, net.Conn out) cannot cross the
// gobind boundary. Dialing runs without a deadline; use SetDeadline on the
// returned connection to bound individual reads and writes.
func (e *EmbeddedEngine) DialConn(network, addr string) (*EmbeddedConn, error) {
	srv, err := e.netstack()
	if err != nil {
		return nil, err
	}
	conn, err := srv.Dial(context.Background(), network, addr)
	if err != nil {
		return nil, err
	}
	return &EmbeddedConn{conn: conn}, nil
}

// ListenTCP listens on the overlay netstack and returns the listener as an
// EmbeddedListener. It is the gomobile-facing form of EmbeddedEngine.Listen
// (net.Listener cannot cross the gobind boundary).
func (e *EmbeddedEngine) ListenTCP(network, addr string) (*EmbeddedListener, error) {
	srv, err := e.netstack()
	if err != nil {
		return nil, err
	}
	ln, err := srv.Listen(network, addr)
	if err != nil {
		return nil, err
	}
	return &EmbeddedListener{ln: ln}, nil
}
