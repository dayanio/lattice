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

package tvoslib

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
)

func TestMediaHandleReadAtAndSize(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 64) // 1024 bytes

	// 请求日志：断言顺序续读不重开连接、seek 回跳重开且 Range 起点正确
	// （TestMediaHandleSeekReconnect 并入此处——服务端断言 Range 起点）。
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		serveRange(t, w, r, body) // 支持 Range 的最小实现，见下方辅助
	}))
	defer srv.Close()

	h, err := newMediaHandle(srv.URL, func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr) // 单测走本机回环，不依赖引擎
	})
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if h.size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", h.size, len(body))
	}
	buf := make([]byte, 10)
	if n := h.readAt(0, buf); n != 10 || string(buf) != "0123456789" {
		t.Fatalf("readAt(0) = %d %q", n, buf)
	}
	// 顺序续读复用连接（不重开 Range——请求日志下方断言）
	if n := h.readAt(10, buf); n != 10 || string(buf) != "abcdef0123" {
		t.Fatalf("readAt(10) = %d %q", n, buf)
	}
	// seek 回跳重开 Range：offset 1000 处内容为 body[1000:1008]（1000%16==8）
	if n := h.readAt(1000, buf[:8]); n != 8 || string(buf[:8]) != "89abcdef" {
		t.Fatalf("readAt(1000) = %d %q", n, buf[:n])
	}
	// EOF
	if n := h.readAt(int64(len(body)), buf); n != 0 {
		t.Fatalf("readAt(EOF) = %d, want 0", n)
	}
	// 4 个请求：size 探测、offset 0 打开、seek 1000 重开、EOF 1024 重开；
	// readAt(10) 不得产生新请求（顺序复用）。seek 的 Range 起点必须是 1000。
	mu.Lock()
	got := append([]string(nil), ranges...)
	mu.Unlock()
	want := []string{"bytes=0-0", "bytes=0-", "bytes=1000-", "bytes=1024-"}
	if !slices.Equal(got, want) {
		t.Fatalf("request Range log = %q, want %q", got, want)
	}
}

// serveRange: GET 无 Range → 200 全量 + Content-Length；有 Range → 206 分片。
func serveRange(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
	t.Helper()
	spec := r.Header.Get("Range")
	if spec == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
		return
	}
	var start int
	if _, err := fmt.Sscanf(spec, "bytes=%d-", &start); err != nil {
		t.Fatalf("bad range %q", spec)
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)-start))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(body[start:])
}
