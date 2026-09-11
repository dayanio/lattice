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

// Package standalone_e2e boots the real `latticed --standalone` binary and
// drives the full zero-K8s control-plane loop over its public surfaces:
// REST (users, workspaces, enrollment tokens, policies, workflow approval)
// and NATS signaling (peer register / GetNetMap). No k3d, no CRDs.
package standalone_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/alatticeio/lattice/pkg/utils/resp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	latticedBin string // path to the latticed binary (built in BeforeSuite when empty)
	serverURL   string // http://127.0.0.1:<port>
	natsURL     string // nats://127.0.0.1:4222

	serverCmd  *exec.Cmd
	serverDir  string // working dir holding lattice.db
	httpClient = &http.Client{Timeout: 10 * time.Second}
)

func TestStandaloneE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Lattice Standalone E2E Suite")
}

var _ = BeforeSuite(func() {
	serverDir = GinkgoT().TempDir()

	bin := latticedBin
	if bin == "" {
		By("Building latticed binary")
		bin = filepath.Join(serverDir, "latticed")
		build := exec.Command("go", "build", "-o", bin, "github.com/alatticeio/lattice/cmd/latticed")
		out, err := build.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "build latticed failed: %s", string(out))
	}

	// Pick a free HTTP port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	port := l.Addr().(*net.TCPAddr).Port
	Expect(l.Close()).To(Succeed())
	serverURL = "http://127.0.0.1:" + strconv.Itoa(port)
	natsURL = "nats://127.0.0.1:4222"

	// The embedded NATS port is fixed; fail fast with an actionable message.
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:4222", time.Second); err == nil {
		_ = conn.Close()
		Fail("port 4222 is already in use — stop other latticed/standalone instances before running this suite")
	}

	By("Starting latticed --standalone on " + serverURL)
	cfgDir := filepath.Join(serverDir, "config")
	Expect(os.MkdirAll(cfgDir, 0o755)).To(Succeed())
	cmd := exec.Command(bin, "--standalone", "--config-dir", cfgDir)
	cmd.Dir = serverDir // lattice.db lands in the temp dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("LATTICE_LISTEN=127.0.0.1:%d", port),
		"LATTICE_RESYNC_INTERVAL=1s", // fast convergence for TTL assertions
	)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	Expect(cmd.Start()).To(Succeed())
	serverCmd = cmd

	// Wait for the HTTP API to come up.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if serverCmd.ProcessState != nil && serverCmd.ProcessState.Exited() {
			Fail("latticed exited during startup")
		}
		resp, err := httpClient.Get(serverURL + "/api/v1/discovery")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	Fail("latticed HTTP API did not become ready within 60s")
})

var _ = AfterSuite(func() {
	if serverCmd != nil && serverCmd.Process != nil {
		_ = serverCmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = serverCmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = serverCmd.Process.Kill()
		}
	}
	_ = os.RemoveAll(serverDir)
})

// ---- REST helpers ----

// apiPOST performs an authenticated JSON POST against the management API.
func apiPOST(url, accessToken string, body any, headers ...map[string]string) (int, *resp.Response) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(raw))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	return doAPI(req)
}

// apiGET performs an authenticated GET against the management API.
func apiGET(url, accessToken string, headers ...map[string]string) (int, *resp.Response) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	return doAPI(req)
}

func doAPI(req *http.Request) (int, *resp.Response) {
	httpResp, err := httpClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s failed", req.Method, req.URL)
	defer httpResp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(httpResp.Body)
	Expect(err).NotTo(HaveOccurred())
	fmt.Fprintf(GinkgoWriter, "API %s %s -> %d body=%s\n", req.Method, req.URL, httpResp.StatusCode, truncate(string(raw), 300))
	var out resp.Response
	_ = json.Unmarshal(raw, &out) // non-JSON bodies decode to zero value
	return httpResp.StatusCode, &out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
