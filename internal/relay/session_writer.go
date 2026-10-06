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
	"os"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/metrics"
)

// ADR-0005 F4: the relay used to write relayed frames to a destination's
// stream under the session mutex, synchronously from the *source's* read
// loop. One slow (or half-dead) destination therefore stalled every sender
// relaying to it — head-of-line blocking across peers. The per-session
// writer turns that into: enqueue (never blocks) → sole writer per session
// with a write deadline; a stalled destination kills only its own session.

const (
	// forwardQueueDepth bounds one destination's pending frames. 256 ×
	// (12-byte header + ≤1500-byte payload) ≈ 384 KiB per stalled session —
	// enough to ride out a brief stall without a memory amplifier.
	forwardQueueDepth = 256
)

// sessionWriteDeadline bounds each stream write. On expiry the session is
// shut down; the client's reconnect backoff takes over from there. A var so
// tests can shrink it.
var sessionWriteDeadline = 2 * time.Second

var (
	framesDropped = metrics.NewCounter("ferry_frames_dropped_total")
	writerStalls  = metrics.NewCounter("ferry_session_writer_stalls_total")
)

// writerLogger is set by NewServer; nil elsewhere (tests) — the writer
// then fails silently, which the counters still record.
var writerLogger atomic.Pointer[log.Logger]

func setWriterLogger(l *log.Logger) { writerLogger.Store(l) }

func writerLog() *log.Logger { return writerLogger.Load() }

// newTCPSession builds a TCP session. The writer goroutine starts lazily on
// the first forwarded frame; sessions that never relay cost nothing.
func newTCPSession(id uint64, stream Stream) *Session {
	return &Session{ID: id, Stream: stream, Type: "TCP"}
}

// enqueue hands one complete frame to the session's writer. It never
// blocks: a full queue drops the frame (WireGuard retransmits; the relay is
// best-effort per ADR-0005 §5) so the *source's* read loop keeps draining
// its own connection.
func (s *Session) enqueue(frame []byte) error {
	s.workOnce.Do(s.startWriter)
	select {
	case s.sendCh <- frame:
		return nil
	case <-s.done:
		s.dropped.Add(1)
		framesDropped.Inc()
		return errors.New("relay: session closed")
	default:
		s.dropped.Add(1)
		framesDropped.Inc()
		if l := writerLog(); l != nil {
			l.Warn("relay forward queue full, dropping frame", "to", s.ID, "dropped", s.dropped.Load())
		}
		return errors.New("relay: forward queue full")
	}
}

func (s *Session) startWriter() {
	s.sendCh = make(chan []byte, forwardQueueDepth)
	s.done = make(chan struct{})
	go s.writeLoop()
}

// writeLoop is the session stream's only writer. Each frame is written
// whole under a write deadline, so a destination that stops reading kills
// its own session within 2 s instead of blocking its senders indefinitely.
func (s *Session) writeLoop() {
	for {
		select {
		case frame := <-s.sendCh:
			if err := s.writeFrame(frame); err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					writerStalls.Inc()
				}
				if l := writerLog(); l != nil {
					l.Warn("relay session writer failed, closing session", "to", s.ID, "err", err)
				}
				s.shutdown()
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *Session) writeFrame(frame []byte) error {
	type deadliner interface{ SetWriteDeadline(time.Time) error }
	if d, ok := s.Stream.(deadliner); ok {
		if err := d.SetWriteDeadline(time.Now().Add(sessionWriteDeadline)); err != nil {
			return err
		}
		defer func() { _ = d.SetWriteDeadline(time.Time{}) }() //nolint:errcheck
	}
	_, err := s.Stream.Write(frame)
	return err
}

// shutdown stops the writer and closes the stream. Idempotent; safe from
// the writer itself, a replaced registration or Unregister.
func (s *Session) shutdown() {
	s.closeOnce.Do(func() {
		if s.done != nil {
			close(s.done)
		}
		_ = s.Stream.Close() //nolint:errcheck
	})
}
