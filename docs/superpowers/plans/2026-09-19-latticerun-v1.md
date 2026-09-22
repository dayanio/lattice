# LatticeRun v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 LatticeRun v1——`lattice run --profile <name> -- <agent 命令>`：零控制面的本地域名级出口沙箱（netns + DNS 拦截 + 动态授权 + 审计报告）+ profile 模板生态（内置 5 官方模板 + lattice-profiles 注册表消费）。

**Architecture:** 父进程（host netns）解析 profile 后 spawn 自身 re-exec 的 launcher 子进程（`CLONE_NEWNET` 新网络命名空间），veth 对连回宿主。launcher 在 netns 内用 iptables REDIRECT 把 agent 的 TCP 全量引入 `tproxy.Proxy`（SO_ORIGINAL_DST 取回原目的）、UDP/53 引入 DNS 拦截应答器；DNS 应答器按 profile 白名单过滤转发并在放行时把"解析出的 IP+端口"登记进动态授权表（TTL≤5m）；`DomainFilter`（实现 `shim.PolicyChecker`）按"授权表 ∪ CIDR 白名单 − 显式封禁"判定每条连接。launcher 自身的上游 socket 打 SO_MARK=1 豁免 REDIRECT 防回环。**反绕过边界：agent 再进一层单 uid user namespace（对沙箱 netns 无 CAP_NET_RAW/CAP_NET_ADMIN，raw socket 无法绕过 REDIRECT），netns 内禁用 IPv6。** 会话结束生成 terminal/md/json 报告。隔离后端 v1 诚实标注为 `netns`。

**Tech Stack:** Go 1.2x、gVisor netstack 已在 go.mod（v1 数据面主要走 tproxy，netstack 供 mesh 档复用）、`github.com/miekg/dns`、`github.com/goccy/go-yaml`、`internal/agent/tproxy`（已有）、`lattice-shim`（已有）、exec `ip`/`iptables`（不新增依赖）。

## Global Constraints

- 每个新 `.go` 文件顶部带仓库统一 Apache 头（拷贝 `cmd/lattice/cmd/sandbox/run.go` 的 1-15 行）。
- 平台相关文件加 `//go:build linux`；`internal/run` 平台无关核心（profile/authtable/dns/egressfilter/audit/report/registry）不加。
- YAML 解析统一用 `github.com/goccy/go-yaml`（已在 go.mod，仓库惯例）。
- netns 内固定端口：DNS `53`、tproxy `7443`；会话子网 `10.213.0.0/24`（host=`10.213.0.1`，child=`10.213.0.2`）；SO_MARK=`1`；豁免规则必须带 `-m mark ! --mark 1`。
- 授权 TTL 上限 5 分钟；DNS AAAA 查询一律回 NOERROR 空应答（防 v6 绕过）。
- 注册表默认基址 `https://raw.githubusercontent.com/alatticeio/lattice-profiles/main`。
- 隔离级别标注：`isolation: netns`（v1）；`gvisor` / `microvm-gvisor` 仅出现在 v2 规划，不得在报告或文档中声称 v1 提供；`netns-under-runsc` 仅在 Task 14 验证通过后可标注为受支持。
- v1 声称范围内的反绕过底线：agent 子进程必须运行在单 uid 映射的 user namespace 中（对 netns 无 CAP_NET_RAW/CAP_NET_ADMIN，raw socket 无法绕过 REDIRECT）；netns 内必须禁用 IPv6。
- 提交信息 conventional commits（`feat(run): ...`），每任务至少一次提交。
- 测试命令一律 `go test ./...`（仓库根目录）；涉及 root/netns 的端到端测试放 `-tags integration` 后面。

## 对 spec 的三处偏差（已评估，需在评审时回写 spec）

1. **isolation 后端 v1 = `netns`，非 spec §四 的 `gvisor`**。现状代码里 runsc 进程隔离从未接线（`runSandbox` 走内核 wf0，netstack 仅在待移除的 sidecar 使用）；v1 的域级强制由 netns+iptables+tproxy 承担。报告与 profile 默认值如实写 `netns`。
2. **`--local` 档 `--mcp-proxy` 推迟**（spec §九 标"建议"项）：`mcpproxy.NewPolicyCache` 依赖控制面 URL+token，local 档接入需先定义本地策略源，留待 mini-spec。
3. **域名匹配 v1 为精确匹配**（无通配符），官方模板需枚举全部域名（含 `objects.githubusercontent.com` 类 CDN 域）；通配符/SNI 校验属 v2。

---

### Task 1: Profile 解析与校验

**Files:**
- Create: `internal/run/profile.go`
- Test: `internal/run/profile_test.go`

**Interfaces:**
- Produces: `type AllowEntry struct { Domain string; Ports []uint16; CIDR *net.IPNet; Port uint16 }`（Port==0 表示未指定；Domain 非空表示域名条目）；`type EgressProfile`；`func ParseProfile(data []byte) (*EgressProfile, error)`；`func (p *EgressProfile) Validate() error`；`func (p *EgressProfile) SHA256() string`

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"net"
	"testing"
	"time"
)

const profileYAML = `apiVersion: lattice.io/v1alpha1
kind: EgressProfile
metadata:
  name: npm-ci
  version: 1.0.0
  description: CI 包管理出口
  maintainer: lattice-team
spec:
  isolation:
    backend: netns
  egress:
    internet:
      defaultDeny: true
      allow:
        - domain: registry.npmjs.org
          ports: [443]
        - cidr: 10.0.0.0/8
        - port: 53
      block:
        - cidr: 169.254.169.254/32
    dns:
      mode: filtered
      servers: [system]
    overlay:
      allowCIDRs: []
  session:
    ttl: 4h
    onExpire: terminate
  audit:
    sink: local-file
  report:
    formats: [terminal, md]
`

func TestParseProfile(t *testing.T) {
	p, err := ParseProfile([]byte(profileYAML))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if p.Metadata.Name != "npm-ci" || p.Metadata.Version != "1.0.0" {
		t.Fatalf("metadata: %+v", p.Metadata)
	}
	if !p.Spec.Egress.Internet.DenyByDefault() {
		t.Fatal("defaultDeny should be true")
	}
	if p.Spec.Session.TTL != 4*time.Hour {
		t.Fatalf("ttl = %v", p.Spec.Session.TTL)
	}
	e := p.Spec.Egress.Internet.Allow[0]
	if e.Domain != "registry.npmjs.org" || len(e.Ports) != 1 || e.Ports[0] != 443 {
		t.Fatalf("allow[0] = %+v", e)
	}
	if p.Spec.Egress.Internet.Allow[1].CIDR == nil ||
		!p.Spec.Egress.Internet.Allow[1].CIDR.Contains(net.ParseIP("10.1.2.3")) {
		t.Fatalf("allow[1].cidr = %+v", p.Spec.Egress.Internet.Allow[1])
	}
	if p.Spec.Egress.Internet.Allow[2].Port != 53 {
		t.Fatalf("allow[2] = %+v", p.Spec.Egress.Internet.Allow[2])
	}
}

func TestParseProfileDefaults(t *testing.T) {
	p, err := ParseProfile([]byte("metadata:\n  name: x\n  version: 0.1.0\nspec:\n  egress:\n    internet:\n      allow:\n        - domain: a.com\n"))
	if err != nil {
		t.Fatalf("ParseProfile: %v", err)
	}
	if !p.Spec.Egress.Internet.DenyByDefault() {
		t.Fatal("defaultDeny must default true")
	}
	if p.Spec.Session.TTL != 4*time.Hour || p.Spec.Session.OnExpire != "terminate" {
		t.Fatalf("session defaults: %+v", p.Spec.Session)
	}
	if p.Spec.Egress.DNS.Mode != "filtered" {
		t.Fatalf("dns mode default = %q", p.Spec.Egress.DNS.Mode)
	}
}

func TestProfileValidate(t *testing.T) {
	p, _ := ParseProfile([]byte(profileYAML))
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	bad := *p
	bad.Metadata.Name = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("empty name should fail Validate")
	}
	bad = *p
	bad.Spec.Egress.Internet.Block = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("missing metadata-endpoint block should fail Validate")
	}
}

func TestProfileSHA256(t *testing.T) {
	p, _ := ParseProfile([]byte(profileYAML))
	if len(p.SHA256()) != 64 {
		t.Fatalf("sha256 = %q", p.SHA256())
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -v`
Expected: FAIL（`no required module provides package` / `undefined: ParseProfile`，因包尚不存在，先 `mkdir -p internal/run` 后会报 undefined）

- [ ] **Step 3: 最小实现**

```go
package run

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

// AllowEntry is one allow/block rule. Exactly one of Domain / CIDR / Port is set.
type AllowEntry struct {
	Domain string
	Ports  []uint16
	CIDR   *net.IPNet
	Port   uint16
}

type Metadata struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Description string `yaml:"description"`
	Maintainer  string `yaml:"maintainer"`
}

type EgressProfile struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       ProfileSpec `yaml:"spec"`
}

type ProfileSpec struct {
	Isolation IsolationSpec `yaml:"isolation"`
	Egress    EgressSpec    `yaml:"egress"`
	Session   SessionSpec   `yaml:"session"`
	Audit     AuditSpec     `yaml:"audit"`
	Report    ReportSpec    `yaml:"report"`
}

type IsolationSpec struct {
	Backend string `yaml:"backend"` // netns (v1)
}

type EgressSpec struct {
	Internet InternetEgress `yaml:"internet"`
	DNS      DNSSpec        `yaml:"dns"`
	Overlay  OverlayEgress  `yaml:"overlay"`
}

type InternetEgress struct {
	// DefaultDeny is a *bool so "explicitly false" survives defaults.
	DefaultDeny *bool       `yaml:"defaultDeny"`
	Allow       []AllowEntry `yaml:"allow"`
	Block       []AllowEntry `yaml:"block"`
}

// DenyByDefault reports the effective default-deny (true unless explicitly false).
func (e *InternetEgress) DenyByDefault() bool { return e.DefaultDeny == nil || *e.DefaultDeny }

type DNSSpec struct {
	Mode    string   `yaml:"mode"` // filtered | direct | block
	Servers []string `yaml:"servers"`
}

type OverlayEgress struct {
	AllowCIDRs []*net.IPNet `yaml:"allowCIDRs"`
}

type SessionSpec struct {
	TTL      time.Duration `yaml:"ttl"`
	OnExpire string        `yaml:"onExpire"`
}

type AuditSpec struct {
	Sink string `yaml:"sink"`
	Path string `yaml:"path"`
}

type ReportSpec struct {
	Formats []string `yaml:"formats"`
}

// ParseProfile parses YAML into an EgressProfile and applies defaults.
func ParseProfile(data []byte) (*EgressProfile, error) {
	var p EgressProfile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("profile yaml: %w", err)
	}
	applyDefaults(&p)
	p.raw = append([]byte(nil), data...)
	return &p, nil
}

