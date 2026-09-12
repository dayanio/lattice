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

// Package daemon provides the client-side node runtime plumbing: pidfile
// management, the local IPC socket (CLI ↔ daemon), and service unit
// generation. It is platform-neutral Go with no external dependencies.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Request is one IPC operation. Op is "status" or "down".
type Request struct {
	Op string `json:"op"`
}

// Response carries the result of an IPC operation.
type Response struct {
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
	Status *StatusInfo `json:"status,omitempty"`
}

// StatusInfo is the daemon's self-reported runtime state.
type StatusInfo struct {
	State          string `json:"state"` // running
	PID            int    `json:"pid"`
	Address        string `json:"address,omitempty"`
	AppID          string `json:"appId,omitempty"`
	AppliedVersion string `json:"appliedVersion,omitempty"`
	UptimeSeconds  int64  `json:"uptimeSeconds"`
}

// Handler processes one IPC request.
type Handler func(Request) Response

// DownCallback is invoked when a "down" operation arrives.
type DownCallback func()

// ServeIPC serves newline-delimited JSON requests on a unix socket until
// ctx is cancelled. The socket file is removed on exit. An existing stale
// socket file is removed before bind.
func ServeIPC(ctx context.Context, socketPath string, handler Handler) error {
	return ServeIPCWithDown(ctx, socketPath, handler, nil)
}

// ServeIPCWithDown is ServeIPC with a callback invoked when a "down"
// operation arrives — the daemon cancels its context in response.
func ServeIPCWithDown(ctx context.Context, socketPath string, handler Handler, onDown DownCallback) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socketPath) // stale socket from a previous run

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			continue
		}
		go handleConn(conn, handler, onDown)
	}
}

func handleConn(conn net.Conn, handler Handler, onDown DownCallback) {
	defer conn.Close() //nolint:errcheck
	dec := json.NewDecoder(conn)
	var req Request
	if err := dec.Decode(&req); err != nil {
		return
	}
	resp := handler(req)
	if req.Op == "down" && onDown != nil {
		onDown()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

// Call sends one IPC request and waits for the response.
func Call(socketPath string, req Request, timeout time.Duration) (*Response, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	defer conn.Close() //nolint:errcheck
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return &resp, fmt.Errorf("%s", resp.Error)
	}
	return &resp, nil
}
