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

package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastDiscoverBackoff(t *testing.T) {
	t.Helper()
	prev := discoverBackoff
	discoverBackoff = time.Millisecond
	t.Cleanup(func() { discoverBackoff = prev })
}

// dropConn closes the connection without a response: what the client sees as
// EOF when a proxy or middlebox resets the flow.
func dropConn(w http.ResponseWriter) {
	conn, _, _ := w.(http.Hijacker).Hijack()
	_ = conn.Close()
}

func TestDiscoverWithRetry_SurvivesTransientDrops(t *testing.T) {
	fastDiscoverBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			dropConn(w)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"nats_url":"nats://n:4222","stun_url":"s:3478"}}`))
	}))
	defer srv.Close()

	d, err := discoverWithRetry(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("a transient drop must not abort agent startup: %v", err)
	}
	if d.NatsURL != "nats://n:4222" || calls.Load() != 3 {
		t.Fatalf("got %+v after %d calls, want success on the 3rd", d, calls.Load())
	}
}

func TestDiscoverWithRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	fastDiscoverBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		dropConn(w)
	}))
	defer srv.Close()

	if _, err := discoverWithRetry(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error when the server never answers")
	}
	if got := calls.Load(); got != discoverAttempts {
		t.Fatalf("made %d attempts, want %d", got, discoverAttempts)
	}
}

func TestDiscoverWithRetry_DoesNotRetryAnEmptyAnswer(t *testing.T) {
	fastDiscoverBackoff(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"data":{"nats_url":""}}`))
	}))
	defer srv.Close()

	if _, err := discoverWithRetry(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for an empty nats_url")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("a well-formed but empty answer is a config problem, not a blip; made %d attempts", got)
	}
}

func TestDiscoverWithRetry_StopsWhenContextCancelled(t *testing.T) {
	prev := discoverBackoff
	discoverBackoff = time.Hour
	t.Cleanup(func() { discoverBackoff = prev })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dropConn(w) }))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { _, _ = discoverWithRetry(ctx, srv.URL); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("discoverWithRetry ignored context cancellation while backing off")
	}
}
