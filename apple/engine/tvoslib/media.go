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

// Package tvoslib exports the embedded Lattice engine over a plain C ABI
// for tvOS apps that link the static library directly (no gomobile). See
// docs/superpowers/specs/2026-09-30-tvos-cast-design.md §4.2.
package tvoslib

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

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

	mu     sync.Mutex
	body   io.ReadCloser
	pos    int64 // next byte the open body will return
	closed bool
}

func newMediaHandle(urlStr string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (*mediaHandle, error) {
	h := &mediaHandle{
		url:  urlStr,
		dial: dial,
		size: -1,
		client: &http.Client{Transport: &http.Transport{
			DialContext:        dial,
			DisableCompression: true,
		}},
	}
	// Probe total size with a 1-byte Range; servers without Range support
	// return 200 + Content-Length, which we also accept (no seek then).
	req, err := http.NewRequest("GET", urlStr, nil)
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
		h.body.Close()
		h.body = nil
		return -1
	}
	return n
}

func (h *mediaHandle) openAt(offset int64) error {
	if h.body != nil {
		h.body.Close()
		h.body = nil
	}
	req, err := http.NewRequest("GET", h.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return fmt.Errorf("range get: status %d", resp.StatusCode)
	}
	h.body = resp.Body
	h.pos = offset
	return nil
}

func (h *mediaHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.body != nil {
		h.body.Close()
		h.body = nil
	}
	h.closed = true
}
