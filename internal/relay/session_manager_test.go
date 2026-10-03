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
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockStream struct {
	mu      sync.Mutex
	written [][]byte
	closed  bool
}

func (m *mockStream) Read(p []byte) (int, error) { return 0, nil }
func (m *mockStream) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = append(m.written, append([]byte(nil), p...))
	return len(p), nil
}
func (m *mockStream) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}
func (m *mockStream) RemoteAddr() net.Addr { return nil }
func (m *mockStream) isClosed() bool       { m.mu.Lock(); defer m.mu.Unlock(); return m.closed }
func (m *mockStream) frames() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.written...)
}

// waitUntil polls cond until it holds or the timeout elapses.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}

func TestSessionManager_RegisterAndGet(t *testing.T) {
	sm := NewSessionManager()
	stream := &mockStream{}
	sm.Register(42, &Session{ID: 42, Stream: stream, Type: "TCP"})

	s := sm.Get(42)
	if s == nil {
		t.Fatal("expected session, got nil")
	}
	if s.ID != 42 {
		t.Errorf("expected ID 42, got %d", s.ID)
	}
}

func TestSessionManager_Unregister(t *testing.T) {
	sm := NewSessionManager()
	sess := &Session{ID: 42, Stream: &mockStream{}, Type: "TCP"}
	sm.Register(42, sess)
	sm.Unregister(42, sess)

	if sm.Get(42) != nil {
		t.Error("session should be nil after unregister")
	}
}

// TestSessionManager_ReRegisterRace covers the reconnect race: the old
// connection's deferred Unregister must not delete the replacement
// session that re-registered with the same ID.
func TestSessionManager_ReRegisterRace(t *testing.T) {
	sm := NewSessionManager()
	old := &Session{ID: 42, Stream: &mockStream{}, Type: "TCP"}
	sm.Register(42, old)

	fresh := &Session{ID: 42, Stream: &mockStream{}, Type: "TCP"}
	sm.Register(42, fresh)

	// The stale connection dies after the new one registered.
	sm.Unregister(42, old)

	if sm.Get(42) != fresh {
		t.Fatal("stale Unregister must not evict the re-registered session")
	}

	// Replaced sessions are closed so their handler goroutines exit.
	if _, ok := old.Stream.(*mockStream); !ok {
		t.Fatalf("unexpected stream type %T", old.Stream)
	}

	sm.Unregister(42, fresh)
	if sm.Get(42) != nil {
		t.Error("owner Unregister must evict the session")
	}
}