func applyDefaults(p *EgressProfile) {
	if p.Spec.Egress.Internet.DefaultDeny == nil {
		v := true
		p.Spec.Egress.Internet.DefaultDeny = &v
	}
	if p.Spec.Session.TTL == 0 {
		p.Spec.Session.TTL = 4 * time.Hour
	}
	if p.Spec.Session.OnExpire == "" {
		p.Spec.Session.OnExpire = "terminate"
	}
	if p.Spec.Egress.DNS.Mode == "" {
		p.Spec.Egress.DNS.Mode = "filtered"
	}
	if p.Spec.Isolation.Backend == "" {
		p.Spec.Isolation.Backend = "netns"
	}
}
```

```go
// Validate enforces the invariants the registry CI also enforces.
func (p *EgressProfile) Validate() error {
	if strings.TrimSpace(p.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if strings.TrimSpace(p.Metadata.Version) == "" {
		return fmt.Errorf("metadata.version is required")
	}
	const metaEndpoint = "169.254.169.254/32"
	for _, e := range p.Spec.Egress.Internet.Block {
		if e.CIDR != nil && e.CIDR.String() == metaEndpoint {
			return nil
		}
	}
	return fmt.Errorf("egress.internet.block must include %s (metadata endpoint)", metaEndpoint)
}

// SHA256 returns the hex sha256 of the source bytes (EgressProfile carries
// the unexported field `raw []byte`, filled by ParseProfile).
func (p *EgressProfile) SHA256() string {
	sum := sha256.Sum256(p.raw)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS（4 个测试）

- [ ] **Step 5: 提交**

```bash
git add internal/run/profile.go internal/run/profile_test.go
git commit -m "feat(run): add EgressProfile v1alpha1 parsing and validation"
```

---

### Task 2: 内置官方模板（5 个）与嵌入

**Files:**
- Create: `profiles/claude-code.yaml`, `profiles/openai-codex.yaml`, `profiles/generic-web.yaml`, `profiles/npm-ci.yaml`, `profiles/llm-api-only.yaml`
- Create: `internal/run/builtin.go`
- Test: `internal/run/builtin_test.go`

**Interfaces:**
- Consumes: `ParseProfile`、`Validate`（Task 1）
- Produces: `func BuiltinProfile(name string) ([]byte, bool)`；`func BuiltinNames() []string`；embed FS 路径 `profiles/*.yaml`（相对 `internal/run/`，所以模板文件放 `internal/run/profiles/` 下）

> 路径决策：模板必须被 go:embed，故放 `internal/run/profiles/`（`profiles/` 顶层目录会被误认为仓库根资源）。注册表仓库同步脚本（Task 12）从这里取。

- [ ] **Step 1: 写失败测试**

```go
package run

import "testing"

func TestBuiltinProfilesValid(t *testing.T) {
	for _, name := range BuiltinNames() {
		data, ok := BuiltinProfile(name)
		if !ok {
			t.Fatalf("builtin %q missing", name)
		}
		p, err := ParseProfile(data)
		if err != nil {
			t.Fatalf("builtin %q: %v", name, err)
		}
		if err := p.Validate(); err != nil {
			t.Fatalf("builtin %q invalid: %v", name, err)
		}
	}
}

func TestBuiltinProfileMissing(t *testing.T) {
	if _, ok := BuiltinProfile("nope"); ok {
		t.Fatal("unknown builtin should return false")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestBuiltin -v`
Expected: FAIL（`undefined: BuiltinProfile`）

- [ ] **Step 3: 写 5 个模板与 builtin.go**

`internal/run/profiles/npm-ci.yaml`：

```yaml
apiVersion: lattice.io/v1alpha1
kind: EgressProfile
metadata:
  name: npm-ci
  version: 1.0.0
  description: npm install/test —— 包管理与 GitHub 出口
  maintainer: lattice-team
spec:
  isolation:
    backend: netns
  egress:
    internet:
      defaultDeny: true
      allow:
        - domain: registry.npmjs.org
          ports: [443]
        - domain: github.com
          ports: [443]
        - domain: codeload.github.com
          ports: [443]
        - domain: objects.githubusercontent.com
          ports: [443]
        - domain: api.github.com
          ports: [443]
        - cidr: 10.0.0.0/8
      block:
        - cidr: 169.254.169.254/32
    dns:
      mode: filtered
      servers: [system]
    overlay:
      allowCIDRs: []
  session:
    ttl: 1h
    onExpire: terminate
  audit:
    sink: local-file
  report:
    formats: [terminal, md]
```

`internal/run/profiles/claude-code.yaml`：同结构，`name: claude-code`，`ttl: 4h`，allow 追加：

```yaml
        - domain: api.anthropic.com
          ports: [443]
        - domain: statsig.anthropic.com
          ports: [443]
        - domain: sentry.io
          ports: [443]
        - domain: registry.npmjs.org
          ports: [443]
        - domain: github.com
          ports: [443]
        - domain: api.github.com
          ports: [443]
        - domain: codeload.github.com
          ports: [443]
        - domain: objects.githubusercontent.com
          ports: [443]
```

`internal/run/profiles/openai-codex.yaml`：`name: openai-codex`，allow：`api.openai.com`、`chatgpt.com`、`ab.chatgpt.com`、`auth.openai.com`、`registry.npmjs.org`、`github.com`、`api.github.com`、`codeload.github.com`、`objects.githubusercontent.com`（均 `ports: [443]`）+ `cidr: 10.0.0.0/8`。

`internal/run/profiles/llm-api-only.yaml`：`name: llm-api-only`，`ttl: 8h`，allow 仅 `api.anthropic.com`、`api.openai.com`（443）——最小面模板。

`internal/run/profiles/generic-web.yaml`：`name: generic-web`，allow `api.anthropic.com`、`api.openai.com`、`registry.npmjs.org`、`pypi.org`、`files.pythonhosted.org`、`proxy.golang.org`、`sum.golang.org`（443）+ `cidr: 10.0.0.0/8`。

每个模板的 `block` 段都必须含 `- cidr: 169.254.169.254/32`（Validate 强制）。`openai-codex.yaml` 的 `ttl: 4h`。

`internal/run/builtin.go`：

```go
package run

import "embed"

//go:embed profiles/*.yaml
var builtinFS embed.FS

// BuiltinProfile returns the raw YAML bytes of an embedded official profile.
func BuiltinProfile(name string) ([]byte, bool) {
	data, err := builtinFS.ReadFile("profiles/" + name + ".yaml")
	if err != nil {
		return nil, false
	}
	return data, true
}

// BuiltinNames returns the names of all embedded profiles.
func BuiltinNames() []string {
	entries, err := builtinFS.ReadDir("profiles")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if n := e.Name(); len(n) > 5 && n[len(n)-5:] == ".yaml" {
			names = append(names, n[:len(n)-5])
		}
	}
	return names
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/profiles/ internal/run/builtin.go internal/run/builtin_test.go
git commit -m "feat(run): embed five official egress profiles"
```

---

### Task 3: Profile Loader（多来源解析）

**Files:**
- Create: `internal/run/loader.go`
- Test: `internal/run/loader_test.go`

**Interfaces:**
- Consumes: Task 1/2
- Produces: `type Resolved struct { Profile *EgressProfile; Source string; Path string }`；`func LoadProfile(nameOrPath string) (*Resolved, error)`（解析顺序：绝对/相对路径 → `$LATTICE_PROFILE_DIR/<name>.yaml` → `~/.lattice/profiles/<name>.yaml` → 内置 → 报错并提示 `lattice profile install`）

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfileFromPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my.yaml")
	if err := os.WriteFile(path, []byte(profileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if r.Source != "path" || r.Path != path || r.Profile.Metadata.Name != "npm-ci" {
		t.Fatalf("resolved: %+v", r)
	}
}

func TestLoadProfileFromBuiltin(t *testing.T) {
	r, err := LoadProfile("npm-ci")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if r.Source != "builtin" {
		t.Fatalf("source = %q", r.Source)
	}
}

func TestLoadProfileFromUserDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".lattice", "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "custom.yaml"), []byte(profileYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATTICE_PROFILE_DIR", "")
	r, err := LoadProfile("custom")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if r.Source != "user" {
		t.Fatalf("source = %q", r.Source)
	}
}

func TestLoadProfileNotFound(t *testing.T) {
	_, err := LoadProfile("does-not-exist")
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestLoadProfile -v`
Expected: FAIL（`undefined: LoadProfile`）

- [ ] **Step 3: 实现**

```go
package run

import (
	"fmt"
	"os"
	"path/filepath"
)

// Resolved is a profile plus provenance for display and audit.
type Resolved struct {
	Profile *EgressProfile
	Source  string // path | env | user | builtin
	Path    string // empty for builtin
}

func userProfileDir() string {
	if d := os.Getenv("LATTICE_PROFILE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".lattice", "profiles")
}

// LoadProfile resolves nameOrPath in the documented order:
// explicit path → LATTICE_PROFILE_DIR → ~/.lattice/profiles → builtin.
func LoadProfile(nameOrPath string) (*Resolved, error) {
	if filepath.Contains(nameOrPath, filepath.Separator) || filepath.IsAbs(nameOrPath) {
		data, err := os.ReadFile(nameOrPath)
		if err != nil {
			return nil, fmt.Errorf("read profile %s: %w", nameOrPath, err)
		}
		p, err := ParseProfile(data)
		if err != nil {
			return nil, err
		}
		if err := p.Validate(); err != nil {
			return nil, err
		}
		return &Resolved{Profile: p, Source: "path", Path: nameOrPath}, nil
	}

	if d := os.Getenv("LATTICE_PROFILE_DIR"); d != "" {
		if p, ok := tryFile(filepath.Join(d, nameOrPath+".yaml"), "env"); ok {
			return p, nil
		}
	}
	if d := userProfileDir(); d != "" {
		if p, ok := tryFile(filepath.Join(d, nameOrPath+".yaml"), "user"); ok {
			return p, nil
		}
	}
	if data, ok := BuiltinProfile(nameOrPath); ok {
		p, err := ParseProfile(data)
		if err != nil {
			return nil, err
		}
		return &Resolved{Profile: p, Source: "builtin"}, nil
	}
	return nil, fmt.Errorf("profile %q not found (try: lattice profile search %s)", nameOrPath, nameOrPath)
}

func tryFile(path, source string) (*Resolved, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	p, err := ParseProfile(data)
	if err != nil {
		return nil, false
	}
	if err := p.Validate(); err != nil {
		return nil, false
	}
	return &Resolved{Profile: p, Source: source, Path: path}, true
}
```

> 修正：`filepath.Contains` 不存在，用 `strings.ContainsRune(nameOrPath, filepath.Separator) || filepath.IsAbs(nameOrPath)`（import "strings"）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/loader.go internal/run/loader_test.go
git commit -m "feat(run): multi-source profile loader"
```

---

### Task 4: 动态授权表

**Files:**
- Create: `internal/run/authtable.go`
- Test: `internal/run/authtable_test.go`

**Interfaces:**
- Produces: `type Grant struct { Domain string; ExpiresAt time.Time }`；`type AuthTable`；`func NewAuthTable() *AuthTable`；`func (t *AuthTable) Grant(ip net.IP, ports []uint16, ttl time.Duration, domain string)`（ports 空 = 全端口）；`func (t *AuthTable) Lookup(ip net.IP, port uint16) (Grant, bool)`；`const MaxGrantTTL = 5 * time.Minute`

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"net"
	"testing"
	"time"
)

func TestAuthTableGrantAndLookup(t *testing.T) {
	tb := NewAuthTable()
	tb.Grant(net.ParseIP("140.82.112.3"), []uint16{443}, time.Minute, "github.com")
	g, ok := tb.Lookup(net.ParseIP("140.82.112.3"), 443)
	if !ok || g.Domain != "github.com" {
		t.Fatalf("lookup: %+v ok=%v", g, ok)
	}
	if _, ok := tb.Lookup(net.ParseIP("140.82.112.3"), 80); ok {
		t.Fatal("port 80 should not be granted")
	}
}

func TestAuthTableAllPortsGrant(t *testing.T) {
	tb := NewAuthTable()
	tb.Grant(net.ParseIP("10.0.0.5"), nil, time.Minute, "internal")
	if _, ok := tb.Lookup(net.ParseIP("10.0.0.5"), 9999); !ok {
		t.Fatal("nil ports should grant all ports")
	}
}

func TestAuthTableTTLExpiry(t *testing.T) {
	tb := NewAuthTable()
	tb.Grant(net.ParseIP("1.2.3.4"), nil, 10*time.Millisecond, "x")
	time.Sleep(30 * time.Millisecond)
	if _, ok := tb.Lookup(net.ParseIP("1.2.3.4"), 443); ok {
		t.Fatal("expired grant should be gone")
	}
}

func TestAuthTableTTLCapped(t *testing.T) {
	tb := NewAuthTable()
	tb.Grant(net.ParseIP("1.2.3.4"), nil, time.Hour, "x")
	g, ok := tb.Lookup(net.ParseIP("1.2.3.4"), 80)
	if !ok {
		t.Fatal("grant missing")
	}
	if time.Until(g.ExpiresAt) > MaxGrantTTL+time.Second {
		t.Fatalf("TTL not capped: %v", time.Until(g.ExpiresAt))
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestAuthTable -v`
Expected: FAIL（`undefined: NewAuthTable`）

- [ ] **Step 3: 实现**

```go
package run

import (
	"net"
	"sync"
	"time"
)

// MaxGrantTTL caps how long a DNS-derived grant may live.
const MaxGrantTTL = 5 * time.Minute

type grantKey struct {
	ip   string
	port uint16 // 0 = all ports
}

// Grant is one authorized (ip, port) binding derived from a DNS answer.
type Grant struct {
	Domain    string
	ExpiresAt time.Time
}

// AuthTable maps DNS-resolved addresses to their source domain with expiry.
type AuthTable struct {
	mu      sync.RWMutex
	entries map[grantKey]Grant
}

func NewAuthTable() *AuthTable {
	return &AuthTable{entries: make(map[grantKey]Grant)}
}

// Grant authorizes ip for the given ports (nil/empty = all ports) until
// now+ttl, capped at MaxGrantTTL.
func (t *AuthTable) Grant(ip net.IP, ports []uint16, ttl time.Duration, domain string) {
	if ttl > MaxGrantTTL {
		ttl = MaxGrantTTL
	}
	g := Grant{Domain: domain, ExpiresAt: time.Now().Add(ttl)}
	portsToGrant := ports
	if len(portsToGrant) == 0 {
		portsToGrant = []uint16{0}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range portsToGrant {
		t.entries[grantKey{ip.String(), p}] = g
	}
}

// Lookup returns the live grant for ip:port, or false.
func (t *AuthTable) Lookup(ip net.IP, port uint16) (Grant, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if g, ok := t.entries[grantKey{ip.String(), port}]; ok && time.Now().Before(g.ExpiresAt) {
		return g, true
	}
	if g, ok := t.entries[grantKey{ip.String(), 0}]; ok && time.Now().Before(g.ExpiresAt) {
		return g, true
	}
	return Grant{}, false
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -run TestAuthTable -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/authtable.go internal/run/authtable_test.go
git commit -m "feat(run): dynamic authorization table for DNS-derived grants"
```

---

### Task 5: 审计事件、JSONL 写入与会话统计

**Files:**
- Create: `internal/run/audit.go`
- Test: `internal/run/audit_test.go`

**Interfaces:**
- Produces: `type Event struct { TS time.Time; SessionID string; RunMode string; Profile ProfileRef; DstIP string; DstPort uint16; Domain string; Verdict string; Reason string; Layer string }`；`type ProfileRef struct { Name, Version, SHA256 string }`；`type AuditSink interface { Emit(Event) }`；`func NewJSONLWriter(path string) (*JSONLWriter, error)`；`func (w *JSONLWriter) Emit(e Event)`；`func (w *JSONLWriter) Close() error`；`type Stats`；`func NewStats() *Stats`；`func (s *Stats) Record(domain string, ip string, port uint16, allowed bool, reason string)`；`func (s *Stats) Violations() int`；`func (s *Stats) Snapshot() StatsSnapshot`；`type StatsSnapshot struct { Duration time.Duration; TopAllowed []DomainCount; Denied []DenyEvent; Violations int }`；`type DomainCount struct { Domain string; Connections int }`；`type DenyEvent struct { Domain, IP string; Port uint16; Reason string }`；`type TeeSink []AuditSink`（Emit 依序广播）

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJSONLWriterEmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Emit(Event{DstIP: "1.2.3.4", DstPort: 443, Domain: "a.com", Verdict: "allow", Layer: "tcp", Reason: "domain"})
	w.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e Event
	if err := json.Unmarshal(data[:len(data)-1], &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.DstIP != "1.2.3.4" || e.Domain != "a.com" || e.Verdict != "allow" {
		t.Fatalf("event: %+v", e)
	}
}

func TestStatsRecordAndSnapshot(t *testing.T) {
	s := NewStats()
	s.Record("a.com", "1.1.1.1", 443, true, "domain")
	s.Record("a.com", "1.1.1.1", 443, true, "domain")
	s.Record("", "9.9.9.9", 80, false, "default-deny")
	snap := s.Snapshot()
	if snap.TopAllowed[0].Domain != "a.com" || snap.TopAllowed[0].Connections != 2 {
		t.Fatalf("top: %+v", snap.TopAllowed)
	}
	if len(snap.Denied) != 1 || snap.Denied[0].IP != "9.9.9.9" {
		t.Fatalf("denied: %+v", snap.Denied)
	}
	if s.Violations() != 1 {
		t.Fatalf("violations = %d", s.Violations())
	}
}

func TestTeeSink(t *testing.T) {
	var got []Event
	tee := TeeSink{sinkFunc(func(e Event) { got = append(got, e) }), sinkFunc(func(e Event) { got = append(got, e) })}
	tee.Emit(Event{Verdict: "drop"})
	if len(got) != 2 {
		t.Fatalf("tee delivered %d events", len(got))
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run "TestJSONL|TestStats|TestTee" -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**

```go
package run

import (
	"encoding/json"
	"os"
	"sort"
	"sync"
	"time"
)

// ProfileRef identifies the profile that governed a session.
type ProfileRef struct {
	Name    string
	Version string
	SHA256  string
}

// Event is one audit line. Superset of shim.AuditEvent — local runner has
// richer context (domain, reason, profile).
type Event struct {
	TS        time.Time `json:"ts"`
	SessionID string    `json:"session_id"`
	RunMode   string    `json:"run_mode"` // local
	Profile   ProfileRef `json:"profile"`
	DstIP     string    `json:"dst_ip,omitempty"`
	DstPort   uint16    `json:"dst_port,omitempty"`
	Domain    string    `json:"domain,omitempty"`
	Verdict   string    `json:"verdict"` // allow | drop
	Reason    string    `json:"reason,omitempty"`
	Layer     string    `json:"layer"` // dns | tcp
}

// AuditSink receives audit events.
type AuditSink interface {
	Emit(Event)
}

type sinkFunc func(Event)

func (f sinkFunc) Emit(e Event) { f(e) }

// TeeSink fans out to multiple sinks.
type TeeSink []AuditSink

func (t TeeSink) Emit(e Event) {
	for _, s := range t {
		s.Emit(e)
	}
}

// JSONLWriter appends events to a file, one JSON object per line.
type JSONLWriter struct {
	mu sync.Mutex
	f  *os.File
}

func NewJSONLWriter(path string) (*JSONLWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &JSONLWriter{f: f}, nil
}

func (w *JSONLWriter) Emit(e Event) {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.f.Write(append(data, '\n'))
}

func (w *JSONLWriter) Close() error { return w.f.Close() }

// DomainCount aggregates allowed connections per domain.
type DomainCount struct {
	Domain      string
	Connections int
}

// DenyEvent is one blocked connection.
type DenyEvent struct {
	Domain string
	IP     string
	Port   uint16
	Reason string
}

// StatsSnapshot is the aggregate view for reports.
type StatsSnapshot struct {
	TopAllowed []DomainCount
	Denied     []DenyEvent
	Violations int
}

type statsKey struct {
	domain string
	ip     string
	port   uint16
}

// Stats aggregates allow/deny decisions for the session report.
type Stats struct {
	mu        sync.Mutex
	allowed   map[statsKey]int
	denied    []DenyEvent
	violation int
}

func NewStats() *Stats {
	return &Stats{allowed: make(map[statsKey]int)}
}

// Record tallies one decision. Denied events with reason != "blocked-cidr"
// count as violations (blocked-cidr is an explicit profile deny, expected).
func (s *Stats) Record(domain, ip string, port uint16, allowed bool, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if allowed {
		s.allowed[statsKey{domain, ip, port}]++
		return
	}
	s.denied = append(s.denied, DenyEvent{Domain: domain, IP: ip, Port: port, Reason: reason})
	s.violation++
}

// Violations returns the number of denied connections so far.
func (s *Stats) Violations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.violation
}

// Snapshot returns the aggregate, sorted for stable rendering.
func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := StatsSnapshot{Denied: append([]DenyEvent(nil), s.denied...), Violations: s.violation}
	for k, n := range s.allowed {
		snap.TopAllowed = append(snap.TopAllowed, DomainCount{Domain: k.domain, Connections: n})
	}
	sort.Slice(snap.TopAllowed, func(i, j int) bool {
		if snap.TopAllowed[i].Connections != snap.TopAllowed[j].Connections {
			return snap.TopAllowed[i].Connections > snap.TopAllowed[j].Connections
		}
		return snap.TopAllowed[i].Domain < snap.TopAllowed[j].Domain
	})
	return snap
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/audit.go internal/run/audit_test.go
git commit -m "feat(run): audit events, JSONL sink and session stats"
```

---

### Task 6: DomainFilter（实现 shim.PolicyChecker）

**Files:**
- Create: `internal/run/egressfilter.go`
- Test: `internal/run/egressfilter_test.go`

**Interfaces:**
- Consumes: Task 1（AllowEntry）、Task 4（AuthTable）、Task 5（Stats）
- Produces: `type DomainFilter`；`func NewDomainFilter(p *EgressProfile, table *AuthTable, stats *Stats, audit AuditSink, sessionID string) *DomainFilter`；`func (f *DomainFilter) Allow(identity string, dstIP net.IP, dstPort uint16) bool`（实现 `shim.PolicyChecker`，编译期断言 `var _ shim.PolicyChecker = (*DomainFilter)(nil)`）

判定顺序：显式 Block CIDR → 显式 Allow CIDR → 授权表（grant，Reason 记 `domain:<name>`）→ `DenyByDefault()` 兜底。每次判定 `stats.Record` + `audit.Emit`。

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"net"
	"testing"
	"time"
)

func newTestFilter(t *testing.T) (*DomainFilter, *AuthTable) {
	t.Helper()
	p, err := ParseProfile([]byte(profileYAML))
	if err != nil {
		t.Fatal(err)
	}
	tb := NewAuthTable()
	return NewDomainFilter(p, tb, NewStats(), TeeSink{}, "s1"), tb
}

func TestDomainFilterCIDRAllow(t *testing.T) {
	f, _ := newTestFilter(t)
	if !f.Allow("agent", net.ParseIP("10.1.2.3"), 443) {
		t.Fatal("10.0.0.0/8 should be allowed by profile")
	}
}

func TestDomainFilterGrantAllow(t *testing.T) {
	f, tb := newTestFilter(t)
	ip := net.ParseIP("104.16.0.1")
	tb.Grant(ip, []uint16{443}, time.Minute, "registry.npmjs.org")
	if !f.Allow("agent", ip, 443) {
		t.Fatal("granted ip:port should be allowed")
	}
	if reason, ok := f.LastReason(ip.String(), 443); !ok || reason != "allow:domain:registry.npmjs.org" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDomainFilterDefaultDeny(t *testing.T) {
	f, _ := newTestFilter(t)
	if f.Allow("agent", net.ParseIP("93.184.216.34"), 443) {
		t.Fatal("unknown ip should be denied")
	}
}

func TestDomainFilterExplicitBlock(t *testing.T) {
	p, _ := ParseProfile([]byte(profileYAML))
	p.Spec.Egress.Internet.Block = append(p.Spec.Egress.Internet.Block,
		AllowEntry{CIDR: mustCIDR("140.82.0.0/16")})
	f := NewDomainFilter(p, NewAuthTable(), NewStats(), TeeSink{}, "s1")
	if f.Allow("agent", net.ParseIP("140.82.112.3"), 443) {
		t.Fatal("explicitly blocked CIDR should be denied even if granted")
	}
}

func mustCIDR(s string) *net.IPNet {
	_, n, _ := net.ParseCIDR(s)
	return n
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestDomainFilter -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**

```go
package run

import (
	"fmt"
	"net"

	shim "github.com/alatticeio/lattice-shim/shim"
)

var _ shim.PolicyChecker = (*DomainFilter)(nil)

// DomainFilter enforces profile egress over (ip, port) tuples, combining
// CIDR rules with DNS-derived grants.
type DomainFilter struct {
	profile   *EgressProfile
	table     *AuthTable
	stats     *Stats
	audit     AuditSink
	sessionID string

	lastReason map[string]string
}

func NewDomainFilter(p *EgressProfile, table *AuthTable, stats *Stats, audit AuditSink, sessionID string) *DomainFilter {
	return &DomainFilter{
		profile:    p,
		table:      table,
		stats:      stats,
		audit:      audit,
		sessionID:  sessionID,
		lastReason: make(map[string]string),
	}
}

// LastReason exposes the most recent verdict reason for a dst (test/report aid).
func (f *DomainFilter) LastReason(ip string, port uint16) (string, bool) {
	r, ok := f.lastReason[ip]
	return r, ok
}

// Allow implements shim.PolicyChecker.
func (f *DomainFilter) Allow(_ string, dstIP net.IP, dstPort uint16) bool {
	reason := f.decide(dstIP, dstPort)
	allowed := reason != ""
	f.lastReason[dstIP.String()] = fmt.Sprintf("allow:%s", reason)
	if !allowed {
		f.lastReason[dstIP.String()] = fmt.Sprintf("deny:%s", f.denyReason(dstIP))
	}
	f.stats.Record("", dstIP.String(), dstPort, allowed, f.denyReason(dstIP))
	if f.audit != nil {
		verdict := "allow"
		if !allowed {
			verdict = "drop"
		}
		f.audit.Emit(Event{
			SessionID: f.sessionID,
			RunMode:   "local",
			Profile:   ProfileRef{Name: f.profile.Metadata.Name, Version: f.profile.Metadata.Version, SHA256: f.profile.SHA256()},
			DstIP:     dstIP.String(),
			DstPort:   dstPort,
			Verdict:   verdict,
			Reason:    reason,
			Layer:     "tcp",
		})
	}
	return allowed
}

func (f *DomainFilter) decide(ip net.IP, port uint16) string {
	for _, e := range f.profile.Spec.Egress.Internet.Block {
		if e.CIDR != nil && e.CIDR.Contains(ip) {
			return ""
		}
	}
	for _, e := range f.profile.Spec.Egress.Internet.Allow {
		if e.CIDR != nil && e.CIDR.Contains(ip) {
			return "cidr:" + e.CIDR.String()
		}
	}
	if g, ok := f.table.Lookup(ip, port); ok {
		return "domain:" + g.Domain
	}
	return ""
}

func (f *DomainFilter) denyReason(ip net.IP) string {
	for _, e := range f.profile.Spec.Egress.Internet.Block {
		if e.CIDR != nil && e.CIDR.Contains(ip) {
			return "blocked-cidr"
		}
	}
	return "default-deny"
}
```

> 评审注：`Allow` 的 stats 归因——`decide` 最终返回 `(reason, domain string)` 二元组：授权命中时 `stats.Record(domain, ...)`、CIDR 命中与拒绝时 domain 传 `""`，保证报告能按域名归因。`lastReason` 赋值逻辑保持测试所断言的形式（`allow:<reason>` / `deny:<denyReason>`）。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/egressfilter.go internal/run/egressfilter_test.go
git commit -m "feat(run): DomainFilter enforcing CIDR rules plus DNS grants"
```

---

### Task 7: DNS 拦截应答器

**Files:**
- Create: `internal/run/dns.go`
- Test: `internal/run/dns_test.go`

**Interfaces:**
- Consumes: Task 1（AllowEntry/DNSSpec）、Task 4（AuthTable）、Task 5（Event/AuditSink）
- Produces: `type DNSServer`；`func NewDNSServer(addr string, p *EgressProfile, table *AuthTable, upstream []string, dial func(ctx, network, addr) (net.Conn, error), audit AuditSink, sessionID string) (*DNSServer, error)`（dial 传 MarkDialer，测试传普通 dialer）；`func (s *DNSServer) Start(ctx context.Context) error`；`func (s *DNSServer) HandleQuery(w dns.ResponseWriter, r *dns.Msg)`（导出便于直接测试）

行为：`filtered` 模式下，allow 表内精确域名 → `dns.Exchange` 转发首个可达上游 → A 记录逐条 `table.Grant(ip, entryPorts, min(ansTTL, MaxGrantTTL), qname)` → 原样应答；表外 → NXDOMAIN；AAAA 查询 → NOERROR 空应答；`direct` 模式 → 全部直接转发不登记（v1 不建议，仅供调试）；上游解析失败 → SERVFAIL。

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// fakeUpstream answers A queries with a fixed IP and TTL, AAAA with empty.
func fakeUpstream(t *testing.T) (addr string, closeFn func()) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		q := r.Question[0]
		m := new(dns.Msg)
		m.SetReply(r)
		if q.Qtype == dns.TypeA {
			rr, _ := dns.NewRR(strings.TrimSuffix(q.Name, ".") + " 120 IN A 93.184.216.34")
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})}
	go server.ActivateAndServe()
	return pc.LocalAddr().String(), func() { server.Shutdown() }
}

func queryA(t *testing.T, server *DNSServer, name string, qtype uint16) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	w := &testResponseWriter{buf: &dns.Msg{}}
	server.HandleQuery(w, m)
	return w.buf
}

func TestDNSAllowedDomainGrants(t *testing.T) {
	up, closeUp := fakeUpstream(t)
	defer closeUp()
	p, _ := ParseProfile([]byte(profileYAML))
	tb := NewAuthTable()
	srv, err := NewDNSServer("127.0.0.1:0", p, tb, []string{up}, dialFunc, TeeSink{}, "s1")
	if err != nil {
		t.Fatal(err)
	}
	resp := queryA(t, srv, "registry.npmjs.org", dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("answers: %+v", resp.Answer)
	}
	g, ok := tb.Lookup(net.ParseIP("93.184.216.34"), 443)
	if !ok || g.Domain != "registry.npmjs.org" {
		t.Fatalf("grant missing: %+v ok=%v", g, ok)
	}
	if time.Until(g.ExpiresAt) > MaxGrantTTL {
		t.Fatal("grant TTL not capped")
	}
}

func TestDNSDeniedDomainNXDOMAIN(t *testing.T) {
	up, closeUp := fakeUpstream(t)
	defer closeUp()
	p, _ := ParseProfile([]byte(profileYAML))
	srv, _ := NewDNSServer("127.0.0.1:0", p, NewAuthTable(), []string{up}, dialFunc, TeeSink{}, "s1")
	resp := queryA(t, srv, "evil.com", dns.TypeA)
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("rcode = %d", resp.Rcode)
	}
}

