# Apple Embedded Engine (M1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `apple/engine/embedded` 包：一个不经过 `NEPacketTunnelProvider` 的嵌入式引擎，注册进 Lattice 网络后直接暴露 `Dial`/`Listen`，验证方式是纯 Go test（不涉及 gomobile），跑在本机已搭好的 `mac-demo` workspace 本地测试环境上。

**Architecture:** 复用 `internal/agent.NewNode` 已有的 `CustomTUN`/`ProvisionerFactory` 扩展点——这正是 Linux AgentSandbox 接 gVisor 用的同一个口子（`NewNode` 源码注释原话："The sandbox supplies a gVisor TUNAdapter instead of creating a kernel TUN"）。`EmbeddedEngine.Start` 依次调用：`latticeagent.RegisterSandboxViaNATSNotify` 拿 overlay IP → `shim.NewServer(overlayIP, nil)` 建 netstack → `internal/agent/gvisor.NewTUNAdapter`/`InjectIntoChannel` 桥接 `shim.Server.Channel()` 到 wireguard-go → `latticeagent.NewNode` 用这个 TUN + `internal/agent/gvisor.NewSandboxProvisionerFactory` 作 Provisioner。以上四个函数全部已存在、已导出，本计划不改动它们一行代码，只新写编排逻辑。`Dial`/`Listen` 直接透传给 `shim.Server`。

**Tech Stack:** Go 1.26、`github.com/alatticeio/lattice-shim`（需先把 `go.mod` 升级到含 `shim.Server` 的版本）、`internal/agent`、`internal/agent/gvisor`（均已存在）。

## Global Constraints

- 模块：`github.com/alatticeio/lattice`，Go 1.26.0（`go.mod`，勿改）。
- 提交前跑 `make lint`，有报错必须修复才能提交。
- Git：`git commit -s`（Signed-off-by），不加 `Co-Authored-By`；一个 feature 一个 commit（本计划例外——每个 Task 独立提交，跟 CLAUDE.md 里"一个 feature 一次提交"的要求不冲突，因为每个 Task 本身就是一个独立可验证的小 feature）。
- 依赖前提：`lattice-shim` 的 `Server`（`docs/plans/2026-09-22-tsnet-style-server.md` 落地的产物）必须已经推送到远程，本计划 Task 1 第一步就是升级依赖。
- 本地测试环境：`latticed --standalone --config-dir .standalone-cfg`（监听 `127.0.0.1:8080`，admin 账号 `admin`/`123456`）+ 三个 docker 容器 `mac-node-a`（overlay `10.96.0.2`）、`mac-node-b`（`10.96.0.3`）、`lattice-gateway`（`10.96.0.4`），workspace `mac-demo`。运行本计划的集成测试前用 `docker ps` 确认这三个容器还在跑，`curl -s http://127.0.0.1:8080/healthz` 或直接跑一次登录请求确认控制面还活着；如果这套环境不在了，跳过集成测试步骤，只跑不依赖真实服务器的部分。

---

## Task 1: 升级 `lattice-shim` 依赖 + `EmbeddedEngine` 配置骨架

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `apple/engine/embedded/config.go`
- Test: `apple/engine/embedded/config_test.go`

**Interfaces:**
- Produces: `type Config struct { ServerURL, Token, Name string }`；`func ParseConfig(configJSON string) (Config, error)`——镜像 `apple/engine/engine.go` 里 `engineConfig`/`NewEngine` 的校验逻辑（`ServerURL`/`Token` 必填，`Name` 缺省为 `"lattice-embedded"`）。

- [ ] **Step 1: 升级 `lattice-shim` 依赖**

先确认 M0 那份计划（`lattice-shim` 的 `Server`）已经推送到远程：

```bash
cd /Users/francis/workspc/lattice-shim && git log origin/dev..dev --oneline
```

Expected: 空输出（说明 M0 的 3 个 commit 都已经推送）。如果不是空的，先去把它们推送完再继续本计划。

```bash
cd /Users/francis/workspc/lattice-shim && git rev-parse dev
```

记下这个 commit hash（下面记作 `<SHIM_COMMIT>`），然后：

```bash
cd /Users/francis/workspc/lattice
go get github.com/alatticeio/lattice-shim@<SHIM_COMMIT>
go build ./... 2>&1 | head -30
```

