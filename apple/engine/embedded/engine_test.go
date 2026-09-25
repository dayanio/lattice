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

package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// testControlPlane points at the local mac-demo standalone control plane
// used throughout this repo's manual testing (see the Global Constraints
// section of this plan). Override with LATTICE_EMBED_TEST_SERVER if needed.
const testControlPlaneDefault = "http://127.0.0.1:8080"

func testControlPlane() string {
	if v := os.Getenv("LATTICE_EMBED_TEST_SERVER"); v != "" {
		return v
	}
	return testControlPlaneDefault
}

// testWorkspaceSlug is the workspace the tests create devices in. The
// standalone default is "mac-demo"; override when pointing the suite at a
// control plane with a different workspace (e.g. "run-test").
func testWorkspaceSlug() string {
	if v := os.Getenv("LATTICE_EMBED_TEST_WORKSPACE"); v != "" {
		return v
	}
	return "mac-demo"
}

// peerIP is the overlay address of the mesh peer the Dial/Listen tests talk
// to (default: the mac-demo container node at 10.96.0.2).
func peerIP() string {
	if v := os.Getenv("LATTICE_EMBED_PEER_IP"); v != "" {
		return v
	}
	return "10.96.0.2"
}

// peerExecCmd runs a command on the mesh peer host. Default assumes the peer
// runs as local container "mac-node-a"; override for remote peers, e.g.
// LATTICE_EMBED_PEER_EXEC="ssh root@<host> docker exec lattice-cloud-node".
func peerExecCmd(args ...string) *exec.Cmd {
	prefix := os.Getenv("LATTICE_EMBED_PEER_EXEC")
	if prefix == "" {
		prefix = "docker exec mac-node-a"
	}
	return exec.Command("sh", "-c", prefix+" "+strings.Join(args, " "))
}

// requireIntegration skips the test unless LATTICE_EMBED_INTEGRATION=1 is
// set, so `go test ./...` stays green on machines without the local
// mac-demo control plane + docker containers running.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("LATTICE_EMBED_INTEGRATION") != "1" {
		t.Skip("set LATTICE_EMBED_INTEGRATION=1 to run against the local mac-demo control plane")
	}
}

// adminToken logs into the control plane as admin (the standing dev
// credentials are admin/123456; override with LATTICE_EMBED_TEST_USER /
// LATTICE_EMBED_TEST_PASS for control planes with rotated credentials) and
// returns a bearer token plus the target workspace ID.
func adminToken(t *testing.T) (bearer, workspaceID string) {
	t.Helper()
	user := os.Getenv("LATTICE_EMBED_TEST_USER")
	if user == "" {
		user = "admin"
	}
	pass := os.Getenv("LATTICE_EMBED_TEST_PASS")
	if pass == "" {
		pass = "123456"
	}
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := http.Post(testControlPlane()+"/api/v1/users/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if out.Data.Token == "" {
		t.Fatalf("login returned no token")
	}

	req, _ := http.NewRequest(http.MethodGet, testControlPlane()+"/api/v1/workspaces/list", nil)
	req.Header.Set("Authorization", "Bearer "+out.Data.Token)
	wsResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	defer wsResp.Body.Close()
	var wsOut struct {
		Data struct {
			List []struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(wsResp.Body).Decode(&wsOut); err != nil {
		t.Fatalf("decode workspaces response: %v", err)
	}
	for _, ws := range wsOut.Data.List {
		if ws.Slug == testWorkspaceSlug() {
			return out.Data.Token, ws.ID
		}
	}
	t.Fatalf("mac-demo workspace not found")
	return "", ""
}

// mintEnrollmentToken creates a fresh, single-name enrollment token in the
// mac-demo workspace. name doubles as the token value (see the control
// plane's generateToken handler).
func mintEnrollmentToken(t *testing.T, bearer, workspaceID, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"name": name, "expiry": "1h", "limit": 1})
	req, _ := http.NewRequest(http.MethodPost, testControlPlane()+"/api/v1/token/generate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Workspace-Id", workspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if out.Data.Token == "" {
		t.Fatalf("token generation returned empty token")
	}
	return out.Data.Token
}

// approvePeer approves a device that registered pending approval. It
// tolerates a device that never needed approval (already-approved
// devices just get a no-op status update).
func approvePeer(t *testing.T, bearer, workspaceID, name string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"status": "approved"})
	req, _ := http.NewRequest(http.MethodPut, testControlPlane()+"/api/v1/peers/"+name+"/approval", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Workspace-Id", workspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("approve peer: %v", err)
	}
	defer resp.Body.Close()
}