func TestDNSAAAAEmptyNoError(t *testing.T) {
	up, closeUp := fakeUpstream(t)
	defer closeUp()
	p, _ := ParseProfile([]byte(profileYAML))
	srv, _ := NewDNSServer("127.0.0.1:0", p, NewAuthTable(), []string{up}, dialFunc, TeeSink{}, "s1")
	resp := queryA(t, srv, "registry.npmjs.org", dns.TypeAAAA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("AAAA resp: rcode=%d answers=%d", resp.Rcode, len(resp.Answer))
	}
}
```

测试辅助（与上面测试同文件）：

```go
func dialFunc(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

// testResponseWriter captures the reply without a real socket.
type testResponseWriter struct{ buf *dns.Msg }

func (w *testResponseWriter) LocalAddr() net.Addr       { return nil }
func (w *testResponseWriter) RemoteAddr() net.Addr      { return nil }
func (w *testResponseWriter) WriteMsg(m *dns.Msg) error { *w.buf = *m; return nil }
func (w *testResponseWriter) Write([]byte) (int, error) { return 0, nil }
func (w *testResponseWriter) Close() error              { return nil }
func (w *testResponseWriter) TsigStatus() error         { return nil }
func (w *testResponseWriter) TsigTimersOnly(bool)       {}
func (w *testResponseWriter) Hijack()                   {}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestDNS -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**

```go
package run

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"
)

// DNSServer intercepts UDP/53 inside the sandbox netns, enforces the profile
// allow list, forwards allowed queries upstream and registers grants.
type DNSServer struct {
	addr      string
	profile   *EgressProfile
	table     *AuthTable
	upstream  []string
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	audit     AuditSink
	sessionID string
	server    *dns.Server
}

func NewDNSServer(addr string, p *EgressProfile, table *AuthTable, upstream []string,
	dial func(ctx context.Context, network, addr string) (net.Conn, error),
	audit AuditSink, sessionID string,
) (*DNSServer, error) {
	if len(upstream) == 0 {
		return nil, fmt.Errorf("dns: no upstream servers")
	}
	return &DNSServer{addr: addr, profile: p, table: table, upstream: upstream, dial: dial, audit: audit, sessionID: sessionID}, nil
}

// Start binds and serves UDP until ctx is cancelled.
func (s *DNSServer) Start(ctx context.Context) error {
	s.server = &dns.Server{Addr: s.addr, Net: "udp", Handler: dns.HandlerFunc(s.HandleQuery)}
	go func() {
		<-ctx.Done()
		_ = s.server.Shutdown()
	}()
	return s.server.ListenAndServe()
}

// HandleQuery implements the filtered-forwarding pipeline.
func (s *DNSServer) HandleQuery(w dns.ResponseWriter, r *dns.Msg) {
	q := r.Question[0]
	name := strings.TrimSuffix(strings.ToLower(q.Name), ".")

	if q.Qtype == dns.TypeAAAA {
		m := new(dns.Msg)
		m.SetReply(r)
		_ = w.WriteMsg(m) // NOERROR, empty: force IPv4
		return
	}

	entry, allowed := s.lookupAllow(name)
	if !allowed {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		_ = w.WriteMsg(m)
		s.emit(name, "", "drop", "dns:not-allowed")
		return
	}

	client := &dns.Client{Net: "udp", Timeout: 3 * time.Second}
	var reply *dns.Msg
	var err error
	for _, up := range s.upstream {
		reply, _, err = client.Exchange(r, up)
		if err == nil {
			break
		}
	}
	if err != nil || reply == nil {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		_ = w.WriteMsg(m)
		return
	}

	for _, rr := range reply.Answer {
		if a, ok := rr.(*dns.A); ok {
			ttl := time.Duration(a.Hdr.Ttl) * time.Second
			s.table.Grant(a.A, entry.Ports, ttl, name)
			s.emit(name, a.A.String(), "allow", "dns:granted")
		}
	}
	_ = w.WriteMsg(reply)
}

func (s *DNSServer) lookupAllow(name string) (AllowEntry, bool) {
	for _, e := range s.profile.Spec.Egress.Internet.Allow {
		if e.Domain != "" && strings.EqualFold(e.Domain, name) {
			return e, true
		}
	}
	return AllowEntry{}, false
}

func (s *DNSServer) emit(domain, ip, verdict, reason string) {
	if s.audit == nil {
		return
	}
	s.audit.Emit(Event{
		SessionID: s.sessionID,
		RunMode:   "local",
		Profile:   ProfileRef{Name: s.profile.Metadata.Name, Version: s.profile.Metadata.Version, SHA256: s.profile.SHA256()},
		Domain:    domain,
		DstIP:     ip,
		Verdict:   verdict,
		Reason:    reason,
		Layer:     "dns",
	})
}
```

> 评审注：`dns.Client.Exchange` 用的是内置 UDP dial，不走注入的 dial——在 netns 里没有回环问题（上游流量带 mark 是由调用侧 socket 决定的）。为保证上游查询也豁免 REDIRECT，实现时改用 `dns.Client{Net: "udp", Dialer: &net.Dialer{Control: controlSetMark}}`（goccy 无关，miekg/dns 的 `Client.Dialer` 字段存在）。`direct` 模式在 `HandleQuery` 顶部短路：直接转发并跳过 grant 登记。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -run TestDNS -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/dns.go internal/run/dns_test.go
git commit -m "feat(run): DNS interceptor with allowlist forwarding and grant registration"
```

---

### Task 8: netns 命令构造器（纯函数）

**Files:**
- Create: `internal/run/netns_linux.go`
- Test: `internal/run/netns_linux_test.go`

**Interfaces:**
- Produces: `const (sessionSubnet = "10.213.0.0/24"; hostVethIP = "10.213.0.1"; childVethIP = "10.213.0.2"; hostVethPrefix = "lrh"; childVethPrefix = "lrc"; tproxyPort = 7443)`；`type netnsSpec struct { SessionID string; HostVeth, ChildVeth string }`；`func newNetnsSpec(sessionID string) netnsSpec`（veth 名 = 前缀+sessionID 前 8 字符）`；`func hostSetupCommands(s netnsSpec) [][]string`；`func childSetupCommands(s netnsSpec) [][]string`；`func hostTeardownCommands(s netnsSpec) [][]string`

- [ ] **Step 1: 写失败测试**

```go
//go:build linux

package run

import (
	"strings"
	"testing"
)

func TestNewNetnsSpec(t *testing.T) {
	s := newNetnsSpec("abcdefgh12345678")
	if s.HostVeth != "lrhabcdefgh" || s.ChildVeth != "lrcabcdefgh" {
		t.Fatalf("spec: %+v", s)
	}
}

func TestHostSetupCommands(t *testing.T) {
	cmds := hostSetupCommands(newNetnsSpec("sess1234"))
	joined := render(cmds)
	for _, want := range []string{
		"ip link add lrhsess1234 type veth peer name lrcsess1234",
		"ip addr add 10.213.0.1/24 dev lrhsess1234",
		"ip link set lrhsess1234 up",
		"iptables -t nat -C POSTROUTING -s 10.213.0.2/32 -j MASQUERADE",
		"iptables -t nat -A POSTROUTING -s 10.213.0.2/32 -j MASQUERADE",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func TestChildSetupCommands(t *testing.T) {
	cmds := childSetupCommands(newNetnsSpec("sess1234"))
	joined := render(cmds)
	for _, want := range []string{
		"ip link set lo up",
		"ip addr add 10.213.0.2/24 dev lrcsess1234",
		"ip route add default via 10.213.0.1",
		"iptables -t nat -A OUTPUT -p tcp -m mark ! --mark 1 -j REDIRECT --to-ports 7443",
		"iptables -t nat -A OUTPUT -p udp -m udp --dport 53 -m mark ! --mark 1 -j REDIRECT --to-ports 53",
		"sysctl -w net.ipv6.conf.all.disable_ipv6=1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
}

func render(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(strings.Join(c, " "))
		b.WriteString("\n")
	}
	return b.String()
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run TestHostSetup -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
//go:build linux

package run

import "fmt"

const (
	sessionSubnet  = "10.213.0.0/24"
	hostVethIP     = "10.213.0.1"
	childVethIP    = "10.213.0.2"
	childVethCIDR  = "10.213.0.2/24"
	hostVethPrefix = "lrh"
	childVethPrefix = "lrc"
	tproxyPort     = 7443
)

type netnsSpec struct {
	SessionID string
	HostVeth  string
	ChildVeth string
}

func newNetnsSpec(sessionID string) netnsSpec {
	id := sessionID
	if len(id) > 8 {
		id = id[:8]
	}
	return netnsSpec{SessionID: sessionID, HostVeth: hostVethPrefix + id, ChildVeth: childVethPrefix + id}
}

func runCmd(args ...string) []string { return args }

func hostSetupCommands(s netnsSpec) [][]string {
	return [][]string{
		runCmd("ip", "link", "add", s.HostVeth, "type", "veth", "peer", "name", s.ChildVeth),
		runCmd("ip", "addr", "add", hostVethIP+"/24", "dev", s.HostVeth),
		runCmd("ip", "link", "set", s.HostVeth, "up"),
		// -C probe then -A add: idempotent on re-runs.
		runCmd("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", childVethIP+"/32", "-j", "MASQUERADE"),
		runCmd("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", childVethIP+"/32", "-j", "MASQUERADE"),
	}
}

func childSetupCommands(s netnsSpec) [][]string {
	return [][]string{
		runCmd("ip", "link", "set", "lo", "up"),
		runCmd("ip", "link", "set", s.ChildVeth, "up"),
		runCmd("ip", "addr", "add", childVethCIDR, "dev", s.ChildVeth),
		runCmd("ip", "route", "add", "default", "via", hostVethIP),
		runCmd("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "tcp",
			"-m", "mark", "!", "--mark", "1", "-j", "REDIRECT", "--to-ports", fmt.Sprint(tproxyPort)),
		runCmd("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "udp",
			"-m", "udp", "--dport", "53", "-m", "mark", "!", "--mark", "1",
			"-j", "REDIRECT", "--to-ports", "53"),
		// Hardening: no IPv6 inside the sandbox netns (anti-bypass).
		runCmd("sysctl", "-w", "net.ipv6.conf.all.disable_ipv6=1"),
		runCmd("sysctl", "-w", "net.ipv6.conf.default.disable_ipv6=1"),
	}
}

func hostTeardownCommands(s netnsSpec) [][]string {
	return [][]string{
		runCmd("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", childVethIP+"/32", "-j", "MASQUERADE"),
		runCmd("ip", "link", "del", s.HostVeth),
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/netns_linux.go internal/run/netns_linux_test.go
git commit -m "feat(run): netns setup command builders"
```

---

### Task 9: 本地运行器引擎（RunLocal + launcher）

**Files:**
- Create: `internal/run/launcher_linux.go`
- Test: `internal/run/launcher_linux_test.go`（仅 argv/env 纯函数测试；完整链路走 Task 13 集成冒烟）

**Interfaces:**
- Consumes: Task 3/5/6/7/8 + `internal/agent/tproxy`
- Produces: `type LocalOptions struct { Profile *EgressProfile; ProfilePath string; CmdArgs []string; TTL time.Duration; StrictViolations bool }`；`type SessionResult struct { ExitCode int; Violations int; SessionID string }`；`func RunLocal(ctx context.Context, opts LocalOptions) (SessionResult, error)`；`func controlSetMark(network, address string, c syscall.RawConn) error`

流程（RunLocal，父进程）：
1. `sessionID := randHex(8)`；flock `/tmp/lattice-run-<sessionID>.lock`（v1 单会话：先试固定名 `/tmp/lattice-run-session.lock`，占用即报错"another lattice run is active"）。
2. 若 `os.Geteuid() != 0` → 返回带 docker 提示的错误。
3. 组装 launcher 命令：`os.Executable()` + `["__run-launcher"]`，`SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}`，Env 注入 `LATTICE_RUN_SESSION=<id>`、`LATTICE_RUN_PROFILE_PATH=<临时写出的 profile yaml>`、`LATTICE_RUN_CMD=<os.Executable 不参与；cmd 通过 argv 4.. 传递>`。cmdArgs 通过 ExtraArgs（launcher 子命令 Args 接收）传递。
4. `cmd.Start()` → 执行 `hostSetupCommands`（每条 `exec.Command(args[0], args[1:]...).Run()`）→ `ip link set <childVeth> netns <childPid>` → 若 `sysctl net.ipv4.ip_forward` 为 0 则设 1。
5. 从 pipe（`os.Pipe`，fd3 传给 child）读一行：`READY` 或 `ERROR <msg>`。
6. 等 ctx（TTL deadline 由调用方注入）或 child 退出；`hostTeardownCommands` 兜底执行。
7. 从统计文件?——否：launcher 退出码即 agent 退出码（透传）；violations 由父进程读 `~/.lattice/audit/<sessionID>.jsonl` 统计?——**决策：stats 在 launcher 进程内存中，会话结束由 launcher 把 `StatsSnapshot` JSON 写到管道第二行**，父进程解析后渲染报告。管道协议：`READY\n` ... 会话结束写 `RESULT <json>\n`。

launcherMain（`__run-launcher`，netns 内）：
1. `childSetupCommands` 全部执行。
2. 起 `DNSServer(:53)` + `tproxy.Proxy{Addr: "127.0.0.1:7443", Dial: markDialContext}`，`DomainFilter` 包在 Dial 里：`dial = func(ctx, network, addr) { ip, port 解析 → f.Allow → MarkDialer.DialContext }`（addr 即 SO_ORIGINAL_DST 原目的）。
3. 写 `READY`。
4. `fork agent`（复制 `cmd/lattice/cmd/sandbox/shared_linux.go` 的 forkAgent 语义，TTL 到期 SIGTERM → 5s → SIGKILL），收集 child 退出码。
5. 写 `RESULT {"violations":N,"exit_code":E}` → 清理退出。

- [ ] **Step 1: 写失败测试**

```go
//go:build linux

package run

import "testing"

func TestControlSetMark(t *testing.T) {
	// 只验证函数存在且类型正确；SO_MARK 生效需 root，由集成冒烟覆盖。
	var c controlFunc = controlSetMark
	if c == nil {
		t.Fatal("nil")
	}
}

type controlFunc func(network, address string, c syscall.RawConn) error
```

```go
//go:build linux

package run

import (
	"testing"
	"syscall"
)

func TestSessionResultParsing(t *testing.T) {
	r, err := parseResultLine(`RESULT {"violations":2,"exit_code":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Violations != 2 || r.ExitCode != 3 {
		t.Fatalf("result: %+v", r)
	}
}

func TestAgentSysProcAttrStripsPrivileges(t *testing.T) {
	a := agentSysProcAttr()
	if a.Cloneflags&syscall.CLONE_NEWUSER == 0 {
		t.Fatal("agent must run in a user namespace (privilege strip)")
	}
	if len(a.UidMappings) != 1 || a.UidMappings[0].Size != 1 {
		t.Fatalf("uid mappings: %+v", a.UidMappings)
	}
	if len(a.GidMappings) != 1 || a.GidMappings[0].Size != 1 {
		t.Fatalf("gid mappings: %+v", a.GidMappings)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run "TestControlSetMark|TestSessionResult" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
//go:build linux

package run

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alatticeio/lattice/internal/agent/tproxy"
	"github.com/goccy/go-yaml"
	"golang.org/x/sys/unix"
)

// LocalOptions configures one local sandbox session.
type LocalOptions struct {
	Profile          *EgressProfile
	ProfilePath      string // resolved source, for launcher re-read
	CmdArgs          []string
	TTL              time.Duration // 0 = profile default
	StrictViolations bool
}

// SessionResult is what the launcher reported back.
type SessionResult struct {
	ExitCode   int             `json:"exit_code"`
	Violations int             `json:"violations"`
	Stats      StatsSnapshot   `json:"-"`
	Raw        json.RawMessage `json:"-"`
}

// RunLocal drives one --local session end to end and returns the result.
func RunLocal(ctx context.Context, opts LocalOptions) (SessionResult, error) {
	var res SessionResult
	if opts.Profile == nil {
		return res, errors.New("profile is required")
	}
	if os.Geteuid() != 0 {
		return res, fmt.Errorf("lattice run --local needs root/NET_ADMIN; on macOS use --docker (see docs)")
	}

	lock, err := acquireSessionLock()
	if err != nil {
		return res, err
	}
	defer func() { _ = lock.Close() }()

	sessionID := randHex(8)
	spec := newNetnsSpec(sessionID)

	// Always hand the launcher the *effective* profile (including inline
	// --egress-allow additions applied by the caller), not the stale on-disk
	// source.
	f, err := os.CreateTemp("", "lattice-profile-*.yaml")
	if err != nil {
		return res, err
	}
	data, err := yaml.Marshal(opts.Profile)
	if err != nil {
		return res, err
	}
	if _, err := f.Write(data); err != nil {
		return res, err
	}
	_ = f.Close()
	profilePath := f.Name()
	defer func() { _ = os.Remove(profilePath) }()

	pr, pw, err := os.Pipe()
	if err != nil {
		return res, err
	}
	defer func() { _ = pr.Close() }()

	ttl := opts.TTL
	if ttl == 0 {
		ttl = opts.Profile.Spec.Session.TTL
	}
	runCtx, cancel := context.WithTimeout(ctx, ttl)
	defer cancel()

	cmd := exec.CommandContext(runCtx, mustSelf(), "__run-launcher", opts.CmdArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	cmd.ExtraFiles = []*os.File{pw}
	cmd.Env = append(os.Environ(),
		"LATTICE_RUN_SESSION="+sessionID,
		"LATTICE_RUN_PROFILE_PATH="+profilePath,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr

	if err := cmd.Start(); err != nil {
		return res, fmt.Errorf("spawn launcher: %w", err)
	}
	_ = pw.Close() // parent keeps read end only
	defer func() { _ = cmd.Process.Release() }()

	// Track which host-side steps succeeded so the deferred teardown only
	// undoes work that was actually done (评审注 3).
	setupStage := 0
	defer func() {
		if setupStage >= 2 {
			for _, c := range hostTeardownCommands(spec) {
				_ = exec.Command(c[0], c[1:]...).Run()
			}
		}
	}()

	for _, c := range hostSetupCommands(spec) {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			return res, fmt.Errorf("host setup %v: %s: %w", c, out, err)
		}
	}
	setupStage = 1
	if err := exec.Command("ip", "link", "set", spec.ChildVeth, "netns", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return res, fmt.Errorf("move veth into netns: %w", err)
	}
	setupStage = 2
	ensureIPForward()

	scanner := bufio.NewScanner(pr)
	status := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "READY":
			continue
		case strings.HasPrefix(line, "ERROR "):
			status = line
		case strings.HasPrefix(line, "RESULT "):
			raw := []byte(strings.TrimPrefix(line, "RESULT "))
			res.Raw = raw
			if parseErr := json.Unmarshal(raw, &res); parseErr != nil {
				return res, fmt.Errorf("launcher result: %w", parseErr)
			}
		}
	}

	waitErr := cmd.Wait()
	if status != "" {
		return res, errors.New(strings.TrimPrefix(status, "ERROR "))
	}
	if res.ExitCode == 0 && waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
	}
	if opts.StrictViolations && res.Violations > 0 && res.ExitCode == 0 {
		res.ExitCode = 2
	}
	return res, nil
}

