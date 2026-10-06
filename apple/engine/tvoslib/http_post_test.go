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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// postRecorder: 记录到达服务端的请求（方法/路径/头/body），返回固定状态码。
type postRecorder struct {
	mu     sync.Mutex
	method string
	path   string
	ctype  string
	bearer string
	body   string
}

func (r *postRecorder) handler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.method = req.Method
		r.path = req.URL.Path
		r.ctype = req.Header.Get("Content-Type")
		r.bearer = req.Header.Get("Authorization")
		r.body = string(b)
		r.mu.Unlock()
		w.WriteHeader(status)
	})
}

func (r *postRecorder) snapshot() (method, path, ctype, bearer, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.method, r.path, r.ctype, r.bearer, r.body
}

func TestTVHTTPPostDeliversPOST(t *testing.T) {
	// 状态回传链路（task-11）：电视端 postStatus → 引擎拨号 POST 到
	// http://<overlay-ip>:47823/__cast/status。断言方法/Content-Type/
	// Bearer 头/body 原样到达，服务端状态码透传回调用方。
	rec := &postRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusOK))
	defer srv.Close()

	code := postWithURL(srv.URL+"/__cast/status",
		`{"state":"playing","positionMS":65000,"title":"a.mp4"}`,
		"tok123", loopbackDial)
	if code != http.StatusOK {
		t.Fatalf("postWithURL = %d, want %d", code, http.StatusOK)
	}
	method, path, ctype, bearer, body := rec.snapshot()
	if method != http.MethodPost {
		t.Fatalf("method = %s, want POST", method)
	}
	if path != "/__cast/status" {
		t.Fatalf("path = %s, want /__cast/status", path)
	}
	if ctype != "application/json" {
		t.Fatalf("Content-Type = %s, want application/json", ctype)
	}
	if bearer != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", bearer, "Bearer tok123")
	}
	wantBody := `{"state":"playing","positionMS":65000,"title":"a.mp4"}`
	if body != wantBody {
		t.Fatalf("body = %q, want %q", body, wantBody)
	}
}

func TestTVHTTPPostPassesServerStatusThrough(t *testing.T) {
	// 面板返回的 401/404/200 都必须原样返回：调用方（TVCastManager）只看
	// 数值，不解析响应体。
	rec := &postRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusUnauthorized))
	defer srv.Close()
	if code := postWithURL(srv.URL+"/__cast/status", "{}", "tok", loopbackDial); code != http.StatusUnauthorized {
		t.Fatalf("postWithURL = %d, want %d", code, http.StatusUnauthorized)
	}
}

func TestTVHTTPPostErrorsAreNegative(t *testing.T) {
	// 传输失败（连接拒绝对应一个已关闭的服务）→ 负数；URL 无法构造请求 →
	// 负数。statusPostTimeout 内必须返回，不得挂死调用线程。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 立刻关闭 → dial 连接拒绝

	done := make(chan int, 1)
	go func() { done <- postWithURL(srv.URL+"/__cast/status", "{}", "tok", loopbackDial) }()
	select {
	case code := <-done:
		if code >= 0 {
			t.Fatalf("postWithURL on dead server = %d, want negative", code)
		}
	case <-time.After(statusPostTimeout + 2*time.Second):
		t.Fatal("postWithURL blocked past statusPostTimeout on a dead server")
	}
	if code := postWithURL("://bad-url", "{}", "tok", loopbackDial); code >= 0 {
		t.Fatalf("postWithURL with bad URL = %d, want negative", code)
	}
}

func TestTVHTTPPostTimeoutDoesNotApplyToMediaClient(t *testing.T) {
	// Critical：newHTTPClient 是 media.go 的 Range 拉流与状态回传共用的
	// client 构造。mediaHandle 的流式读可以远超 15s（一部 2 小时的片子），
	// 若共用构造带 client.Timeout，长片播到一半读会被整体掐死——所以整体
	// 超时只允许加在 postWithURL 自己克隆的副本上，共享构造必须 Timeout=0
	//（仅 ResponseHeaderTimeout 兜底，与 media.go 既有语义一致）。
	c := newHTTPClient(loopbackDial)
	if c.Timeout != 0 {
		t.Fatalf("newHTTPClient.Timeout = %s, want 0 (media streams must not get a whole-request deadline)", c.Timeout)
	}
	if c.Transport == nil {
		t.Fatal("newHTTPClient.Transport = nil")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", tr.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Fatal("DialContext not wired (must dial through the injected dial function)")
	}
}

// postWithURL 必须走注入的 dial 而不是系统解析：用一个"只接受循环回拨号"
// 的受限 dial 证明请求确实经注入路径出门（生产里即引擎 overlay 拨号）。
func TestTVHTTPPostDialsThroughInjectedDial(t *testing.T) {
	rec := &postRecorder{}
	srv := httptest.NewServer(rec.handler(http.StatusOK))
	defer srv.Close()

	// 拒绝解析主机名的 dial：地址非回环即报错——生产里就是 e.Dial。
	restrictive := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, err := net.SplitHostPort(addr); err != nil || host != "127.0.0.1" {
			return nil, context.Canceled
		}
		return loopbackDial(ctx, network, addr)
	}
	u := srv.URL // httptest 默认绑 127.0.0.1
	code := postWithURL(u+"/__cast/status", "{}", "tok", restrictive)
	if code != http.StatusOK {
		t.Fatalf("postWithURL via restrictive dial = %d, want %d (request must go through the injected dial)", code, http.StatusOK)
	}
}