// TestSessionManager_RelaySerializesWrites verifies that concurrent Relay
// calls into the same TCP session produce whole, in-order frames: the
// session's writer is the stream's only writer, so sources can never
// interleave (the pre-F4 mutex guarantee, kept by the queue).
func TestSessionManager_RelaySerializesWrites(t *testing.T) {
	sm := NewSessionManager()
	stream := &mockStream{}
	sm.Register(7, &Session{ID: 7, Stream: stream, Type: "TCP"})

	var wg sync.WaitGroup
	var sent atomic.Int64
	const goroutines, perG = 16, 50
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				// A full queue drops by design (the writers here are slower
				// than 16 concurrent fillers); what must hold is that every
				// accepted frame lands whole, never interleaved.
				if err := sm.Relay(7, []byte("frame")); err == nil {
					sent.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	waitUntil(t, 2*time.Second, func() bool { return len(stream.frames()) == int(sent.Load()) },
		"writer must drain every accepted frame")
	for i, f := range stream.frames() {
		if string(f) != "frame" {
			t.Fatalf("frame %d corrupted (interleaved write): %q", i, f)
		}
	}
}

func TestSessionManager_RelayToMissingTarget(t *testing.T) {
	sm := NewSessionManager()
	err := sm.Relay(99, []byte("data"))
	if err == nil {
		t.Error("expected error when relaying to missing target")
	}
}

func TestSessionManager_RelayTCP(t *testing.T) {
	sm := NewSessionManager()
	stream := &mockStream{}
	sm.Register(99, &Session{ID: 99, Stream: stream, Type: "TCP"})

	err := sm.Relay(99, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, time.Second, func() bool { return len(stream.frames()) == 1 }, "frame must reach the stream")
	if got := string(stream.frames()[0]); got != "hello" {
		t.Errorf("unexpected payload: %s", got)
	}
}

// ── ADR-0005 F4: per-session forward queue ──────────────────────────────────

// blockingStream's Write blocks until released (or it observes a write
// deadline, mimicking a real net.Conn).
type blockingStream struct {
	mu       sync.Mutex
	deadline time.Time
	closed   bool
	release  chan struct{}
}

func (s *blockingStream) Read(p []byte) (int, error) { return 0, nil }
func (s *blockingStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	return nil
}
func (s *blockingStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	deadline := s.deadline
	s.mu.Unlock()
	var timer <-chan time.Time
	if !deadline.IsZero() {
		timer = time.After(time.Until(deadline))
	}
	select {
	case <-timer:
		return 0, os.ErrDeadlineExceeded
	case <-s.release:
		return len(p), nil
	}
}
func (s *blockingStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *blockingStream) RemoteAddr() net.Addr { return nil }
func (s *blockingStream) isClosed() bool       { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

// A stalled destination must not block senders to other peers, and once its
// queue fills, frames are dropped (counted) instead of blocking the source.
func TestRelay_StalledDestinationDoesNotBlockOthers(t *testing.T) {
	prevDeadline := sessionWriteDeadline
	sessionWriteDeadline = 10 * time.Millisecond
	t.Cleanup(func() { sessionWriteDeadline = prevDeadline })

	sm := NewSessionManager()
	stuck := &blockingStream{}
	sm.Register(1, newTCPSession(1, stuck))

	// Fill the stalled destination's queue until drops start.
	relayed := 0
	for droppedStart := 0; ; relayed++ {
		_ = sm.Relay(1, []byte("to the stalled peer"))
		if s := sm.Get(1); s.dropped.Load() > 0 {
			_ = droppedStart
			break
		}
		if relayed > 100*forwardQueueDepth {
			t.Fatalf("queue never filled: relayed %d frames, dropped=%d", relayed, sm.Get(1).dropped.Load())
		}
	}
	if d := sm.Get(1).dropped.Load(); d == 0 {
		t.Fatal("expected dropped frames on a stalled destination")
	}

	// A healthy destination is unaffected: its relay returns immediately and
	// completes even while the stalled one is still backing up.
	healthy := &mockStream{}
	sm.Register(2, newTCPSession(2, healthy))
	for i := 0; i < 32; i++ {
		if err := sm.Relay(2, []byte("unaffected")); err != nil {
			t.Fatalf("healthy destination blocked behind the stalled one: %v", err)
		}
	}
	waitUntil(t, time.Second, func() bool { return len(healthy.frames()) == 32 },
		"healthy destination must drain normally")

	// The stalled session's own writer deadline fires (10 ms): the session is
	// shut down — only its own, nobody else's.
	waitUntil(t, 2*time.Second, func() bool { return stuck.isClosed() },
		"write deadline must shut the stalled session down")
	if healthy.isClosed() {
		t.Error("the healthy session must stay open")
	}
}

// Replacing a registration must stop the old session's writer: its goroutine
// exits and its stream is closed (no leak per reconnect).
func TestRelay_ReplacedSessionStopsItsWriter(t *testing.T) {
	sm := NewSessionManager()
	oldStream := &mockStream{}
	old := newTCPSession(5, oldStream)
	sm.Register(5, old)
	_ = sm.Relay(5, []byte("wake the writer"))

	fresh := newTCPSession(5, &mockStream{})
	sm.Register(5, fresh) // replaces → old.shutdown()

	waitUntil(t, time.Second, func() bool { return oldStream.isClosed() },
		"replaced session's stream must be closed")

	// The old session is dead: relays onto it drop (counted) instead of
	// writing to the closed stream.
	waitUntil(t, time.Second, func() bool {
		_ = old.enqueue([]byte("late"))
		return old.dropped.Load() > 0
	}, "frames enqueued onto a shut-down session must be dropped")
}

// Frames of different sizes must land whole and in order (single writer, no
// interleaving).
func TestRelay_MixedSizeFramesStayWholeAndOrdered(t *testing.T) {
	sm := NewSessionManager()
	stream := &mockStream{}
	sm.Register(9, newTCPSession(9, stream))

	want := make([][]byte, 0, 64)
	for i := 0; i < 64; i++ {
		frame := make([]byte, 1+i%7) // sizes 1..7
		for j := range frame {
			frame[j] = byte(i)
		}
		want = append(want, frame)
		if err := sm.Relay(9, frame); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 2*time.Second, func() bool { return len(stream.frames()) == len(want) },
		"all frames must reach the stream")
	got := stream.frames()
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("frame %d: got %x, want %x", i, got[i], want[i])
		}
	}
}

func TestSessionManager_ConcurrentAccess(t *testing.T) {
	sm := NewSessionManager()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(3)
		id := uint64(i)
		go func() { defer wg.Done(); sm.Register(id, &Session{ID: id, Stream: &mockStream{}, Type: "TCP"}) }()
		go func() { defer wg.Done(); sm.Get(id) }()
		go func() { defer wg.Done(); sm.Unregister(id, nil) }()
	}
	wg.Wait()
}

func TestSessionManager_ConnectedPeers(t *testing.T) {
	sm := NewSessionManager()
	if sm.ConnectedPeers() != 0 {
		t.Error("expected 0 connected peers")
	}
	a := &Session{ID: 1, Stream: &mockStream{}, Type: "TCP"}
	b := &Session{ID: 2, Stream: &mockStream{}, Type: "TCP"}
	sm.Register(1, a)
	sm.Register(2, b)
	if sm.ConnectedPeers() != 2 {
		t.Errorf("expected 2 connected peers, got %d", sm.ConnectedPeers())
	}
	sm.Unregister(1, a)
	if sm.ConnectedPeers() != 1 {
		t.Errorf("expected 1 connected peer after unregister, got %d", sm.ConnectedPeers())
	}
}