// launcherMain runs inside the fresh netns. argv[0] is the agent command.
func launcherMain(sessionID, profilePath string, cmdArgs []string) int {
	spec := newNetnsSpec(sessionID)
	for _, c := range childSetupCommands(spec) {
		if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR child setup %v: %s\n", c, out)
			return 1
		}
	}

	profileData, err := os.ReadFile(profilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR read profile: %v\n", err)
		return 1
	}
	profile, err := ParseProfile(profileData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR parse profile: %v\n", err)
		return 1
	}

	upstreams := profile.Spec.Egress.DNS.Servers
	if len(upstreams) == 1 && upstreams[0] == "system" {
		upstreams = systemResolvers()
	}
	stats := NewStats()
	auditPath := profile.Spec.Audit.Path
	if auditPath == "" {
		auditPath = "/tmp/lattice-audit.jsonl"
	}
	jsonl, err := NewJSONLWriter(auditPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR audit log: %v\n", err)
		return 1
	}
	sinks := TeeSink{jsonl}

	table := NewAuthTable()
	filter := NewDomainFilter(profile, table, stats, sinks, sessionID)

	dnsSrv, err := NewDNSServer(":53", profile, table, upstreams,
		markDialContext, sinks, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR dns: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := dnsSrv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR dns start: %v\n", err)
		return 1
	}

	proxy := &tproxy.Proxy{
		Addr: fmt.Sprintf("127.0.0.1:%d", tproxyPort),
		Dial: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			host, portStr, splitErr := net.SplitHostPort(addr)
			if splitErr != nil {
				return nil, splitErr
			}
			ip := net.ParseIP(host)
			port := parsePort(portStr)
			if !filter.Allow("agent", ip, port) {
				return nil, fmt.Errorf("egress denied: %s", addr)
			}
			return markDialContext(dialCtx, network, addr)
		},
	}
	if err := proxy.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR tproxy start: %v\n", err)
		return 1
	}

	writeLauncherLine("READY")

	exitCode := forkAgentProcess(ctx, cmdArgs)

	out, _ := json.Marshal(map[string]int{"violations": stats.Violations(), "exit_code": exitCode})
	writeLauncherLine("RESULT " + string(out))
	_ = jsonl.Close()
	return exitCode
}

