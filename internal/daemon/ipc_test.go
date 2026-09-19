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

package daemon

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestIPC_StatusRoundtrip(t *testing.T) {
	socket := fmt.Sprintf("/tmp/lt-e2e-%d.sock", time.Now().UnixNano())

	handler := func(req Request) Response {
		if req.Op != "status" {
			t.Errorf("op = %q, want status", req.Op)
		}
		return Response{OK: true, Status: &StatusInfo{
			State: "running", PID: 4242, Address: "10.96.0.2",
			Peers: []PeerStatus{{AppID: "cloud-node-1", PublicKey: "k", Transport: "ice-ready"}},
		}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := ServeIPC(ctx, socket, handler); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	waitForSocket(t, socket, 2*time.Second)

	resp, err := Call(socket, Request{Op: "status"}, 2*time.Second)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !resp.OK || resp.Status == nil || resp.Status.Address != "10.96.0.2" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.Status.Peers) != 1 || resp.Status.Peers[0].Transport != "ice-ready" {
		t.Fatalf("peers not carried over IPC: %+v", resp.Status.Peers)
	}
}

func TestIPC_DownOpTriggersCallback(t *testing.T) {
	socket := fmt.Sprintf("/tmp/lt-e2e-%d.sock", time.Now().UnixNano())
	downCalled := make(chan struct{}, 1)

	handler := func(req Request) Response { return Response{OK: true} }
	onDown := func() { close(downCalled) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := ServeIPCWithDown(ctx, socket, handler, onDown); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	waitForSocket(t, socket, 2*time.Second)

	if _, err := Call(socket, Request{Op: "down"}, 2*time.Second); err != nil {
		t.Fatalf("call down: %v", err)
	}

	select {
	case <-downCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("down callback never invoked")
	}
}

func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("socket never appeared")
}
