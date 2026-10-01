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

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// responseHeaderTimeout bounds every HTTP request: without it a server that
// accepts the connection but never answers would hang TVOpenURL/readAt
// (and, while readAt holds the mutex, TVClose) forever.
const responseHeaderTimeout = 10 * time.Second

// statusPostTimeout is the whole-request ceiling for the status uplink
// (TVHTTPPost): a one-shot POST must not hang the caller's timer thread.
// It is deliberately NOT part of newHTTPClient — the media Range stream
// legitimately streams far longer than this (a two-hour movie), so a
// whole-request deadline on the shared client would kill long plays.
const statusPostTimeout = 15 * time.Second

// newHTTPClient builds the engine-dialed HTTP client shared by the media
// Range stream and the status uplink. Timeout is deliberately zero here:
// callers that need a whole-request deadline set it on their own copy (see
// statusPostTimeout). Tests dial loopback through the same injection point.
func newHTTPClient(dial func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:           dial,
		DisableCompression:    true,
		ResponseHeaderTimeout: responseHeaderTimeout,
	}}
}

// mediaHandle serves one URL over an injectable dial function. Production
// dials through the embedded engine's netstack; tests dial loopback.
// ReadAt keeps one Range stream open and follows it while the caller reads
// sequentially (the common case); any offset jump reopens with a fresh
// Range. All methods are safe for concurrent use — PlayerKit may call read
// from multiple threads (see MediaRandomAccessReader's contract).
type mediaHandle struct {
	url    string
	dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	size   int64
	client *http.Client

	mu   sync.Mutex // guards body, pos, closed
	body io.ReadCloser
	pos  int64 // next byte the open body will return

	// cancel aborts the in-flight request, from before its Do until its
	// body is discarded. It has its own lock so close() can fire it without
	// waiting on mu: a read blocked in a hung Do/Body.Read holds mu for as
	// long as the server stays silent, and cancelling first is exactly what
	// unblocks it.
	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// closing is set under cancelMu the moment close() starts. openAt checks
	// it under cancelMu after publishing its cancel and refuses to start a
	// new request against a closing handle. Without it a read that raced in
	// after close's cancel snapshot could hang in Do while close waits on
	// mu with nothing left to cancel — the loop in close re-snapshots, but
	// refusing new requests up front keeps the in-flight set finite.
	closing bool

	closed bool
}

func newMediaHandle(urlStr string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*mediaHandle, error) {
	h := &mediaHandle{
		url:    urlStr,
		dial:   dial,
		size:   -1,
		client: newHTTPClient(dial),
	}
	// Probe total size with a 1-byte Range; servers without Range support
	// return 200 + Content-Length, which we also accept (no seek then).
	ctx, cancel := context.WithTimeout(context.Background(), responseHeaderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", "bytes=0-0")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		// "bytes 0-0/12345"
		if i := strings.LastIndex(cr, "/"); i >= 0 {
			if n, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
				h.size = n
			}
		}
	} else if resp.StatusCode == http.StatusOK && resp.ContentLength > 0 {
		h.size = resp.ContentLength
	}
	return h, nil
}

// readAt reads up to len(buf) bytes starting at offset. Returns bytes read
// (0 = EOF) or -1 on error.
func (h *mediaHandle) readAt(offset int64, buf []byte) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return -1
	}
	if h.body == nil || h.pos != offset {
		if err := h.openAt(offset); err != nil {
			return -1
		}
	}
	n, err := io.ReadFull(h.body, buf)
	h.pos += int64(n)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return n
	}
	if err != nil {
		h.discardBody()
		return -1
	}
	return n
}

// publishCancel makes c reachable for close() while the request is in
// flight — Do may hang forever while holding h.mu, so the cancel must be
// visible before it starts, not after the response arrives. Caller must
// hold h.mu.
func (h *mediaHandle) publishCancel(c context.CancelFunc) {
	h.cancelMu.Lock()
	h.cancel = c
	h.cancelMu.Unlock()
}

// unpublishCancel drops the in-flight cancel entry. Safe unconditionally
// because openAt calls are serialized by h.mu (the entry can only be this
// request's own) and close() never writes it. Caller must hold h.mu.
func (h *mediaHandle) unpublishCancel() {
	h.cancelMu.Lock()
	h.cancel = nil
	h.cancelMu.Unlock()
}