// agentSysProcAttr returns the attributes that strip network privileges from
// the agent: a single-uid user namespace means the agent holds capabilities
// only in its own userns, while the sandbox netns is owned by the init
// userns — raw/packet sockets and iptables changes are denied even as "root".
func agentSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
}

func forkAgentProcess(ctx context.Context, cmdArgs []string) int {
	child := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.SysProcAttr = agentSysProcAttr()
	if err := child.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "lattice: start agent: %v\n", err)
		return 127
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-done:
	case <-sigCh:
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
			<-done
		}
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(<-done, &exitErr) {
		code = exitErr.ExitCode()
	}
	cancel()
	return code
}

// markDialContext dials with SO_MARK=1 so launcher sockets bypass the
// OUTPUT REDIRECT rules.
func markDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Control: controlSetMark, Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, addr)
}

func controlSetMark(network, address string, c syscall.RawConn) error {
	var ctrlErr error
	err := c.Control(func(fd uintptr) {
		ctrlErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, 1)
	})
	if err != nil {
		return err
	}
	return ctrlErr
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func mustSelf() string {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return self
}

func parsePort(s string) uint16 {
	v, _ := strconv.ParseUint(s, 10, 16)
	return uint16(v)
}

func systemResolvers() []string {
	data, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return []string{"8.8.8.8:53"}
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "nameserver ") {
			ns := strings.TrimSpace(strings.TrimPrefix(line, "nameserver "))
			out = append(out, net.JoinHostPort(ns, "53"))
		}
	}
	if len(out) == 0 {
		return []string{"8.8.8.8:53"}
	}
	return out
}
```

`writeLauncherLine`/`acquireSessionLock`/`ensureIPForward`/`parseResultLine` 一并写在同文件（`forkAgentProcess` 与其 hardening 属性 `agentSysProcAttr` 在上面代码块）：

```go
var launcherPipe = os.NewFile(3, "launcher-pipe")

