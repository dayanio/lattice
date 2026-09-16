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
	"errors"
	"sync"

	quic "github.com/quic-go/quic-go"
)

type SessionManager struct {
	mu        sync.RWMutex
	sessions  map[uint64]*Session
	quicConns map[uint64]*quic.Conn
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions:  make(map[uint64]*Session),
		quicConns: make(map[uint64]*quic.Conn),
	}
}

// Register stores the session under id, replacing any previous one. A
// replaced session's stream is closed so its handler goroutine exits
// promptly; that handler's later Unregister call is a no-op because it no
// longer owns the slot.
func (m *SessionManager) Register(id uint64, s *Session) {
	m.mu.Lock()
	old := m.sessions[id]
	m.sessions[id] = s
	m.mu.Unlock()
	if old != nil && old != s {
		_ = old.Stream.Close()
	}
}

// Unregister removes the session only if it still owns the slot. The
// identity check prevents a stale connection's deferred cleanup from
// deleting a newer session that re-registered with the same ID (which
// used to blackhole the peer until its next reconnect).
func (m *SessionManager) Unregister(id uint64, s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s == nil || m.sessions[id] != s {
		return
	}
	delete(m.sessions, id)
	delete(m.quicConns, id)
}

// RegisterQUIC stores a QUIC session (control stream + conn) and returns
// it; the caller must pass the returned session to Unregister.
func (m *SessionManager) RegisterQUIC(id uint64, ctrl Stream, conn *quic.Conn) *Session {
	s := &Session{
		ID:     id,
		Stream: ctrl,
		Type:   "QUIC",
	}
	m.mu.Lock()
	old := m.sessions[id]
	m.sessions[id] = s
	m.quicConns[id] = conn
	m.mu.Unlock()
	if old != nil && old != s {
		_ = old.Stream.Close()
	}
	return s
}

func (m *SessionManager) Get(id uint64) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// write serializes access to the session stream. TCP session streams are
// bufio-backed and not safe for concurrent writes; without this lock two
// source peers relaying to the same destination interleave frames and
// corrupt the destination's stream.
func (s *Session) write(frame []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Stream.Write(frame)
}

func (m *SessionManager) Relay(toID uint64, frame []byte) error {
	m.mu.RLock()
	qconn := m.quicConns[toID]
	session := m.sessions[toID]
	m.mu.RUnlock()

	if qconn != nil {
		return qconn.SendDatagram(frame)
	}
	if session != nil {
		_, err := session.write(frame)
		return err
	}
	return errors.New("lrp: relay target not found")
}

func (m *SessionManager) ConnectedPeers() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}