// selectRoute 给消费端设备选择某个 provider 为出口（route-selection API）。
func selectRoute(t *testing.T, bearer, workspaceID, consumer, provider string) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"provider": provider, "selected": true})
	req, _ := http.NewRequest(http.MethodPost, testControlPlane()+"/api/v1/peers/"+consumer+"/route-selection", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Workspace-Id", workspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("select route: %v", err)
	}
	defer resp.Body.Close()
}

// TestEmbeddedEngine_ExitRoutePropagation（探针）：给测试设备选择
// cloud-node-1 为出口，验证服务端 netmap 把 0.0.0.0/0 传播进引擎的
// peer 管理器（出口数据面的信令链路验收，设计文档 §五）。
func TestEmbeddedEngine_ExitRoutePropagation(t *testing.T) {
	requireIntegration(t)

	bearer, workspaceID := adminToken(t)
	deviceName := fmt.Sprintf("embed-exitroute-%d", time.Now().UnixNano())
	token := mintEnrollmentToken(t, bearer, workspaceID, deviceName)
	configJSON, _ := json.Marshal(Config{ServerURL: testControlPlane(), Token: token, Name: deviceName})
	e, err := New(string(configJSON))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Start(ctx) }()

	selectRoute(t, bearer, workspaceID, deviceName, "cloud-node-1")

	deadline := time.Now().Add(45 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, deviceName)
		node := e.node
		if node == nil {
			time.Sleep(time.Second)
			continue
		}
		for _, peer := range node.GetPeerManager().GetAll() {
			if peer.Name == "cloud-node-1" && strings.Contains(peer.AllowedIPs, "0.0.0.0/0") {
				found = true
				t.Logf("cloud-node-1 AllowedIPs = %s", peer.AllowedIPs)
				break
			}
		}
		if found {
			break
		}
		time.Sleep(time.Second)
	}
	if !found {
		t.Fatal("0.0.0.0/0 never propagated into the engine's peer manager")
	}
	cancel()
}