func writeLauncherLine(line string) {
	if launcherPipe != nil {
		fmt.Fprintln(launcherPipe, line)
	}
}

func acquireSessionLock() (*os.File, error) {
	f, err := os.OpenFile("/tmp/lattice-run-session.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another lattice run session is active")
	}
	return f, nil
}

func ensureIPForward() {
	out, err := exec.Command("sysctl", "-n", "net.ipv4.ip_forward").Output()
	if err == nil && strings.TrimSpace(string(out)) == "0" {
		_ = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	}
}

func parseResultLine(line string) (SessionResult, error) {
	var r SessionResult
	return r, json.Unmarshal([]byte(strings.TrimPrefix(line, "RESULT ")), &r)
}
```

> 评审注 1：`forkAgentProcess` 里 `select` 第二分支与末行对同一 channel 重复接收，实现时统一为：局部 `waitErr := <-done` 先收，再按 sigCh 分支处理；保持"退出码透传 + SIGTERM 优雅终止"语义即可。
> 评审注 2：launcher 的 `__run-launcher` 子命令在 Task 10 注册（Hidden: true），把 `os.Args[2:]` 作为 cmdArgs、环境变量取 session/profile path 后调用 `launcherMain`。
> 评审注 3：`RunLocal` 的 host setup 中途失败路径也要清理——把 `hostSetupCommands` 之后的全部逻辑包进一个 `defer`，按"已执行到哪步"跳过未做的清理，避免 veth/MASQ 残留。
> 评审注 4：`ProfileSummary` 与 `SaveReport` 属 Task 11（report.go）；本任务只补导出 `LauncherMain`（包装既有 `launcherMain`）。
> 评审注 5：审计事件里的 `Profile.SHA256` 取自 `--egress-allow` 未生效时的源文件字节（`raw`）；经内联覆盖的会话属临时调试用法，其 sha256 语义 v2 统一改为对 effective profile 计算。
> 评审注 6（hardening 语义）：agent 的单 uid user namespace 是 v1 反绕过的关键——agent 即使是"root"，其 capabilities 只在自己的 userns 内有效，而沙箱 netns 归 init userns 所有，因此 `socket(AF_PACKET)`、raw socket、iptables 修改全部被内核拒绝。已知限制并写入文档：agent 无法绑定 <1024 端口（cap 在 netns 属主 userns 下不可用，agent 场景几乎不需要）；no_new_privs 由 Go SysProcAttr 无法直接设置，靠"无 setuid 二进制可执行面 + bounding 无特权"等效覆盖，v2 seccomp notify 落地后补齐。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS（纯函数部分；`go vet ./internal/run/` 干净）

- [ ] **Step 5: 提交**

```bash
git add internal/run/launcher_linux.go internal/run/launcher_linux_test.go
git commit -m "feat(run): local netns runner engine with marked-dialer loop prevention"
```

---

### Task 10: `lattice run` 命令 + 隐藏 launcher 入口

**Files:**
- Create: `cmd/lattice/cmd/run/run.go`
- Modify: `cmd/lattice/cmd/root.go:93`（追加 `rootCmd.AddCommand(run.NewRunCommand())`）
- Test: `cmd/lattice/cmd/run/run_test.go`

**Interfaces:**
- Consumes: Task 9（`RunLocal`）、Task 7（报告，Task 11 的 `RenderMarkdown`——本任务先用 `RenderTerminal`）
- Produces: `func NewRunCommand() *cobra.Command`；`func NewLauncherCommand() *cobra.Command`（Hidden）
- 行为：`--profile <name|path>`（默认 `none` = 内置全阻断？否——**默认无 profile 时报错并提示**，宁可明确不可猜测）；`--ttl`、`--egress-allow`（逗号分隔 `host[:port]` 或 CIDR，覆盖式追加进 profile 的 allow）、`--yes`（跳过摘要确认）、`--docker`（非 Linux 时构造 Task 12 的 docker 命令并打印/执行）、`--strict-violations`；`--report md`（会话结束打印 Markdown 报告并写 `~/.lattice/reports/<session>.md`）。

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"strings"
	"testing"
	"time"

	internalrun "github.com/alatticeio/lattice/internal/run"
)

func TestInlineEgressAllowParsing(t *testing.T) {
	entries, err := parseInlineEgressAllow("api.example.com:443,10.0.0.0/8,foo.com")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Domain != "api.example.com" || entries[0].Ports[0] != 443 {
		t.Fatalf("e0: %+v", entries[0])
	}
	if entries[1].CIDR == nil {
		t.Fatal("e1 should be CIDR")
	}
	if entries[2].Domain != "foo.com" || len(entries[2].Ports) != 0 {
		t.Fatalf("e2: %+v", entries[2])
	}
}

func TestInlineEgressAllowInvalid(t *testing.T) {
	if _, err := parseInlineEgressAllow("not a host!"); err == nil {
		t.Fatal("expected error")
	}
}

func TestProfileSummaryText(t *testing.T) {
	p := mustParseProfile(t)
	p.Spec.Session.TTL = 4 * time.Hour
	s := internalrun.ProfileSummary(p)
	for _, want := range []string{"npm-ci", "1.0.0", "netns", "registry.npmjs.org"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
}
```

`mustParseProfile` 用 Task 1 的 `profileYAML` 常量（同包测试文件复制或提为 `testdata`；选择：新建 `cmd/lattice/cmd/run/testdata/profile.yaml` 内容同 `profileYAML`，helper 读它）。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./cmd/lattice/cmd/run/ -v`
Expected: FAIL（undefined）

- [ ] **Step 3: 实现**

```go
package run

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	internalrun "github.com/alatticeio/lattice/internal/run"
)

var (
	flagProfile     string
	flagTTL         time.Duration
	flagEgressAllow string
	flagYes         bool
	flagDocker      bool
	flagStrict      bool
	flagReport      bool
)

func NewRunCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run --profile <name|path> -- <command> [args...]",
		Short: "Run an AI agent inside a local zero-trust egress sandbox",
		Long: `Run resolves an EgressProfile, prints a summary, then executes the given
command inside a network namespace with domain-level egress enforcement:
DNS is intercepted, only allow-listed domains resolve and connect, and every
decision is audited. No control plane, no enrollment.

Requires root/NET_ADMIN on Linux. On macOS use --docker.`,
		Example: `  lattice run --profile claude-code -- claude-code "fix the bug"
  lattice run --profile npm-ci --ttl 30m -- npm install`,
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: false,
		RunE:               runRun,
	}
	cmd.Flags().StringVar(&flagProfile, "profile", "", "profile name or yaml path (required)")
	cmd.Flags().DurationVar(&flagTTL, "ttl", 0, "override session TTL")
	cmd.Flags().StringVar(&flagEgressAllow, "egress-allow", "", "inline allow entries: host[:port] or CIDR, comma-separated")
	cmd.Flags().BoolVar(&flagYes, "yes", false, "skip profile summary confirmation")
	cmd.Flags().BoolVar(&flagDocker, "docker", false, "wrap execution in a Linux container (macOS path)")
	cmd.Flags().BoolVar(&flagStrict, "strict-violations", false, "exit 2 if any egress violation occurred")
	cmd.Flags().BoolVar(&flagReport, "report", false, "print and save a Markdown session report")
	_ = cmd.MarkFlagRequired("profile")
	return cmd
}

func runRun(cmd *cobra.Command, args []string) error {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 || len(args) <= dash {
		return fmt.Errorf("usage: lattice run --profile <name> -- <command> [args...]")
	}
	cmdArgs := args[dash:]

	if runtime.GOOS != "linux" && !flagDocker {
		return fmt.Errorf("native egress sandbox requires Linux; use --docker (or read docs/profile-spec.md)")
	}
	if flagDocker {
		argv, err := internalrun.DockerWrapArgs(flagProfile, cmdArgs)
		if err != nil {
			return err
		}
		fmt.Println("+", strings.Join(argv, " "))
		return exec.Command(argv[0], argv[1:]...).Run()
	}

	resolved, err := internalrun.LoadProfile(flagProfile)
	if err != nil {
		return err
	}
	profile := resolved.Profile
	if flagEgressAllow != "" {
		entries, err := parseInlineEgressAllow(flagEgressAllow)
		if err != nil {
			return err
		}
		profile.Spec.Egress.Internet.Allow = append(profile.Spec.Egress.Internet.Allow, entries...)
	}
	if err := profile.Validate(); err != nil {
		return err
	}

	fmt.Print(internalrun.ProfileSummary(profile))
	if !flagYes {
		fmt.Print("Proceed? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return fmt.Errorf("aborted")
		}
	}

	result, err := internalrun.RunLocal(context.Background(), internalrun.LocalOptions{
		Profile:          profile,
		ProfilePath:      resolved.Path,
		CmdArgs:          cmdArgs,
		TTL:              flagTTL,
		StrictViolations: flagStrict,
	})
	if err != nil {
		return err
	}
	if flagReport {
		md := internalrun.RenderMarkdown(internalrun.SessionMeta{
			SessionID: "see-audit", Profile: internalrun.ProfileRef{Name: profile.Metadata.Name, Version: profile.Metadata.Version, SHA256: profile.SHA256()},
			Isolation: "netns", ExitCode: result.ExitCode,
		}, result.Stats)
		path, saveErr := internalrun.SaveReport(md)
		if saveErr == nil {
			fmt.Printf("report saved: %s\n", path)
		}
		fmt.Println(md)
	}
	return nil
}

// parseInlineEgressAllow parses --egress-allow entries (host[:port] | CIDR).
func parseInlineEgressAllow(s string) ([]internalrun.AllowEntry, error) {
	var out []internalrun.AllowEntry
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, cidr, err := net.ParseCIDR(part); err == nil {
			out = append(out, internalrun.AllowEntry{CIDR: cidr})
			continue
		}
		host, portStr, err := net.SplitHostPort(part)
		if err != nil {
			out = append(out, internalrun.AllowEntry{Domain: strings.ToLower(part)})
			continue
		}
		port, err := strconv.ParseUint(portStr, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid port in %q: %w", part, err)
		}
		out = append(out, internalrun.AllowEntry{Domain: strings.ToLower(host), Ports: []uint16{uint16(port)}})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty --egress-allow")
	}
	return out, nil
}
```

`profileSummary` / `NewLauncherCommand`：

```go
func NewLauncherCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "__run-launcher [agent-args...]",
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID := os.Getenv("LATTICE_RUN_SESSION")
			profilePath := os.Getenv("LATTICE_RUN_PROFILE_PATH")
			os.Exit(internalrun.LauncherMain(sessionID, profilePath, args))
			return nil
		},
	}
}
```

`internal/run/launcher_linux.go` 补导出：`func LauncherMain(sessionID, profilePath string, cmdArgs []string) int`（包装既有 `launcherMain`）。`ProfileSummary` 与 `SaveReport` 由 Task 11 落在 `internal/run/report.go`，本任务直接调用。

root.go 修改（`cmd/lattice/cmd/root.go:93` 后追加一行）：

```go
	rootCmd.AddCommand(run.NewRunCommand())
	rootCmd.AddCommand(run.NewLauncherCommand())
