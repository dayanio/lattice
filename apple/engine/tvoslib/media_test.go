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
	"time"
)

// loopbackDial: 单测走本机回环，不依赖引擎。
func loopbackDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

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

	h, err := newMediaHandle(srv.URL, loopbackDial)
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

func TestHandleRegistryGCRoot(t *testing.T) {
	// Critical-1：TVOpenURL 把句柄指针写进 C 内存后，Go 侧失去 GC 根，句柄
	// 可被回收造成 use-after-free。注册表在 Go 侧保活句柄（存活根），
	// TVClose 先注销再 close。直接测 register/unregister 辅助函数。
	h := &mediaHandle{size: 42}
	p := registerHandle(h)
	if got, ok := handles.m[p]; !ok || got != h {
		t.Fatalf("registered handle not rooted in registry: ok=%v got=%v", ok, got)
	}
	unregisterHandle(p)
	if _, ok := handles.m[p]; ok {
		t.Fatal("handle still in registry after unregister")
	}
}

func TestMediaHandleRejectsNonRangeSeek(t *testing.T) {
	// Important-2：服务端不支持 Range（一律 200 全量）时，offset 0 的 200
	// 放行（无 Range 服务端首读），offset>0 的 200 必须报错（readAt=-1），
	// 不得把 pos 置为 offset 静默读错位数据。
	body := []byte("0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body) // 忽略 Range 头，永远 200 全量
	}))
	defer srv.Close()

	h, err := newMediaHandle(srv.URL, loopbackDial)
	if err != nil {
		t.Fatal(err)
	}
	defer h.close()
	if h.size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", h.size, len(body))
	}
	buf := make([]byte, 4)
	if n := h.readAt(0, buf); n != 4 || string(buf) != "0123" {
		t.Fatalf("readAt(0) = %d %q, want 4 %q", n, buf, "0123")
	}
	if n := h.readAt(8, buf); n != -1 {
		t.Fatalf("readAt(8) on no-Range server = %d, want -1 (200 must be rejected)", n)
	}
	// 拒绝后重开 offset 0 仍可用，且内容从头开始（证明 pos 未被 200 置错位）
	if n := h.readAt(0, buf); n != 4 || string(buf) != "0123" {
		t.Fatalf("readAt(0) after rejected seek = %d %q, want 4 %q", n, buf, "0123")
	}
}

func TestMediaHandleCloseUnblocksHungRead(t *testing.T) {
	// Important-1：服务端对数据请求永不响应（连 header 都不发）——Do 挂死，
	// readAt 持 mu 阻塞。close() 必须先触发 context cancel 解除挂死再拿锁，
	// 否则 TVClose 永远阻塞在锁上。断言 close 与 readAt 都快速返回
	//（2s 内；远小于 ResponseHeaderTimeout 的 10s 兜底），readAt=-1。
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Length", "8") // size 探测正常应答
			w.Write([]byte("01234567"))
			return
		}
		entered <- struct{}{} // readAt 已进入挂死的 Do
		<-release             // 永不写响应，直到测试收尾
	}))
	defer srv.Close()
	defer close(release)

	h, err := newMediaHandle(srv.URL, loopbackDial)
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan int, 1)
	go func() {
		readDone <- h.readAt(0, make([]byte, 4))
	}()
	<-entered
	closed := make(chan struct{})
	go func() {
		h.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked >2s on a hung read (cancel did not release the lock)")
	}
	select {
	case n := <-readDone:
		if n != -1 {
			t.Fatalf("readAt on hung server = %d, want -1", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readAt did not unblock after close")
	}
}