func TestEmbeddedEngine_StartStop(t *testing.T) {
	requireIntegration(t)

	bearer, workspaceID := adminToken(t)
	deviceName := fmt.Sprintf("embed-test-%d", time.Now().UnixNano())
	token := mintEnrollmentToken(t, bearer, workspaceID, deviceName)

	configJSON, _ := json.Marshal(Config{
		ServerURL: testControlPlane(),
		Token:     token,
		Name:      deviceName,
	})
	e, err := New(string(configJSON))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startErr := make(chan error, 1)
	go func() { startErr <- e.Start(ctx) }()

	// Registration may land in "pending approval" first; approve once the
	// device shows up, then wait until the engine reports its overlay
	// address (Start stays running — it blocks until ctx is cancelled).
	deadline := time.Now().Add(30 * time.Second)
	for e.OverlayAddress() == "" && time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, deviceName)
		time.Sleep(500 * time.Millisecond)
	}

	if e.OverlayAddress() == "" {
		t.Fatal("timed out waiting for the engine to acquire an overlay address")
	}
	t.Logf("embedded engine got overlay address %s", e.OverlayAddress())

	// Tear down via the context; only then does Start return (nil).
	cancel()
	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for Start to return after cancel")
	}

	if e.OverlayAddress() != "" {
		t.Errorf("expected overlay address to be cleared after teardown, got %q", e.OverlayAddress())
	}

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestEmbeddedEngine_StartAsyncStop(t *testing.T) {
	requireIntegration(t)

	bearer, workspaceID := adminToken(t)
	deviceName := fmt.Sprintf("embed-async-%d", time.Now().UnixNano())
	token := mintEnrollmentToken(t, bearer, workspaceID, deviceName)

	configJSON, _ := json.Marshal(Config{
		ServerURL: testControlPlane(),
		Token:     token,
		Name:      deviceName,
	})
	e, err := New(string(configJSON))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// StartAsync returns immediately; registration happens in the background.
	if err := e.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	if err := e.StartAsync(); err == nil {
		t.Error("expected second StartAsync to report the engine already running")
	}

	deadline := time.Now().Add(30 * time.Second)
	for e.OverlayAddress() == "" && time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, deviceName)
		time.Sleep(500 * time.Millisecond)
	}
	if e.OverlayAddress() == "" {
		t.Fatal("timed out waiting for the engine to acquire an overlay address")
	}
	t.Logf("embedded engine (StartAsync) got overlay address %s", e.OverlayAddress())

	// Stop cancels the engine's own context and waits for teardown.
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if e.OverlayAddress() != "" {
		t.Errorf("expected overlay address to be cleared after Stop, got %q", e.OverlayAddress())
	}

	// A stopped engine can be started again.
	if err := e.StartAsync(); err != nil {
		t.Fatalf("StartAsync after Stop: %v", err)
	}
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop after restart: %v", err)
	}
}

// startEmbeddedEngine registers a fresh device and waits for it to come up,
// returning the running engine and a cleanup func. Shared by tests in this
// file that need a live engine, not just Start/Stop.
func startEmbeddedEngine(t *testing.T, namePrefix string) (*EmbeddedEngine, context.CancelFunc) {
	t.Helper()
	bearer, workspaceID := adminToken(t)
	deviceName := fmt.Sprintf("%s-%d", namePrefix, time.Now().UnixNano())
	token := mintEnrollmentToken(t, bearer, workspaceID, deviceName)

	configJSON, _ := json.Marshal(Config{
		ServerURL: testControlPlane(),
		Token:     token,
		Name:      deviceName,
	})
	e, err := New(string(configJSON))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go e.Start(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for e.OverlayAddress() == "" && time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, deviceName)
		time.Sleep(500 * time.Millisecond)
	}
	if e.OverlayAddress() == "" {
		cancel()
		t.Fatal("timed out waiting for engine to acquire an overlay address")
	}
	// Give the netmap/ICE handshake a moment to converge with mac-node-a
	// before the test tries to talk to it.
	time.Sleep(3 * time.Second)
	return e, cancel
}

func TestEmbeddedEngine_Listen_ReachableFromContainer(t *testing.T) {
	requireIntegration(t)

	e, cancel := startEmbeddedEngine(t, "embed-listen")
	defer cancel()

	ln, err := e.Listen("tcp", e.OverlayAddress()+":9500")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		io.Copy(conn, conn)
	}()

	out, err := peerExecCmd("nc", "-zv", "-w", "3", e.OverlayAddress(), "9500").CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec nc -zv: %v (%s)", err, out)
	}

	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("Listen never accepted the connection from mac-node-a")
	}
}

func TestEmbeddedEngine_Dial_ReachesContainer(t *testing.T) {
	requireIntegration(t)

	e, cancel := startEmbeddedEngine(t, "embed-dial")
	defer cancel()

	// mac-node-a listens on 10.96.0.2:9501 for the duration of the nc call.
	listenCmd := peerExecCmd("nc", "-l", "-p", "9501")
	if err := listenCmd.Start(); err != nil {
		t.Fatalf("start docker exec nc -l: %v", err)
	}
	defer listenCmd.Process.Kill()
	time.Sleep(1 * time.Second)

	ctx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, err := e.Dial(ctx, "tcp", peerIP()+":9501")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
}