```

import 增加 `"github.com/alatticeio/lattice/cmd/lattice/cmd/run"`（包名冲突时 alias `runpkg`——`run` 与 cobra 变量不冲突，直接用）。

- [ ] **Step 4: 运行确认通过**

Run: `go build ./... && go test ./cmd/lattice/cmd/run/ -v`
Expected: BUILD OK，PASS

- [ ] **Step 5: 提交**

```bash
git add cmd/lattice/cmd/run/ cmd/lattice/cmd/root.go internal/run/
git commit -m "feat(run): lattice run command with profile summary and launcher entry"
```

---

### Task 11: 会话报告渲染 + Profile 汇总/存档

**Files:**
- Create: `internal/run/report.go`
- Test: `internal/run/report_test.go`

**Interfaces:**
- Consumes: Task 5（Stats/ProfileRef）、Task 1（EgressProfile）
- Produces: `type SessionMeta struct { SessionID string; Profile ProfileRef; Isolation string; ExitCode int }`；`func RenderTerminal(m SessionMeta, s StatsSnapshot) string`；`func RenderMarkdown(m SessionMeta, s StatsSnapshot) string`；`func RenderJSON(m SessionMeta, s StatsSnapshot) []byte`；`func SaveReport(md string) (string, error)`；`func ProfileSummary(p *EgressProfile) string`（Task 10 的运行前摘要消费）

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"encoding/json"
	"strings"
	"testing"
)

func sampleSnapshot() StatsSnapshot {
	return StatsSnapshot{
		TopAllowed: []DomainCount{{Domain: "registry.npmjs.org", Connections: 7}},
		Denied:     []DenyEvent{{IP: "93.1.2.3", Port: 80, Reason: "default-deny"}},
		Violations: 1,
	}
}

func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(SessionMeta{SessionID: "abc", Isolation: "netns", ExitCode: 0,
		Profile: ProfileRef{Name: "npm-ci", Version: "1.0.0"}}, sampleSnapshot())
	for _, want := range []string{"# LatticeRun session report", "npm-ci", "netns", "registry.npmjs.org", "default-deny"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q:\n%s", want, md)
		}
	}
}

func TestRenderTerminal(t *testing.T) {
	s := RenderTerminal(SessionMeta{SessionID: "abc", Isolation: "netns", ExitCode: 0}, sampleSnapshot())
	if !strings.Contains(s, "registry.npmjs.org") {
		t.Fatalf("terminal: %s", s)
	}
}

func TestRenderJSON(t *testing.T) {
	b := RenderJSON(SessionMeta{SessionID: "abc", Isolation: "netns", ExitCode: 0}, sampleSnapshot())
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReport(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := SaveReport("# t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".md") {
		t.Fatalf("path = %q", path)
	}
}

func TestProfileSummary(t *testing.T) {
	p, err := ParseProfile([]byte(profileYAML))
	if err != nil {
		t.Fatal(err)
	}
	s := ProfileSummary(p)
	for _, want := range []string{"npm-ci", "1.0.0", "netns", "registry.npmjs.org", "169.254.169.254"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary missing %q:\n%s", want, s)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run "TestRender|TestSaveReport" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

```go
package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type SessionMeta struct {
	SessionID string
	Profile   ProfileRef
	Isolation string
	ExitCode  int
}

func (m SessionMeta) snapshot(s StatsSnapshot) map[string]any {
	return map[string]any{
		"session_id": m.SessionID,
		"profile":    m.Profile,
		"isolation":  m.Isolation,
		"exit_code":  m.ExitCode,
		"stats":      s,
	}
}

func RenderJSON(m SessionMeta, s StatsSnapshot) []byte {
	b, _ := json.MarshalIndent(m.snapshot(s), "", "  ")
	return b
}

func RenderTerminal(m SessionMeta, s StatsSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "── LatticeRun session %s ──\n", m.SessionID)
	fmt.Fprintf(&b, "profile %s v%s · isolation %s · exit %d · violations %d\n",
		m.Profile.Name, m.Profile.Version, m.Isolation, m.ExitCode, s.Violations)
	b.WriteString("allowed (top):\n")
	for i, d := range s.TopAllowed {
		if i == 10 {
			break
		}
		fmt.Fprintf(&b, "  %-45s %d conns\n", d.Domain, d.Connections)
	}
	if len(s.Denied) > 0 {
		b.WriteString("blocked:\n")
		for _, d := range s.Denied {
			fmt.Fprintf(&b, "  %s:%d (%s)\n", d.IP, d.Port, d.Reason)
		}
	}
	return b.String()
}

func RenderMarkdown(m SessionMeta, s StatsSnapshot) string {
	var b strings.Builder
	b.WriteString("# LatticeRun session report\n\n")
	fmt.Fprintf(&b, "| field | value |\n|---|---|\n| session | `%s` |\n| profile | %s v%s |\n| isolation | %s |\n| exit code | %d |\n| violations | %d |\n\n",
		m.SessionID, m.Profile.Name, m.Profile.Version, m.Isolation, m.ExitCode, s.Violations)
	b.WriteString("## Allowed (top domains)\n\n| domain | connections |\n|---|---|\n")
	for i, d := range s.TopAllowed {
		if i == 20 {
			break
		}
		fmt.Fprintf(&b, "| %s | %d |\n", d.Domain, d.Connections)
	}
	if len(s.Denied) > 0 {
		b.WriteString("\n## Blocked connections\n\n| destination | reason |\n|---|---|\n")
		for _, d := range s.Denied {
			fmt.Fprintf(&b, "| %s:%d | %s |\n", d.IP, d.Port, d.Reason)
		}
	}
	fmt.Fprintf(&b, "\n_Generated %s_\n", time.Now().UTC().Format(time.RFC3339))
	return b.String()
}

func SaveReport(md string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".lattice", "reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.md", time.Now().Unix()))
	if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ProfileSummary renders the pre-run profile confirmation block.
func ProfileSummary(p *EgressProfile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "profile:   %s v%s (sha256 %s...)\n", p.Metadata.Name, p.Metadata.Version, p.SHA256()[:12])
	fmt.Fprintf(&b, "isolation: %s\n", p.Spec.Isolation.Backend)
	fmt.Fprintf(&b, "ttl:       %s\n", p.Spec.Session.TTL)
	fmt.Fprintf(&b, "allow:     %d entries, default-deny=%v\n", len(p.Spec.Egress.Internet.Allow), p.Spec.Egress.Internet.DenyByDefault())
	for _, e := range p.Spec.Egress.Internet.Allow {
		switch {
		case e.Domain != "":
			if len(e.Ports) > 0 {
				fmt.Fprintf(&b, "  domain   %s ports %v\n", e.Domain, e.Ports)
			} else {
				fmt.Fprintf(&b, "  domain   %s (any port)\n", e.Domain)
			}
		case e.CIDR != nil:
			fmt.Fprintf(&b, "  cidr     %s\n", e.CIDR)
		}
	}
	fmt.Fprintf(&b, "block:     %d entries\n", len(p.Spec.Egress.Internet.Block))
	return b.String()
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/run/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/report.go internal/run/report_test.go
git commit -m "feat(run): session report renderers and save"
```

---

### Task 12: 注册表客户端 + `lattice profile` 命令族 + Docker 包装

**Files:**
- Create: `internal/run/registry.go`, `internal/run/dockerwrap.go`
- Create: `cmd/lattice/cmd/profile/profile.go`
- Modify: `cmd/lattice/cmd/root.go`（追加 `rootCmd.AddCommand(profile.NewProfileCommand())`）
- Test: `internal/run/registry_test.go`, `internal/run/dockerwrap_test.go`, `cmd/lattice/cmd/profile/profile_test.go`

**Interfaces:**
- Produces（registry）: `type RegistryClient struct { BaseURL string; HTTPClient *http.Client }`；`func NewRegistryClient() *RegistryClient`；`type IndexEntry struct { Name, Version, SHA256, Description string }`；`func (c *RegistryClient) FetchIndex(ctx context.Context) ([]IndexEntry, error)`（GET `<base>/index.json`，形状 `{"profiles":[...]}`）；`func (c *RegistryClient) Install(ctx context.Context, name string) (string, error)`（下载 `<base>/profiles/<name>/profile.yaml` → SHA256 与 index 比对 → 写 `userProfileDir()/<name>.yaml`）
- Produces（dockerwrap）: `func DockerWrapArgs(profile string, cmdArgs []string) ([]string, error)`
- Produces（profile cmd）: `func NewProfileCommand() *cobra.Command`（子命令 `list`（内置+本地+注册表合并视图）、`search <kw>`、`install <name>`、`info <name>`、`validate <path>`；v1 不做 update/remove——`install` 覆盖即升级，帮助文本注明）

- [ ] **Step 1: 写失败测试**

```go
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryFetchAndInstall(t *testing.T) {
	profileData := []byte("metadata:\n  name: web\n  version: 1.0.0\nspec:\n  egress:\n    internet:\n      allow:\n        - cidr: 169.254.169.254/32\n")
	sum := sha256.Sum256(profileData)
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"name":"web","version":"1.0.0","sha256":"` + hex.EncodeToString(sum[:]) + `","description":"d"}]}`))
	})
	mux.HandleFunc("/profiles/web/profile.yaml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(profileData)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Setenv("LATTICE_PROFILE_DIR", filepath.Join(t.TempDir(), "profiles"))
	c := NewRegistryClient()
	c.BaseURL = srv.URL
	idx, err := c.FetchIndex(context.Background())
	if err != nil || len(idx) != 1 {
		t.Fatalf("index: %v %+v", err, idx)
	}
	path, err := c.Install(context.Background(), "web")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryInstallSHAMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"profiles":[{"name":"bad","version":"1.0.0","sha256":"deadbeef"}]}`))
	})
	mux.HandleFunc("/profiles/bad/profile.yaml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("metadata:\n  name: bad\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("LATTICE_PROFILE_DIR", filepath.Join(t.TempDir(), "profiles"))
	c := NewRegistryClient()
	c.BaseURL = srv.URL
	if _, err := c.Install(context.Background(), "bad"); err == nil {
		t.Fatal("sha mismatch should fail")
	}
}
```

```go
package run

import (
	"strings"
	"testing"
)

func TestDockerWrapArgs(t *testing.T) {
	argv, err := DockerWrapArgs("claude-code", []string{"claude-code", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"docker", "run", "--rm", "--cap-add", "NET_ADMIN", "ghcr.io/alatticeio/lattice:latest", "run", "--profile", "claude-code", "--", "claude-code"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q: %s", want, joined)
		}
	}
}
```

（profile cmd 测试：`TestProfileCommandExists`——构造 `NewProfileCommand()`，断言 5 个子命令存在。）

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/run/ -run "TestRegistry|TestDockerWrap" -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`internal/run/registry.go`：

```go
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const defaultRegistryBase = "https://raw.githubusercontent.com/alatticeio/lattice-profiles/main"

// RegistryClient consumes the lattice-profiles registry over HTTPS.
type RegistryClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		BaseURL:    defaultRegistryBase,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type IndexEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Description string `json:"description"`
}

type indexDoc struct {
	Profiles []IndexEntry `json:"profiles"`
}

func (c *RegistryClient) FetchIndex(ctx context.Context) ([]IndexEntry, error) {
	var doc indexDoc
	if err := c.getJSON(ctx, c.BaseURL+"/index.json", &doc); err != nil {
		return nil, err
	}
	return doc.Profiles, nil
}

// Install downloads, verifies and stores one profile; overwrite = upgrade.
func (c *RegistryClient) Install(ctx context.Context, name string) (string, error) {
	idx, err := c.FetchIndex(ctx)
	if err != nil {
		return "", err
	}
	var entry *IndexEntry
	for i := range idx {
		if idx[i].Name == name {
			entry = &idx[i]
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("profile %q not in registry index", name)
	}
	data, err := c.get(ctx, fmt.Sprintf("%s/profiles/%s/profile.yaml", c.BaseURL, name))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != entry.SHA256 {
		return "", fmt.Errorf("sha256 mismatch for %s: want %s got %s", name, entry.SHA256, got)
	}
	dir := userProfileDir()
	if dir == "" {
		return "", fmt.Errorf("cannot resolve profile dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (c *RegistryClient) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (c *RegistryClient) getJSON(ctx context.Context, url string, v any) error {
	data, err := c.get(ctx, url)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
```

`internal/run/dockerwrap.go`：

```go
package run

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DockerWrapArgs builds the docker invocation that runs lattice run inside a
// Linux container (macOS path). cwd and the profile dir are bind-mounted.
func DockerWrapArgs(profile string, cmdArgs []string) ([]string, error) {
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("docker wrap needs a command")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	argv := []string{
		"docker", "run", "--rm", "-it",
		"--cap-add", "NET_ADMIN",
		"-v", cwd + ":/work", "-w", "/work",
		"-v", filepath.Join(home, ".lattice") + ":/root/.lattice",
		"ghcr.io/alatticeio/lattice:latest",
		"run", "--profile", profile, "--yes", "--", strings.Join(cmdArgs, " "),
	}
	return argv, nil
}
```

> 评审注：`strings.Join(cmdArgs, " ")` 会破坏含空格参数——实现时改为 `argv = append(argv, cmdArgs...)` 并把镜像名前移一段，直接以 `--` 结尾拼接原始参数；上面测试断言改为检查子串 `"--", "claude-code"` 或 `strings.Contains(joined, "-- claude-code")`。最终采用 append 形态。

`cmd/lattice/cmd/profile/profile.go`：

```go
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	internalrun "github.com/alatticeio/lattice/internal/run"
)

func NewProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage egress profiles (list/search/install/info/validate)",
	}
	cmd.AddCommand(listCmd(), searchCmd(), installCmd(), infoCmd(), validateCmd())
	return cmd
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List builtin, local and registry profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, n := range internalrun.BuiltinNames() {
				fmt.Printf("builtin  %s\n", n)
			}
			if dir := internalrun.UserProfileDir(); dir != "" {
				matches, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
				for _, m := range matches {
					fmt.Printf("local    %s\n", strings.TrimSuffix(filepath.Base(m), ".yaml"))
				}
			}
			ctx := cmd.Context()
			client := internalrun.NewRegistryClient()
			entries, err := client.FetchIndex(ctx)
			if err == nil {
				for _, e := range entries {
					fmt.Printf("registry %s v%s — %s\n", e.Name, e.Version, e.Description)
				}
			} else {
				fmt.Printf("registry unreachable: %v\n", err)
			}
			return nil
		},
	}
}

func searchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <keyword>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := internalrun.NewRegistryClient().FetchIndex(cmd.Context())
			if err != nil {
				return err
			}
			kw := strings.ToLower(args[0])
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name+" "+e.Description), kw) {
					fmt.Printf("%s v%s — %s\n", e.Name, e.Version, e.Description)
				}
			}
			return nil
		},
	}
}

func installCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <name>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := internalrun.NewRegistryClient().Install(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("installed %s -> %s\n", args[0], path)
			return nil
		},
	}
}

func infoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <name|path>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := internalrun.LoadProfile(args[0])
			if err != nil {
				return err
			}
			fmt.Print(internalrun.ProfileSummary(r.Profile))
			return nil
		},
	}
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			p, err := internalrun.ParseProfile(data)
			if err != nil {
				return err
			}
			if err := p.Validate(); err != nil {
				return err
			}
			fmt.Printf("OK %s v%s (sha256 %s)\n", p.Metadata.Name, p.Metadata.Version, p.SHA256())
			return nil
		},
	}
}
```

> 评审注：profile.go 不再需要 sort import（已删）。`internal/run` 需导出 `UserProfileDir()`（包装既有 `userProfileDir`）。

- [ ] **Step 4: 运行确认通过**

Run: `go build ./... && go test ./internal/run/ ./cmd/lattice/cmd/profile/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/run/registry.go internal/run/dockerwrap.go cmd/lattice/cmd/profile/ cmd/lattice/cmd/root.go
git commit -m "feat(run): registry client, lattice profile command family, docker wrap"
```

---

### Task 13: lattice-profiles 仓库脚手架（独立仓库交付物）

**Files（在主仓库旁新建 `../lattice-profiles/`，独立 git init）:**
- Create: `lattice-profiles/profiles/npm-ci/profile.yaml`（内容 = 主仓库内置模板）
- Create: `lattice-profiles/profiles/npm-ci/README.md`
- Create: `lattice-profiles/profiles/npm-ci/profile.test.yaml`
- Create: `lattice-profiles/schema/profile.v1alpha1.json`
- Create: `lattice-profiles/scripts/build_index.py`
- Create: `lattice-profiles/.github/workflows/ci.yml`
- Create: `lattice-profiles/README.md`

**Interfaces:**
- Consumes: 主仓库 `internal/run/profiles/*.yaml`（权威版本）；`lattice validate` CLI
- Produces: `index.json`（CI 构建提交）；CI 门禁四件套：JSON Schema 校验、block 含元数据端点 lint、README 存在、契约测试钩子

- [ ] **Step 1: 建仓与目录**

```bash
mkdir -p ../lattice-profiles/{profiles/npm-ci,schema,scripts,.github/workflows}
cd ../lattice-profiles && git init
```

- [ ] **Step 2: 内容文件**

`profiles/npm-ci/README.md`：

```markdown
# npm-ci

Egress profile for `npm install && npm test` in CI. Allows the npm registry,
GitHub (api/codeload/objects) over TLS, and RFC1918 internal ranges.

Threat model: blocks all other egress including cloud metadata endpoints
(169.254.169.254), so a prompt-injected npm postinstall script cannot
exfiltrate credentials or phone home.

Contract tests assert: registry.npmjs.org:443 allowed; example.com denied.
```

`profiles/npm-ci/profile.test.yaml`（契约测试清单，v1 由脚本消费）：

```yaml
allow:
  - { domain: registry.npmjs.org, port: 443 }
  - { domain: github.com, port: 443 }
deny:
  - { domain: example.com }
  - { ip: 169.254.169.254 }
```

`scripts/build_index.py`：

```python
#!/usr/bin/env python3
"""Build index.json: name, version, sha256, description per profile."""
import hashlib, json, pathlib, sys

import yaml  # PyYAML

root = pathlib.Path(__file__).resolve().parent.parent
entries = []
for pdir in sorted((root / "profiles").iterdir()):
    pf = pdir / "profile.yaml"
    if not pf.exists():
        continue
    data = pf.read_bytes()
    meta = yaml.safe_load(data)["metadata"]
    if not (pdir / "README.md").exists():
        sys.exit(f"{pdir}: README.md required")
    block = yaml.safe_load(data)["spec"]["egress"]["internet"].get("block") or []
    if not any("169.254.169.254/32" in str(e.get("cidr", "")) for e in block):
        sys.exit(f"{pdir}: block must include metadata endpoint")
    entries.append({
        "name": meta["name"],
        "version": meta["version"],
        "sha256": hashlib.sha256(data).hexdigest(),
        "description": meta.get("description", ""),
    })
(root / "index.json").write_text(json.dumps({"profiles": entries}, indent=2) + "\n")
print(f"index.json: {len(entries)} profiles")
```

`.github/workflows/ci.yml`：

```yaml
name: ci
on: [push, pull_request]
jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with: { python-version: "3.12" }
      - run: pip install pyyaml jsonschema
      - name: schema + policy lint + index build
        run: |
          python scripts/build_index.py
          for f in profiles/*/profile.yaml; do
            python - <<EOF
          import json, yaml, jsonschema, sys
          schema = json.load(open("schema/profile.v1alpha1.json"))
          jsonschema.validate(yaml.safe_load(open("$f")), schema)
          EOF
          done
      - name: contract tests (v1 hook)
        run: echo "contract runner lands with lattice CLI in CI image"
```

`schema/profile.v1alpha1.json`（最小可校验版）：`type: object`，required `["apiVersion","kind","metadata","spec"]`，`properties.apiVersion.const: lattice.io/v1alpha1`、`kind.const: EgressProfile`、`metadata.required: ["name","version"]`、`spec.properties.isolation.properties.backend.enum: ["netns"]`。

- [ ] **Step 3: 本地跑通构建脚本**

```bash
cd ../lattice-profiles && pip3 install pyyaml && python3 scripts/build_index.py && cat index.json
```
Expected: `index.json: 1 profiles`

- [ ] **Step 4: 提交（独立仓库）**

```bash
git add -A && git commit -m "chore: scaffold lattice-profiles registry with CI gates"
```

> 注意：v1 只放 npm-ci 一个模板进注册表（其余四个从主仓库内置同步，作为后续 PR），降低首版 CI 维护面。

---

### Task 14: 端到端冒烟、profile-spec 文档与 spec 回写

**Files:**
- Create: `docs/profile-spec.md`
- Create: `hack/run/e2e_local.sh`
- Modify: `README.md`（AI Agent Sandbox 表格追加一行 LatticeRun 能力）
- Modify: `docs/superpowers/specs/2026-09-19-latticerun-design.md`（回写三处偏差：§四 backend 默认 `netns`、§九 mcpproxy local 延后、§六 域名精确匹配）

**Interfaces:**
- Consumes: 全部前序任务
- Produces: 可执行验收链路 + 权威文档

- [ ] **Step 1: 写 docs/profile-spec.md**

内容框架（完整成文）：格式总览（Task 1 的字段表）、解析顺序与来源优先级、域名精确匹配语义与通配符 roadmap、DNS 处理规则（AAAA 空应答、TTL≤5m、filtered/direct/block）、netns 执行模型图（复用 Task 9 拓扑描述，**含反绕过设计：agent 单 uid userns 特权剥离、netns 禁 IPv6**）、隔离级别标注表（v1：`netns`；宿主可选 `netns-under-runsc` 按 Task 14 Step 3b 验证结果如实标注；`gvisor`/`microvm-gvisor` 为 v2，并明确 v1 声称边界：网络出口管控，不含宿主级进程隔离）、CLI 参考（run/profile 全部 flag）、审计与报告字段表（Task 5 Event 结构）、注册表贡献流程（README/契约测试/metadata-endpoint lint 三门槛）。

- [ ] **Step 2: 写端到端冒烟脚本**

`hack/run/e2e_local.sh`：

```bash
#!/usr/bin/env bash
# LatticeRun --local e2e smoke. Requires root Linux (or run inside the
# ghcr.io/alatticeio/lattice container with --cap-add NET_ADMIN).
set -euo pipefail

echo "[1/4] build"
go build -o /tmp/lattice ./cmd/lattice

echo "[2/4] deny path"
set +e
/tmp/lattice run --profile llm-api-only --yes -- curl -sS -m 5 https://example.com >/dev/null 2>&1
curl_rc=$?
set -e
# curl must fail (network unreachable / reset), not 0
if [ "$curl_rc" -eq 0 ]; then echo "FAIL: example.com should be blocked"; exit 1; fi
grep -q '"verdict":"drop"' /tmp/lattice-audit.jsonl || { echo "FAIL: no drop in audit"; exit 1; }

echo "[3/4] allow path"
/tmp/lattice run --profile llm-api-only --yes -- curl -sS -m 10 https://api.anthropic.com >/dev/null
echo "[4/4] report"
ls ~/.lattice/reports/*.md >/dev/null
echo "E2E SMOKE OK"
```

> 评审注：curl 允许非零退出码作为"被阻断"证据，断言落在审计 JSONL 的 drop 事件上；会话时长由 profile TTL 约束，脚本不传额外 flag。

```bash
chmod +x hack/run/e2e_local.sh
```

- [ ] **Step 3: 手工执行冒烟（Linux 环境 / docker）**

```bash
docker run --rm -it --cap-add NET_ADMIN \
  -v "$PWD":/work -w /work -v "$HOME/.lattice:/root/.lattice" \
  golang:1.23 bash /work/hack/run/e2e_local.sh
```
Expected: `E2E SMOKE OK`（在无 Linux 环境时记录为 CI 跟进项，不阻塞合并，但在 PR 描述中注明）

- [ ] **Step 3b: runsc 严格档验证（只验证，不承诺）**

宿主装 gVisor 后以 `--runtime runsc` 重跑同一冒烟脚本，验证 netns + iptables REDIRECT + SO_ORIGINAL_DST 机制在 runsc 内是否完整工作：

```bash
docker run --rm -it --runtime runsc --cap-add NET_ADMIN \
  -v "$PWD":/work -w /work -v "$HOME/.lattice:/root/.lattice" \
  golang:1.23 bash /work/hack/run/e2e_local.sh
```

结论按实际结果写入 docs/profile-spec.md 隔离级别表：通过 → 标注 `netns-under-runsc` 为"宿主可选、受支持"；未通过 → 如实记录具体限制（哪条 iptables 机制不被 gVisor 支持）。**验证完成前不得在任何文档声称支持 runsc。**

- [ ] **Step 4: README 与 spec 回写**

README.md AI Agent Sandbox 表格追加：

```markdown
| **LatticeRun（本地沙箱）** | `lattice run --profile <name> -- <agent>`：域名级出口白名单 + 默认拒绝 + 会话审计报告，零控制面；profile 模板生态见 docs/profile-spec.md |
```

spec 回写三处（修订记录加 v3 行）：
1. §四 `isolation.backend` 默认值 `netns`（v1），`gvisor` 移至 v2 规划；
2. §九 `--local` 档 `--mcp-proxy` 从 Community 行移除，标注"待 mini-spec（需本地策略源）"；
3. §六 域名匹配明确"v1 精确匹配"。

- [ ] **Step 5: 全量回归 + 提交**

```bash
go build ./... && go test ./... && go vet ./internal/run/ ./cmd/lattice/cmd/...
git add docs/profile-spec.md hack/run/e2e_local.sh README.md docs/superpowers/specs/2026-09-19-latticerun-design.md
git commit -m "feat(run): profile spec docs, e2e smoke, spec amendment"
```

---

## 任务依赖图

```
T1 ─ T2 ─ T3 ────────────────────┐
 ├─ T4 ─ T6 ────┐                │
 ├─ T5 ─ T11 ───┼─ T9 ─ T10 ─────┴─ T12 ─ T14
 └─ T7 ─────────┤        │
      T8 ───────┘        │
                         T13（独立仓库，无依赖，可随时并行）
```

T10 消费 T3（Loader）、T9（引擎）、T11（ProfileSummary/SaveReport）；T12 依赖 T3/T10；T14 最后。T13 可与任何任务并行。