Expected: `go.mod`/`go.sum` 里 `lattice-shim` 的版本号更新为 `<SHIM_COMMIT>` 对应的伪版本号；`go build ./...` 无报错（这一步还没用到新代码，只是升级依赖不应该破坏任何东西）。

- [ ] **Step 2: 写失败测试 `apple/engine/embedded/config_test.go`**

```go
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

import "testing"

func TestParseConfig_RequiresServerURL(t *testing.T) {
	_, err := ParseConfig(`{"token":"lt-abc","name":"x"}`)
	if err == nil {
		t.Fatal("expected error when serverURL is missing")
	}
}

func TestParseConfig_RequiresToken(t *testing.T) {
	_, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","name":"x"}`)
	if err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestParseConfig_DefaultsName(t *testing.T) {
	cfg, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","token":"lt-abc"}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Name != "lattice-embedded" {
		t.Errorf("expected default name %q, got %q", "lattice-embedded", cfg.Name)
	}
}

func TestParseConfig_KeepsExplicitName(t *testing.T) {
	cfg, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","token":"lt-abc","name":"my-app"}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Name != "my-app" {
		t.Errorf("expected name %q, got %q", "my-app", cfg.Name)
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := ParseConfig(`not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
```

- [ ] **Step 3: 运行确认失败**

Run: `cd /Users/francis/workspc/lattice && go test ./apple/engine/embedded/... -v`
Expected: FAIL（`no Go files in .../apple/engine/embedded` 或 `undefined: ParseConfig`）

- [ ] **Step 4: 实现 `apple/engine/embedded/config.go`**

```go
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

// Package embedded is a Lattice mesh engine that runs entirely in user
// space via lattice-shim's gVisor-backed Server — no Network Extension, no
// kernel TUN device, no system VPN authorization. It is meant to be
// embedded directly into a host process (e.g. via gomobile) that wants to
// Dial/Listen on the overlay without owning a full VPN client experience.
//
// Unlike apple/engine (which bridges wireguard-go to Apple's
// NEPacketTunnelProvider via packetTUN), embedded reuses the same
// internal/agent.NewNode data plane through its CustomTUN/ProvisionerFactory
// extension point — the same one internal/agent/gvisor uses for the Linux
// AgentSandbox. No new WireGuard/gVisor bridging code is written here.
package embedded

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Config is the JSON-decoded configuration for a Config.
type Config struct {
	ServerURL string `json:"serverURL"`
	Token     string `json:"token"`
	Name      string `json:"name"`
}

// DefaultName is used when Config.Name is empty.
const DefaultName = "lattice-embedded"

// ParseConfig decodes and validates configJSON. ServerURL and Token are
// required; Name defaults to DefaultName when omitted.
func ParseConfig(configJSON string) (Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ServerURL == "" {
		return Config{}, errors.New("config: serverURL is required")
	}
	if cfg.Token == "" {
		return Config{}, errors.New("config: token is required")
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	return cfg, nil
}
```

- [ ] **Step 5: 运行确认通过**

Run: `cd /Users/francis/workspc/lattice && go test ./apple/engine/embedded/... -v`
Expected: 5 个测试全部 `PASS`

- [ ] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add go.mod go.sum apple/engine/embedded/config.go apple/engine/embedded/config_test.go
git commit -s -m "feat(apple): add embedded engine config parsing, upgrade lattice-shim"
```

---

## Task 2: `EmbeddedEngine.Start`/`Stop` — 真实注册 + 节点起来

**Files:**
- Create: `apple/engine/embedded/engine.go`
- Test: `apple/engine/embedded/engine_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Config`/`ParseConfig`；`shim.NewServer`（`lattice-shim`）；`gvisor.NewTUNAdapter`/`gvisor.InjectIntoChannel`/`gvisor.NewSandboxProvisionerFactory`（`internal/agent/gvisor`，已存在不改）；`latticeagent.RegisterSandboxViaNATSNotify`/`latticeagent.NewNode`/`latticeagent.NodeConfig`（`internal/agent`，已存在不改）。
- Produces: `type EmbeddedEngine struct{...}`；`func New(configJSON string) (*EmbeddedEngine, error)`；`func (e *EmbeddedEngine) Start(ctx context.Context) error`；`func (e *EmbeddedEngine) Stop() error`；`func (e *EmbeddedEngine) OverlayAddress() string`（启动成功后返回分配到的 overlay IP，未启动返回 `""`）。

这一步的测试要打真实的本地控制面（`http://127.0.0.1:8080`），因为节点注册/建连这部分逻辑全部在 `internal/agent` 里，没有现成的 mock，`apple/engine` 自己的测试也没有为它写过 mock——跟集成测试而不是硬造一个假的比,更符合这个包的实际定位（编排胶水代码，不是算法逻辑）。测试默认跳过，除非设了 `LATTICE_EMBED_INTEGRATION=1`，避免破坏没有这套本地环境的人的 `go test ./...`。

- [ ] **Step 1: 写失败测试 `apple/engine/embedded/engine_test.go`**

```go
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
	"net/http"
	"os"
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

// requireIntegration skips the test unless LATTICE_EMBED_INTEGRATION=1 is
// set, so `go test ./...` stays green on machines without the local
// mac-demo control plane + docker containers running.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("LATTICE_EMBED_INTEGRATION") != "1" {
		t.Skip("set LATTICE_EMBED_INTEGRATION=1 to run against the local mac-demo control plane")
	}
}

// adminToken logs into the local control plane as admin/123456 (the
// standing dev credentials for the mac-demo workspace) and returns a
// bearer token plus the mac-demo workspace ID.
func adminToken(t *testing.T) (bearer, workspaceID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "123456"})
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
		if ws.Slug == "mac-demo" {
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
	// device shows up, then wait for Start to actually return.
	deadline := time.Now().Add(15 * time.Second)
	for e.OverlayAddress() == "" && time.Now().Before(deadline) {
		approvePeer(t, bearer, workspaceID, deviceName)
		time.Sleep(500 * time.Millisecond)
	}

	select {
	case err := <-startErr:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for Start to return")
	}

	if e.OverlayAddress() == "" {
		t.Fatal("expected a non-empty overlay address after Start")
	}
	t.Logf("embedded engine got overlay address %s", e.OverlayAddress())

	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd /Users/francis/workspc/lattice && LATTICE_EMBED_INTEGRATION=1 go test ./apple/engine/embedded/... -run TestEmbeddedEngine_StartStop -v`
Expected: FAIL to compile（`undefined: New`、`undefined: EmbeddedEngine`）

- [ ] **Step 3: 实现 `apple/engine/embedded/engine.go`**

```go
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
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/alatticeio/lattice-shim/shim"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	latticeagent "github.com/alatticeio/lattice/internal/agent"
	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/gvisor"
	agentlog "github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/infra"
)

// EmbeddedEngine is a Lattice mesh node that runs entirely in user space:
// no kernel TUN, no Network Extension, no system VPN authorization. Create
// one with New, call Start once, and Dial/Listen once it is up.
type EmbeddedEngine struct {
	cfg Config

	mu      sync.Mutex
	server  *shim.Server
	node    *latticeagent.Node
	overlay string
}

// New validates configJSON and returns an EmbeddedEngine bound to it.
// configJSON: {"serverURL":"http://host:8080","token":"lt-...","name":"my-app"}
func New(configJSON string) (*EmbeddedEngine, error) {
	cfg, err := ParseConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return &EmbeddedEngine{cfg: cfg}, nil
}

// Start registers with the control plane, brings up the WireGuard data
// plane over a gVisor netstack (via lattice-shim's Server), and blocks
// until ctx is cancelled or an unrecoverable error occurs.
func (e *EmbeddedEngine) Start(ctx context.Context) error {
	agentconfig.Conf.AppId = infra.NormalizeAppID(e.cfg.Name)
	agentconfig.Conf.ServerUrl = e.cfg.ServerURL
	agentconfig.Conf.WgPort = 0

	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	peer, err := latticeagent.RegisterSandboxViaNATSNotify(ctx, e.cfg.ServerURL, e.cfg.Token, e.cfg.Name, privKey, nil)
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	if peer.Address == nil || *peer.Address == "" {
		return errors.New("enroll: server assigned no overlay address")
	}
	overlayIP := *peer.Address
	if peer.RelayURL != "" {
		agentconfig.Conf.EnableRelay = true
		if agentconfig.Conf.RelayURL == "" {
			agentconfig.Conf.RelayURL = peer.RelayURL
		}
	}

	srv, err := shim.NewServer(overlayIP, nil)
	if err != nil {
		return fmt.Errorf("netstack: %w", err)
	}
	tunDevice := gvisor.NewTUNAdapter(srv.Channel(), gvisor.InjectIntoChannel(srv.Channel()))

	node, err := latticeagent.NewNode(ctx, &latticeagent.NodeConfig{
		Logger:             agentlog.GetLogger("lattice-embedded"),
		Port:               0,
		ShowLog:            true,
		Flags:              agentconfig.Conf,
		CustomTUN:          tunDevice,
		CustomName:         e.cfg.Name,
		CurrentPeer:        peer,
		ProvisionerFactory: gvisor.NewSandboxProvisionerFactory(overlayIP, e.cfg.Name),
	})
	if err != nil {
		srv.Close()
		return fmt.Errorf("create node: %w", err)
	}

	node.GetNetworkMap = func() (*infra.Message, error) {
		return node.GetNetMap(peer.Token)
	}

	if err := node.Start(ctx); err != nil {
		srv.Close()
		return fmt.Errorf("start node: %w", err)
	}
	go node.StartHeartbeat(ctx)

	e.mu.Lock()
	e.server = srv
	e.node = node
	e.overlay = overlayIP
	e.mu.Unlock()

	<-ctx.Done()

	_ = node.Stop()
	_ = srv.Close()

	e.mu.Lock()
	e.server = nil
	e.node = nil
	e.overlay = ""
	e.mu.Unlock()

	return nil
}

// Stop is a no-op placeholder for callers that prefer an explicit method
// over cancelling the context passed to Start; Start already tears
// everything down when ctx is cancelled or done. Embedders that called
// Start with context.Background() should cancel their own context instead
// of relying on Stop to do anything — this method exists so the type has a
// symmetrical Start/Stop pair matching apple/engine's Engine.
func (e *EmbeddedEngine) Stop() error {
	return nil
}

// OverlayAddress returns this node's assigned overlay IP, or "" before
// Start has completed registration.
func (e *EmbeddedEngine) OverlayAddress() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.overlay
}
```

- [ ] **Step 4: 运行确认通过**

先确认本地测试环境还活着：

```bash
docker ps --format '{{.Names}}' | grep -E 'mac-node-a|mac-node-b|lattice-gateway'
curl -s -X POST http://127.0.0.1:8080/api/v1/users/login -H "Content-Type: application/json" -d '{"username":"admin","password":"123456"}' | head -c 200
```

Expected: 三个容器名都在；登录请求返回带 `token` 字段的 JSON。

Run: `cd /Users/francis/workspc/lattice && LATTICE_EMBED_INTEGRATION=1 go test ./apple/engine/embedded/... -run TestEmbeddedEngine_StartStop -v -timeout 60s`
Expected: `--- PASS: TestEmbeddedEngine_StartStop`，日志里能看到 `embedded engine got overlay address 10.96.0.x`

- [ ] **Step 5: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/engine/embedded/engine.go apple/engine/embedded/engine_test.go
git commit -s -m "feat(apple): embedded engine Start/Stop over lattice-shim Server, no NE"
```

---

## Task 3: `Dial`/`Listen` — 端到端跟真实容器互通

**Files:**
- Modify: `apple/engine/embedded/engine.go`
- Modify: `apple/engine/embedded/engine_test.go`

**Interfaces:**
- Consumes: Task 2 产物 `EmbeddedEngine`（含 `server *shim.Server` 字段）；`(*shim.Server).Dial`/`(*shim.Server).Listen`（`lattice-shim`，已存在）。
- Produces: `func (e *EmbeddedEngine) Dial(ctx context.Context, network, addr string) (net.Conn, error)`；`func (e *EmbeddedEngine) Listen(network, addr string) (net.Listener, error)`。两者在 `e.server == nil`（`Start` 还没完成）时返回错误。

- [ ] **Step 1: 追加失败测试到 `apple/engine/embedded/engine_test.go`**

在 import 块追加 `"io"` 和 `"os/exec"`：

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"
)
```

在文件末尾追加：

```go
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

	deadline := time.Now().Add(15 * time.Second)
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

	out, err := exec.Command("docker", "exec", "mac-node-a", "nc", "-zv", "-w", "3", e.OverlayAddress(), "9500").CombinedOutput()
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
	listenCmd := exec.Command("docker", "exec", "mac-node-a", "nc", "-l", "-p", "9501")
	if err := listenCmd.Start(); err != nil {
		t.Fatalf("start docker exec nc -l: %v", err)
	}
	defer listenCmd.Process.Kill()
	time.Sleep(1 * time.Second)

	ctx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, err := e.Dial(ctx, "tcp", "10.96.0.2:9501")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd /Users/francis/workspc/lattice && LATTICE_EMBED_INTEGRATION=1 go test ./apple/engine/embedded/... -run TestEmbeddedEngine_Listen -v`
Expected: FAIL to compile（`e.Listen undefined`）

- [ ] **Step 3: 追加 `Dial`/`Listen` 到 `apple/engine/embedded/engine.go`**

在 `OverlayAddress` 方法之后追加：

```go
// Dial dials a remote overlay address. Returns an error if Start has not
// yet completed registration.
func (e *EmbeddedEngine) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	e.mu.Lock()
	srv := e.server
	e.mu.Unlock()
	if srv == nil {
		return nil, errors.New("embedded engine not started")
	}
	return srv.Dial(ctx, network, addr)
}

// Listen creates a TCP listener on the overlay netstack. Returns an error
// if Start has not yet completed registration.
func (e *EmbeddedEngine) Listen(network, addr string) (net.Listener, error) {
	e.mu.Lock()
	srv := e.server
	e.mu.Unlock()
	if srv == nil {
		return nil, errors.New("embedded engine not started")
	}
	return srv.Listen(network, addr)
}
```

`net` 需要加进顶部 import：

```go
import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/alatticeio/lattice-shim/shim"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	latticeagent "github.com/alatticeio/lattice/internal/agent"
	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/gvisor"
	agentlog "github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/infra"
)
```

- [ ] **Step 4: 运行确认通过**

Run: `cd /Users/francis/workspc/lattice && LATTICE_EMBED_INTEGRATION=1 go test ./apple/engine/embedded/... -v -timeout 90s`
Expected: 全部 `PASS`，包括 `TestEmbeddedEngine_StartStop`、`TestEmbeddedEngine_Listen_ReachableFromContainer`、`TestEmbeddedEngine_Dial_ReachesContainer`

- [ ] **Step 5: 跑 lint**

Run: `cd /Users/francis/workspc/lattice && make lint`
Expected: 无报错。`_test.go` 文件的 `errcheck`/`unused` 按仓库约定跳过，但 `engine.go` 本身要过 lint。

- [ ] **Step 6: Commit**

```bash
cd /Users/francis/workspc/lattice
git add apple/engine/embedded/engine.go apple/engine/embedded/engine_test.go
git commit -s -m "feat(apple): embedded engine Dial/Listen, verified against mac-demo containers"
git push
```

---

## Self-Review 记录

- **Spec 覆盖**：本计划实现 `docs/superpowers/specs/2026-09-22-apple-embedded-sdk-design.md`（已修正版）里程碑 M1 的全部内容——`EmbeddedEngine` 骨架、`Start`/`Stop`、`Dial`/`Listen`，全部复用 `internal/agent`/`internal/agent/gvisor` 现成代码,没有实现 `ApplePeerManager`（修正后的设计已明确不需要）。M2（gomobile bind）、M3（Swift Package）不在本计划范围。
- **占位符扫描**：无 TBD/TODO；所有步骤含完整代码；`NodeConfig`/`RegisterSandboxViaNATSNotify`/`NewTUNAdapter`/`InjectIntoChannel`/`NewSandboxProvisionerFactory` 的签名均已对照 `internal/agent/node.go`、`internal/agent/sandbox_register.go`、`internal/agent/gvisor/wg_device.go`、`internal/agent/gvisor/provisioner.go` 源码核实。
- **类型一致性**：`EmbeddedEngine.Dial`/`Listen` 签名跟 `shim.Server.Dial`/`Listen`（M0 产物）逐字一致；`Config`/`ParseConfig` 跟 `apple/engine/engine.go` 的 `engineConfig`/`NewEngine` 校验逻辑对齐（`ServerURL`/`Token` 必填，`Name` 缺省），命名上用 `ServerURL`（大写 URL）而不是 `apple/engine` 里的 `ServerURL`（两者一致，未改字段名）。
- **已知限制,写进本计划但不在本计划里解决**：`agentconfig.Conf` 是包级全局可变状态，`EmbeddedEngine` 和 `apple/engine.Engine` 在同一进程里同时跑两个会互相踩——现有 NE 版本也是这个模式，本计划不新增风险,但 gomobile 化之后如果同一个宿主 App 同时想跑 NE 版本又跑嵌入式版本,需要单独设计隔离方案,记进 M2/M3 的风险清单。