// discardBody cancels the in-flight request and then closes the body. The
// cancel must happen first: a request stuck in Do or Read is not released by
// closing the body alone, but the context cancel tears it down immediately.
// Caller must hold h.mu.
func (h *mediaHandle) discardBody() {
	h.cancelMu.Lock()
	cancel := h.cancel
	h.cancel = nil
	h.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if h.body != nil {
		h.body.Close()
		h.body = nil
	}
}

func (h *mediaHandle) openAt(offset int64) error {
	h.discardBody()
	ctx, cancel := context.WithCancel(context.Background())
	h.publishCancel(cancel) // before Do: close() must reach it while Do may hang
	// close() may have started since our snapshot last looked; refuse to
	// point a fresh request at a closing handle. Anything that raced past
	// this check is still published, so close's retry loop cancels it.
	h.cancelMu.Lock()
	closing := h.closing
	h.cancelMu.Unlock()
	if closing {
		h.unpublishCancel()
		cancel()
		return fmt.Errorf("handle is closing")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", h.url, nil)
	if err != nil {
		h.unpublishCancel()
		cancel()
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	resp, err := h.client.Do(req)
	if err != nil {
		h.unpublishCancel()
		cancel()
		return err
	}
	// 206 = Range honored. 200 at offset 0 = server without Range support
	// serving the full body; fine for a first sequential read (nothing
	// before 0 to seek to). 200 at offset > 0 means the server ignored our
	// Range: adopting it would silently hand back bytes from the wrong
	// offset (h.pos=offset while the body starts at 0), so reject — readAt
	// reports -1.
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		h.unpublishCancel()
		cancel()
		return fmt.Errorf("range get: status %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		resp.Body.Close()
		h.unpublishCancel()
		cancel()
		return fmt.Errorf("range get: server ignored Range at offset %d (status 200)", offset)
	}
	h.body = resp.Body // cancel stays published: it aborts this body's request
	h.pos = offset
	return nil
}

// closeRetryInterval is how often close re-checks the lock while a racing
// read drains; closeRetryTimeout is the defensive ceiling after which close
// gives up (leaking the handle) rather than hanging the caller forever.
const (
	closeRetryInterval = 5 * time.Millisecond
	closeRetryTimeout  = 5 * time.Second
)

// postWithURL sends one status POST over the injected dial (production: the
// engine's overlay netstack; tests: loopback) and returns the HTTP status
// code the server answered with, or a negative value on local failure:
//
//	-2 request could not be constructed (bad URL/body)
//	-3 transport error (dial failure, timeout, no response)
//
// The whole request is bounded by statusPostTimeout so the caller's timer
// thread can never hang on it. The response body is drained and closed —
// callers only consume the numeric status.
func postWithURL(urlStr, body, bearer string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) int {
	client := *newHTTPClient(dial)
	client.Timeout = statusPostTimeout
	req, err := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(body))
	if err != nil {
		return -2
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return -3
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode
}

func (h *mediaHandle) close() {
	// Publish closing under cancelMu first: any openAt past this point
	// refuses to start a new request, so the set of in-flight requests this
	// loop still has to tear down is finite.
	h.cancelMu.Lock()
	h.closing = true
	h.cancelMu.Unlock()

	// Cancel before taking mu: a read blocked in a hung Do/Body.Read holds
	// mu indefinitely, so firing the context cancel is what lets this close
	// acquire the lock and complete instead of blocking TVClose forever.
	// A read that raced in after a snapshot re-publishes a fresh cancel, so
	// snapshot+cancel+TryLock loops until the lock is ours — a one-shot
	// snapshot here could miss it and block TVClose for good.
	deadline := time.Now().Add(closeRetryTimeout)
	for {
		h.cancelMu.Lock()
		cancel := h.cancel
		h.cancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if h.mu.TryLock() {
			h.discardBody() // close the body; also catches a request that raced in
			h.closed = true
			h.mu.Unlock()
			return
		}
		if time.Now().After(deadline) {
			// Defensive ceiling only: every mu holder reacts to cancel, so
			// this should be unreachable. If it ever fires, leak the handle
			// rather than hang the calling (player) thread for good.
			log.Printf("tvoslib: mediaHandle.close: giving up after %s, a read is still active and did not react to cancel (url=%q)", closeRetryTimeout, h.url)
			return
		}
		time.Sleep(closeRetryInterval)
	}
}
