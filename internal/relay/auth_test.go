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
	"testing"
)

func TestCheckRegisterToken(t *testing.T) {
	s := &Server{authToken: "secret"}

	// Auth enabled: exact match required.
	if !s.checkRegisterToken([]byte("secret")) {
		t.Fatal("correct token must be accepted")
	}
	if s.checkRegisterToken([]byte("wrong")) {
		t.Fatal("wrong token must be rejected")
	}
	if s.checkRegisterToken(nil) {
		t.Fatal("missing token must be rejected when auth enabled")
	}
	if s.checkRegisterToken([]byte("secre")) {
		t.Fatal("prefix of the token must be rejected")
	}

	// Auth disabled (empty server token): accept anything (legacy open relay).
	open := &Server{}
	if !open.checkRegisterToken(nil) || !open.checkRegisterToken([]byte("anything")) {
		t.Fatal("empty server token must disable auth")
	}
}

func TestSplitURLToken(t *testing.T) {
	addr, token := splitURLToken("10.0.0.1:6266")
	if addr != "10.0.0.1:6266" || token != "" {
		t.Fatalf("plain addr: got %q/%q", addr, token)
	}

	addr, token = splitURLToken("10.0.0.1:6266?token=abc")
	if addr != "10.0.0.1:6266" || token != "abc" {
		t.Fatalf("token addr: got %q/%q", addr, token)
	}

	// token value must survive URL decoding
	addr, token = splitURLToken("10.0.0.1:6266?a=b&token=a%2Bb")
	if addr != "10.0.0.1:6266" || token != "a+b" {
		t.Fatalf("encoded token: got %q/%q", addr, token)
	}
}

func TestRegisterCarriesToken(t *testing.T) {
	c := &lrpClient{authToken: "secret"}
	w := &testWriter{}
	if err := c.register(w); err != nil {
		t.Fatalf("register: %v", err)
	}
	h, err := Unmarshal(w.buf[:HeaderSize])
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if h.Cmd != Register || h.PayloadLen != uint32(len("secret")) {
		t.Fatalf("header: cmd=%d payloadLen=%d", h.Cmd, h.PayloadLen)
	}
	if string(w.buf[HeaderSize:]) != "secret" {
		t.Fatalf("payload: got %q", string(w.buf[HeaderSize:]))
	}
}

type testWriter struct{ buf []byte }

func (w *testWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}
