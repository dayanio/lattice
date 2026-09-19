# LatticeCast 一句话智能投屏 v1 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付 spec（`docs/superpowers/specs/2026-09-18-latticecast-design.md` v4）定义的 v1：新仓库 `lattice-cast` 内的 cast-agent（发现/适配/解析/MCP/审计）+ 三平台渲染端（Android APK / macOS-Linux Go+mpv / Apple TV tvOS），达成"在外手机说一句'把 NAS 里的 xx 投到卧室电视'，电视播出并能答进度"。

**Architecture:** cast-agent 是家里常开节点上的 Go 进程（经宿主 lattice agent 入网），通过 mDNS 发现局域网内的 LatticeCast 渲染端 APK，用自有 HTTP/JSON 协议（play/pause/stop/seek/volume/status）指挥其拉流播放；对外暴露 MCP 工具集供 LLM 调用；渲染端不入 mesh。

**Tech Stack:** Go 1.26、`github.com/modelcontextprotocol/go-sdk/mcp`（MCP 官方 SDK）、`github.com/grandcat/zeroconf`（mDNS）、`gopkg.in/yaml.v3`、yt-dlp（外部二进制）、Kotlin + Media3 ExoPlayer + NanoHTTPD + NsdManager。

## Global Constraints

- 仓库：全部代码进新仓库 `github.com/dayanio/lattice-cast`；**本地检出于 `/Users/francis/workspc/lattice-cast`（与 lattice 仓库平级），严禁嵌套进 lattice 仓库内部**；lattice 主仓库仅 Task 15 一个文档任务（零代码）。
- License：Apache-2.0（对齐主仓库）。
- Go 版本：1.26（对齐主仓库 CI）。
- 端口约定：MCP `7800`、媒体 HTTP `7810`、渲染端 `7822`（APK 内可改）。
- 协议权威定义在 `docs/protocol.md`；Go 与 Kotlin 共用 `testdata/contract/*.json` 契约夹具，两端不得漂移。
- 渲染端不入 mesh、不跑 WireGuard；仅家庭局域网 + Bearer Token。
- v1 不做：转码、屏幕镜像、DLNA/Chromecast 适配器（v2）、tool_spans 控制面上报（v2）、LatticeDNS 别名（v2）。
- v1 认证：MCP 侧静态 Bearer Token（`Authenticator` 接口预留 JWT 实现位）；渲染端侧静态 Bearer Token。
- CI：`make test` 全绿才能合并；mDNS 相关测试必须响应 `testing.Short()`（CI 组播不稳）。
- 提交信息：conventional commits（feat/fix/test/docs/chore）。
- **分支注意**：lattice 主仓库当前 HEAD 在 `fix/sqlite-connection-pool` 且用户有未提交 WIP。Task 15 执行前必须确认工作树干净，且只在 `new_dev` 上做；任何任务都不得动用户的 WIP 文件。
- 依赖版本以执行日最新稳定版为准；MCP go-sdk API 若有小版本漂移，允许调整 handler 签名以编译通过，工具名与语义不得变。

---

### Task 1: 新仓库脚手架

**Files:**
- Create: 仓库 `lattice-cast`（GitHub `alatticeio/lattice-cast`）内：`go.mod`、`Makefile`、`LICENSE`、`README.md`、`.gitignore`、`.github/workflows/ci.yml`

**Interfaces:**
- Produces: Go module `github.com/dayanio/lattice-cast`；`make test`、`make lint`、`make build` 三个入口；CI workflow 文件。后续所有任务在此仓库内工作。

- [ ] **Step 1: 建仓并克隆**

```bash
cd /Users/francis/workspc   # 与 lattice 仓库平级；严禁克隆进 lattice 内部
gh repo create alatticeio/lattice-cast --public --description "LatticeCast — one-sentence casting for the Lattice mesh" --license apache-2.0 --clone
cd lattice-cast
git checkout -b feat/v1-agent
```

预期：本地出现 `lattice-cast/` 目录，含 LICENSE。若 gh 不可用，向用户报告并停在手动建仓。

- [ ] **Step 2: go.mod 与 .gitignore**

```bash
go mod init github.com/dayanio/lattice-cast
```

`.gitignore`:

```
/lattice-cast
*.test
coverage.out
.DS_Store
```

- [ ] **Step 3: Makefile**

```makefile
.PHONY: test lint build verify

test:
	go test ./... -count=1

lint:
	golangci-lint run

build:
	go build ./...

verify: lint test build
```

- [ ] **Step 4: CI workflow**

`.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push: { branches: [main, "feat/**"] }
  pull_request:
jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.26" }
      - run: go vet ./...
      - run: go test ./... -count=1 -short
```

注意 CI 跑 `-short`：mDNS 测试（Task 7）必须遵守 `testing.Short()`。

- [ ] **Step 5: README 骨架**

`README.md`:

```markdown
# LatticeCast

One-sentence casting for the [Lattice](https://github.com/alatticeio/lattice) mesh:
say "把 NAS 里的那部电影投到卧室电视" and it plays.

- `cmd/lattice-cast` — cast-agent (Go): discovery / adapter / media resolver / MCP tools
- `android/` — renderer APK (Kotlin + ExoPlayer) for Android TV / boxes
- `docs/protocol.md` — the LatticeCast wire protocol (authoritative)

Status: v1 in development. Design spec: lattice repo `docs/superpowers/specs/2026-09-18-latticecast-design.md`.
```

- [ ] **Step 6: 提交并推送**

```bash
git add -A && git commit -m "chore: scaffold lattice-cast repo (go 1.26, make verify, ci)"
git push -u origin feat/v1-agent
```

---

### Task 2: Android 骨架 + 侧载可行性门禁（含手动步骤）

**Files:**
- Create: `android/settings.gradle.kts`、`android/app/build.gradle.kts`、`android/app/src/main/AndroidManifest.xml`、`android/app/src/main/java/io/lattice/cast/MainActivity.kt`、`android/app/src/main/res/layout/activity_main.xml`

**Interfaces:**
- Produces: 可构建出 `app-debug.apk` 的最小 Android 工程（空 Activity 显示 "LatticeCast"）；后续 Task 13/14 在此工程上迭代。
- **门禁产出**：目标电视/盒子能否侧载本 APK。**若不能：停下，向用户报告，走 spec 第十三节开放问题 4 的决策（提前引入 DLNA 兜底）——不得自行继续 Task 13/14。**

- [ ] **Step 1: Gradle 工程**

`android/settings.gradle.kts`:

```kotlin
pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
dependencyResolutionManagement { repositories { google(); mavenCentral() } }
rootProject.name = "LatticeCast"
include(":app")
```

`android/app/build.gradle.kts`:

```kotlin
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}
android {
    namespace = "io.lattice.cast"
    compileSdk = 35
    defaultConfig { applicationId = "io.lattice.cast"; minSdk = 21; targetSdk = 35; versionCode = 1; versionName = "0.1.0" }
    buildFeatures { viewBinding = true }
}
dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
}
```

（根 `build.gradle.kts` 与 `gradle.properties` 按 Android Studio Node 的标准模板补齐——AGP 版本取当前稳定版。）

- [ ] **Step 2: Manifest 与空 Activity**

`AndroidManifest.xml`:

```xml
<manifest xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-permission android:name="android.permission.INTERNET"/>
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE"/>
    <application android:label="LatticeCast" android:theme="@android:style/Theme.Black.NoTitleBar.Fullscreen">
        <activity android:name=".MainActivity" android:exported="true" android:screenOrientation="landscape">
            <intent-filter><action android:name="android.intent.action.MAIN"/>
                <category android:name="android.intent.category.LEANBACK_LAUNCHER"/>
                <category android:name="android.intent.category.LAUNCHER"/></intent-filter>
        </activity>
    </application>
</manifest>
```

`MainActivity.kt`:

```kotlin
package io.lattice.cast
import android.app.Activity
import android.os.Bundle
import io.lattice.cast.databinding.ActivityMainBinding

class MainActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(ActivityMainBinding.inflate(layoutInflater).root)
    }
}
```

`res/layout/activity_main.xml`：居中 `TextView`，文本 `LatticeCast`。

- [ ] **Step 3: 构建 debug APK**

```bash
cd android && ./gradlew assembleDebug
```

预期：`app/build/outputs/apk/debug/app-debug.apk` 生成。

- [ ] **Step 4: 【手动·门禁】侧载到目标电视/盒子**

把 APK 拷给用户（或放局域网 HTTP），用户在电视上开启"允许未知来源"并安装、启动。记录：能否安装、能否启动、系统版本。

- [ ] **Step 5: 提交**

```bash
git add android && git commit -m "feat(android): minimal app skeleton + sideload feasibility gate"
```

---

### Task 3: 协议文档 + 契约夹具

**Files:**
- Create: `docs/protocol.md`、`testdata/contract/play_request.json`、`testdata/contract/play_response.json`、`testdata/contract/status_response.json`、`testdata/contract/status_error_response.json`、`testdata/contract/volume_request.json`

**Interfaces:**
- Produces: 全项目引用的 JSON 字段名（`url`/`title`/`position_ms`/`level`/`state`/`ok`/`error`/`duration_ms`）；Go 侧 `adapter` 包与 Kotlin 侧 `RendererServer` 的共同行为基准。后续 Task 4/5/13 直接读这些夹具。

- [ ] **Step 1: 写 docs/protocol.md（全文照抄 spec 第四节并展开）**

要点（正文必须包含）：
- 传输 HTTP/JSON；鉴权头 `Authorization: Bearer <token>`，401 时返回 `{"ok":false,"error":"unauthorized"}`。
- `POST /play` 请求 `{"url":"http://…","title":"星际穿越","position_ms":0}`，响应 `{"ok":true,"state":"playing"}`；渲染端解码失败/URL 不可达时响应 `{"ok":false,"state":"error","error":"source_unreachable"}`。
- `POST /pause`、`POST /stop`、`POST /seek`（`{"position_ms":N}`）、`POST /volume`（`{"level":0-100}`）→ `{"ok":true,"state":"…"}`。
- `GET /status` → `{"state":"playing|paused|idle|error","position_ms":N,"duration_ms":N,"title":"…","error":"…"}`。
- 未知路径 404 `{"ok":false,"error":"not_found"}`；未知方法 405。
- mDNS：`_latticecast._tcp`，TXT：`room=<房间>`、`v=1`。
- 状态机：`idle →(play)→ playing ⇄ paused →(stop)→ idle`；`error` 仅由 play 失败进入，下一次 `play` 可离开。

- [ ] **Step 2: 契约夹具**

`testdata/contract/play_request.json`:

```json
{"url":"http://192.168.1.10:7810/media/abc123","title":"Interstellar","position_ms":0}
```

`testdata/contract/play_response.json`:

```json
{"ok":true,"state":"playing"}
```

`testdata/contract/status_response.json`:

```json
{"state":"playing","position_ms":42000,"duration_ms":5400000,"title":"Interstellar","error":""}
```

`testdata/contract/status_error_response.json`:

```json
{"state":"error","position_ms":0,"duration_ms":0,"title":"","error":"source_unreachable"}
```

`testdata/contract/volume_request.json`:

```json
{"level":42}
```

- [ ] **Step 3: 提交**

```bash
git add docs testdata && git commit -m "docs: LatticeCast wire protocol v1 + contract fixtures"
```

---

### Task 4: CastAdapter 接口 + LatticeCast 客户端（TDD）

**Files:**
- Create: `internal/cast/adapter/adapter.go`、`internal/cast/adapter/latticecast/client.go`
- Test: `internal/cast/adapter/latticecast/client_test.go`

**Interfaces:**
- Produces（后续 Manager/MCP 依赖，签名照抄）:

```go
package adapter
type PlayRequest struct { URL string; Title string; PositionMS int64 }
type SeekRequest  struct { PositionMS int64 }
type VolumeRequest struct { Level int } // 0-100
type Status struct {
    State string; PositionMS int64; DurationMS int64
    Title string; Error string
}
// Target 定位一台渲染端
type Target struct { Host string; Port int; Token string }
func (t Target) BaseURL() string // http://host:port
// Client 是 LatticeCast 协议客户端
type Client struct { HTTP *http.Client }
func NewClient(hc *http.Client) *Client
func (c *Client) Play(ctx context.Context, t Target, r PlayRequest) (Status, error)
func (c *Client) Pause(ctx context.Context, t Target) (Status, error)
func (c *Client) Stop(ctx context.Context, t Target) (Status, error)
func (c *Client) Seek(ctx context.Context, t Target, r SeekRequest) (Status, error)
func (c *Client) Volume(ctx context.Context, t Target, r VolumeRequest) (Status, error)
func (c *Client) Status(ctx context.Context, t Target) (Status, error)
```

错误语义：HTTP 非 2xx 或 `ok=false` → 返回携带响应体 `error` 字段的 `*ProtocolError`；网络失败 → 原样返回 `*url.Error`（Manager 据此判离线）。

- [ ] **Step 1: 写失败测试**（用 `httptest.Server` 模拟渲染端，断言：方法与路径、Bearer 头、请求体 JSON 与 `testdata/contract/play_request.json` 一致、响应映射、`ok=false` → `ProtocolError.Error` 透传、连接拒绝 → error 判离线）

```go
func TestPlay_SendsContractRequest(t *testing.T) {
    var gotPath, gotAuth string
    var gotBody map[string]any
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
        json.NewDecoder(r.Body).Decode(&gotBody)
        w.Write([]byte(`{"ok":true,"state":"playing"}`))
    }))
    defer srv.Close()
    c := NewClient(srv.Client())
    st, err := c.Play(context.Background(), Target{Host: hostOf(srv), Port: portOf(srv), Token: "s3cret"},
        PlayRequest{URL: "http://192.168.1.10:7810/media/abc123", Title: "Interstellar"})
    require.NoError(t, err)
    assert.Equal(t, "/play", gotPath)
    assert.Equal(t, "Bearer s3cret", gotAuth)
    want, _ := os.ReadFile("../../../../testdata/contract/play_request.json")
    var wantBody map[string]any
    json.Unmarshal(want, &wantBody)
    assert.Equal(t, wantBody, gotBody)
    assert.Equal(t, "playing", st.State)
}
func TestPlay_AppError(t *testing.T) { /* 渲染端回 {"ok":false,"state":"error","error":"source_unreachable"} → 断言 err.Error()=="source_unreachable" 且 err 为 *ProtocolError */ }
func TestStatus_Offline(t *testing.T) { /* 关闭的端口 → 断言返回 error 且 errors.As(&url.Error{}) */ }
```

（Pause/Stop/Seek/Volume/Status 各一条同构用例，Volume 请求体对照 `volume_request.json`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cast/adapter/... -count=1`
Expected: 编译失败（包/类型不存在）。

- [ ] **Step 3: 最小实现**

`adapter.go` 定义上述全部类型与 `Target.BaseURL()`；`client.go` 用一个私有 `do()` 方法统一：构造请求（JSON body、Bearer 头）→ 发送 → 非 2xx 读 body 中的 `error` → `ok=false` 时返回 `&ProtocolError{Op: path, Msg: errField}` → 2xx 解析为 `Status`（play/pause 等响应只含 ok/state，映射为 `Status{State: resp.State}`）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cast/adapter/... -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/cast/adapter && git commit -m "feat(adapter): latticecast protocol client with contract-backed tests"
```

---

### Task 5: 假渲染端 + 适配器一致性测试

**Files:**
- Create: `internal/cast/testsupport/fakerenderer/fake.go`
- Test: `internal/cast/testsupport/fakerenderer/fake_test.go`

**Interfaces:**
- Produces:

```go
package fakerenderer
// 一个内存态渲染端：实现 protocol.md 全部端点，维护状态机 idle→playing⇄paused→idle、error。
type Fake struct{ *httptest.Server }
func New(token string) *Fake        // 随机端口
func (f *Fake) Target() adapter.Target
func (f *Fake) SetPlaying(url, title string, durationMS int64) // 预置播放态（测试用）
func (f *Fake) FailNextPlay(msg string)                            // 注入 /play 失败
```

Task 7（发现 e2e）、Task 10/11/12 复用；Task 13 的 Kotlin 契约测试逻辑上与之对齐。

- [ ] **Step 1: 写失败测试**（状态机：初始 status=idle；play 后 status=playing 且 title/url 生效；pause→paused；stop→idle；FailNextPlay 后 /play 返回 ok=false,state=error；无 token 返回 401）

- [ ] **Step 2: 跑测试确认失败**（包不存在）

- [ ] **Step 3: 实现 Fake**（`http.ServeMux` 五个端点 + `sync.Mutex` 保护的 state；play 校验 URL 非空）

- [ ] **Step 4: 一致性循环**（在 `fake_test.go` 中加 `TestClientConformance`：用 Task 4 的 `adapter.Client` 对 Fake 跑 play→status→pause→seek→volume→stop 全链，断言每步 Status；这是 spec"适配器一致性套件"的落点，未来 DLNA/树莓派适配器复用同一套调用序列）

- [ ] **Step 5: 跑测试通过后提交**

```bash
git add internal/cast/testsupport && git commit -m "feat(testsupport): in-memory fake renderer + adapter conformance test"
```

---

### Task 6: 配置加载（TDD）

**Files:**
- Create: `internal/cast/config/config.go`
- Test: `internal/cast/config/config_test.go`、`internal/cast/config/testdata/config.yaml`

**Interfaces:**
- Produces:

```go
package config
type Renderer struct {
    Room  string `yaml:"room"`
    Host  string `yaml:"host,omitempty"` // 留空 = 仅 mDNS 发现
    Port  int    `yaml:"port,omitempty"` // mDNS 发现时可不填
    Token string `yaml:"token"`
}
type Config struct {
    MCPListen       string              `yaml:"mcp_listen"`        // 默认 ":7800"
    MediaListen     string              `yaml:"media_http_listen"` // 默认 "0.0.0.0:7810"
    MediaBaseURL    string              `yaml:"media_base_url"`    // 渲染端访问媒体服务的基址，如 http://192.168.1.10:7810；必填
    MediaLibrary    []string            `yaml:"media_library"`
    YtDlp           string              `yaml:"yt_dlp"`            // 二进制路径；空=禁用 YouTube
    AuthToken       string              `yaml:"auth_token"`        // MCP Bearer（v1 静态）
    Renderers       map[string]Renderer `yaml:"renderers"`         // key = mDNS 实例名 = 设备身份
}
func Load(path string) (Config, error)  // 解析 + 默认值填充 + 必填校验（auth_token、media_base_url、renderers 非空）
```

- [ ] **Step 1: 失败测试**：testdata 覆盖三例——合法配置（断言默认值填充）、缺 `auth_token` 报错、未知字段报错（`yaml.Decoder.KnownFields(true)`）
- [ ] **Step 2: 确认失败** → **Step 3: 实现** → **Step 4: 通过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(config): yaml config with defaults and strict fields"
```

---

### Task 7: mDNS 发现 + 房间归位（TDD）

**Files:**
- Create: `internal/cast/discovery/discovery.go`
- Test: `internal/cast/discovery/discovery_test.go`

**Interfaces:**
- Consumes: `config.Renderers`（房名/token）、`grandcat/zeroconf`。
- Produces:

```go
package discovery
type Found struct {
    Name string        // mDNS 实例名（设备身份，须命中 config.Renderers 的 key 才采纳）
    Host string; Port int
    Room string        // TXT room=，配置优先
}
// Browse 阻塞式枚举一次当前可见渲染端（内部 3 秒窗口）；cfg 中带 host 的条目无条件并入结果（静态覆盖，供 CI/无组播环境）。
func Browse(ctx context.Context, cfg config.Config) ([]Found, error)
```

- [ ] **Step 1: 失败测试**（两例：①静态条目直通——不依赖组播，CI 可跑；②`if !testing.Short()`：`zeroconf.Register` 起一个假注册（借 Task 5 Fake 的端口），Browse 应发现它且 `room` 来自 TXT；未在 `config.Renderers` 注册的实例被忽略）

- [ ] **Step 2: 确认失败** → **Step 3: 实现**（`zeroconf.Browser`；Host 取 `entry.AddrIPv4[0]`；`ctx` 超时 3s）→ **Step 4: `go test ./... -count=1`（全量）与 `-short`（跳过 mDNS 例）都过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(discovery): mdns browse for _latticecast._tcp with config room mapping"
```

---

### Task 8: 媒体库索引/检索 + 内网媒体 HTTP 服务（TDD）

**Files:**
- Create: `internal/cast/resolve/library.go`、`internal/cast/resolve/mediaserver.go`
- Test: `internal/cast/resolve/library_test.go`、`internal/cast/resolve/mediaserver_test.go`

**Interfaces:**
- Produces:

```go
package resolve
type Item struct {
    ID    string `json:"media_id"` // sha1(绝对路径) 前 12 位
    Title string `json:"title"`    // 去扩展名文件名
    Kind  string `json:"kind"`     // video|audio|image
    Path  string `json:"-"`        // 不外泄
}
type Library struct{ /* dirs、index、mu */ }
func NewLibrary(dirs []string) *Library
func (l *Library) Rescan() error            // 递归扫描 mp4/mkv/mov/mp3/flac/wav/m4a/jpg/png，重建 index
func (l *Library) Search(q string) []Item   // 文件名大小写不敏感子串匹配，按 Title 排序，至多 20 条
func (l *Library) Get(id string) (Item, bool)
func (l *Library) MediaURL(baseURL, id string) string // baseURL + "/media/" + id
type MediaServer struct{ /* http.Server, 只读挂 Library */ }
func NewMediaServer(lib *Library, listen string) *MediaServer // GET /media/{id} → http.ServeFile；404 未知 id
func (s *MediaServer) Start() error; func (s *MediaServer) Close() error
```

- [ ] **Step 1: 失败测试**（t.TempDir 造 3 个假媒体文件：`Interstellar.mp4`、`interstellar-2.mp4`、`notes.txt`——Rescan 后 Search("interstellar") 命中 2 条且不含 txt；MediaURL 拼接正确；MediaServer 用 `http.Get` 拉 ID 能取回文件内容、未知 ID 404）
- [ ] **Step 2: 确认失败** → **Step 3: 实现** → **Step 4: 通过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(resolve): media library index/search + lan media file server"
```

---

### Task 9: 直链透传 + YouTube 提流（TDD）

**Files:**
- Create: `internal/cast/resolve/resolve.go`、`internal/cast/resolve/ytdlp.go`
- Test: `internal/cast/resolve/resolve_test.go`

**Interfaces:**
- Consumes: `Library`。
- Produces:

```go
package resolve
type Source struct { URL, Kind string } // Kind: direct|youtube|nas
type Extractor interface { Extract(ctx context.Context, pageURL string) (string, error) }
func NewYtDlp(bin string) *YtDlp // Extract: exec `bin -f "best[ext=mp4]/best" -g pageURL`，取 stdout 首行；非 0 退出返回 error
type Resolver struct { Lib *Library; Base string; Ext Extractor /* 可 nil=禁用 */ }
func (r *Resolver) ByID(ctx context.Context, id string) (Source, error)   // nas
func (r *Resolver) ByURL(ctx context.Context, raw string) (Source, error) // youtube 域名(youtube.com/youtu.be)→Ext；其余透传 direct；Ext 为 nil 且命中 youtube → error "youtube_disabled"
```

- [ ] **Step 1: 失败测试**（YtDlp 用 `t.TempDir` 里一个假 shell 脚本当"yt-dlp"打印固定 URL，断言命令行参数与解析；ByURL 三分支 + youtube_disabled；ByID 未知 id 报错）
- [ ] **Step 2: 确认失败** → **Step 3: 实现** → **Step 4: 通过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(resolve): url passthrough + yt-dlp stream extraction"
```

---

### Task 10: 设备管理器 + 本地审计（TDD）

**Files:**
- Create: `internal/cast/manager/manager.go`、`internal/cast/manager/audit.go`
- Test: `internal/cast/manager/manager_test.go`

**Interfaces:**
- Consumes: `adapter.Client/Target/Status`、`discovery.Browse`、`config.Config`。
- Produces:

```go
package manager
type Device struct {
    Name string `json:"name"`; Room string `json:"room"`
    Online bool `json:"online"`
    NowPlaying string `json:"now_playing,omitempty"`; State string `json:"state,omitempty"`
}
type Manager struct{ /* cfg, client *adapter.Client, lib *resolve.Library, res *resolve.Resolver, audit *AuditLog, mu */ }
func New(cfg config.Config, lib *resolve.Library, res *resolve.Resolver) *Manager
func (m *Manager) Refresh(ctx context.Context)                 // discovery.Browse → 更新 host/port/online
func (m *Manager) List(ctx context.Context) ([]Device, error)  // Refresh 后逐台 Status（离线标 online=false）
func (m *Manager) Play(ctx context.Context, name string, r adapter.PlayRequest) (adapter.Status, error)
func (m *Manager) Stop/Volume/Status(ctx, name string, ...)    // 未知 name → error "unknown_device"
type AuditEntry struct { TS time.Time; Agent, Tool, Args, Result string; OK bool; DurationMS int64 }
type AuditLog struct{ /* mu, f *os.File */ }
func OpenAudit(path string) (*AuditLog, error)                 // JSONL 追加
func (a *AuditLog) Record(agent, tool, args, result string, ok bool, dur time.Duration)
```

- [ ] **Step 1: 失败测试**（用 Task 5 Fake + 静态渲染端配置：List 返回 1 台在线且状态正确；停掉 Fake 的 http.Server 后 List 标 offline；Play 正确路由到该设备；未知设备报 unknown_device；Record 写出的每行是合法 JSON 且字段齐）
- [ ] **Step 2: 确认失败** → **Step 3: 实现** → **Step 4: 通过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(manager): device registry with playback ops and jsonl audit log"
```

---

### Task 11: MCP 服务器 + 六工具（TDD）

**Files:**
- Create: `internal/cast/mcpserver/server.go`、`internal/cast/mcpserver/auth.go`
- Test: `internal/cast/mcpserver/server_test.go`

**Interfaces:**
- Consumes: `manager.Manager`、`resolve.Library/Resolver`。
- Produces:

```go
package mcpserver
type Authenticator interface { Validate(r *http.Request) (agent string, ok bool) }
func StaticToken(token string) Authenticator
// New 返回已挂 6 个工具的 *mcp.Server；HTTP() 返回套了 auth 中间件的 streamable http.Handler（401: {"error":"unauthorized"}）。
func New(mgr *manager.Manager, lib *resolve.Library, res *resolve.Resolver, audit *manager.AuditLog, auth Authenticator) *Server
func (s *Server) HTTP() http.Handler
```

工具定义（名称/参数/返回固定，LLM 侧契约）：
- `list_cast_devices()` → JSON 数组 `manager.Device`；
- `search_media(query)` → `[]resolve.Item`；
- `cast_play(device, media_id?, url?, title?)` → `{"status":…,"adapter":"latticecast"}`；`media_id` 走 `Resolver.ByID`、`url` 走 `ByURL`，二者必传其一，都传/都不传 → `{"error":"media_id_or_url_required"}` / `{"error":"media_id_url_exclusive"}`；`resolve` 错误原样透传（含 `youtube_disabled`）；
- `cast_stop(device)`、`cast_status(device)` → `{"state":…,…}`；
- `cast_volume(device, level 0-100)` → 越界 → `{"error":"level_out_of_range"}`。
- 每次工具调用经 `audit.Record`（agent 取自 Authenticator）。

- [ ] **Step 1: 失败测试**（`httptest.Server(s.HTTP())`：无 token 401；用 go-sdk 的 client（`mcp.NewClient` + streamable client transport）连本服务器，initialize 后逐工具调用，断言上面每个分支——含 `media_id_url_exclusive`、`level_out_of_range`、`youtube_disabled`、audit 文件行数随调用增长）
- [ ] **Step 2: 确认失败** → **Step 3: 实现**（`mcp.NewServer(&mcp.Implementation{Name:"lattice-cast",Version:"0.1.0"}, nil)`；`mcp.AddTool` × 6；handler 出入参用 `mcp.CallToolParamsFor[T]`/`CallToolResultFor[T]`，版本漂移时按 Global Constraints 调整签名）→ **Step 4: 通过**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(mcp): six cast tools behind bearer auth with audit recording"
```

---

### Task 12: cmd/lattice-cast 组装 + CI 端到端

**Files:**
- Create: `cmd/lattice-cast/main.go`、`internal/cast/e2e/e2e_test.go`

**Interfaces:**
- Consumes: 前 11 个任务全部公开接口。
- Produces: 可执行文件 `lattice-cast`（用法 `lattice-cast -config config.yaml`；SIGINT/SIGTERM 优雅停：先停 MCP、再停媒体服务）；`internal/cast/e2e` 全链路测试。

- [ ] **Step 1: 失败测试 `e2e_test.go`**（静态渲染端条目指向 Task 5 Fake；Rescan 临时媒体目录；通过 MCP HTTP 客户端执行 `list_cast_devices` → `search_media` → `cast_play` → `cast_status`（断言 state=playing）→ `cast_stop`（idle）；审计文件 ≥5 行。全程不依赖组播，CI 可跑）
- [ ] **Step 2: 确认失败** → **Step 3: 实现 main.go**（装配顺序：config.Load → manager.OpenAudit → Library.Rescan（失败仅日志不退出）→ MediaServer.Start → manager.New → mcpserver.New → `http.ListenAndServe(cfg.MCPListen)`；各组件错误走 `log.Fatal` 前先 Close 已启动组件）→ **Step 4: `make verify` 全绿**

- [ ] **Step 5: 提交**

```bash
git commit -am "feat(cmd): assemble lattice-cast binary + ci-safe e2e test"
```

---

### Task 13: APK 渲染端运行时（协议实现 + 契约测试 + mDNS + 自启）

**Files:**
- Modify: `android/app/build.gradle.kts`（加依赖）、`AndroidManifest.xml`
- Create: `android/app/src/main/java/io/lattice/cast/PlaybackController.kt`、`RendererServer.kt`、`NsdAnnounce.kt`、`BootReceiver.kt`、`Prefs.kt`
- Test: `android/app/src/androidTest/java/io/lattice/cast/RendererServerTest.kt`

**Interfaces:**
- Consumes: `docs/protocol.md`、`testdata/contract/*.json`。
- Produces: APK 内嵌 HTTP 服务器（默认端口 7822）实现协议五端点；NsdManager 注册 `_latticecast._tcp`（TXT `room`/`v=1`）；开机自启。

- [ ] **Step 1: 依赖与 Manifest**

`build.gradle.kts` 追加:

```kotlin
implementation("org.nanohttpd:nanohttpd:2.3.1")
implementation("androidx.media3:media3-exoplayer:1.4.1")
implementation("androidx.media3:media3-ui:1.4.1")
androidTestImplementation("androidx.test.ext:junit:1.2.1")
androidTestImplementation("com.squareup.okhttp3:okhttp:4.12.0")
```

Manifest 的 `<application>` 内追加（含 `FOREGROUND_SERVICE`、`RECEIVE_BOOT_COMPLETED` 权限与 receiver 声明）。

- [ ] **Step 2: 【测试先行】androidTest 契约测试**

`RendererServerTest.kt`（instrumented，读 `assets/contract/` 下复制的契约夹具——Task 13 里先手工拷贝夹具文件进 `assets`）:

```kotlin
@Test fun playRequestMatchesContract() {
    val ctl = FakeController()                 // 测试替身：记录调用并返回固定 Status
    val srv = RendererServer(ctl, token = "s3cret", port = 0); srv.start()
    val req = Request.Builder().url("http://127.0.0.1:${srv.port}/play")
        .header("Authorization", "Bearer s3cret")
        .post(assets_contract_json("play_request.json").toRequestBody("application/json".toMediaType()))
        .build()
    val body = OkHttpClient().newCall(req).execute().body!!.string()
    assertEquals(contract_json("play_response.json"), JSONObject(body).toString())
    assertEquals("http://192.168.1.10:7810/media/abc123", ctl.lastUrl)
}
```

（另四条：错 token → 401 `{"ok":false,"error":"unauthorized"}`；`/status` 对照 `status_response.json`；错误态对照 `status_error_response.json`；`/volume` 对照 `volume_request.json`。）

- [ ] **Step 3: 跑测试确认失败**：`./gradlew connectedDebugAndroidTest`（无设备时在模拟器 TV 镜像上跑）
- [ ] **Step 4: 实现**

`PlaybackController.kt`（ExoPlayer 包装，全部操作切主线程，`@MainThread` 约定 + `Handler(Looper.getMainLooper())`）:

```kotlin
class PlaybackController(private val player: ExoPlayer) {
    fun play(url: String, title: String?, positionMs: Long): String { /* setMediaItem(MediaItem.fromUri(url)); prepare(); playWhenReady=true; seekTo(positionMs); return okState("playing") */ }
    fun pause(): String; fun stop(): String; fun seek(ms: Long): String; fun volume(level: Int): String /* player.volume = level/100f */
    fun status(): String /* state 由 player.playbackState+isPlaying 映射，error 取 playerError?.message ?: "" */
}
```

`RendererServer.kt`（NanoHTTPD：鉴权→路由五端点→JSON 响应，字段名与 `docs/protocol.md` 逐一对照）；`NsdAnnounce.kt`（`NsdManager.registerService`，serviceType `_latticecast._tcp.`，TXT: room/v=1）；`BootReceiver.kt`（BOOT_COMPLETED → 启动 `MainActivity`，配 `FLAG_ACTIVITY_NEW_TASK`）；`Prefs.kt`（SharedPreferences：token/port/room）。

- [ ] **Step 5: 跑 androidTest 通过；`./gradlew assembleDebug`；提交**

```bash
git add android && git commit -m "feat(android): protocol-compliant renderer runtime (http+exoplayer+nsd+boot)"
```

---

### Task 14: APK 配置 UI + 真机联调清单

**Files:**
- Modify: `android/app/src/main/res/layout/activity_main.xml`、`MainActivity.kt`
- Create: `docs/e2e-checklist.md`

**Interfaces:**
- Produces: 可交付 APK；`docs/e2e-checklist.md`（Task 16 的执行脚本）。

- [ ] **Step 1: 首启配置界面**（token/端口/房间名三个输入框 + "保存并启动"；保存后 `Prefs` 落盘 → 重启 RendererServer → 重新 Nsd 注册；主界面常显：房间名、当前 state、now playing 标题、IP:端口——供排查）
- [ ] **Step 2: 真机冒烟**（装到电视/盒子：配 token → 电脑 `curl -H "Authorization: Bearer …" http://<tv>:7822/status` 得 `{"state":"idle",…}`；mDNS 从 `dns-sd -B _latticecast._tcp` 或 cast-agent 侧可见）
- [ ] **Step 3: 写 docs/e2e-checklist.md**（Task 16 的完整步骤，见该任务）
- [ ] **Step 4: 提交**

```bash
git add android docs && git commit -m "feat(android): first-run provisioning ui + e2e checklist"
```

---

### Task 15: lattice 主仓库策略模板文档（零代码）

**Files:**
- Create（lattice 主仓库，`new_dev` 分支）: `docs/guides/voice-assistant-policy-template.md`

**Interfaces:**
- Produces: voice-assistant 角色的 LatticePolicy 示例（spec 第九节：default-deny 下仅放行 `role=voice-assistant` agent → 网关节点 TCP 7800）。

- [ ] **Step 1: 前置检查（硬性）**：`git -C <lattice> status --porcelain` 必须为空且当前分支可切 `new_dev`；用户 WIP 存在时**跳过本任务并报告**，不得 stash/checkout。
- [ ] **Step 2: 写文档**：以现有 LatticePolicy YAML 结构（`internal/` 中 Policy CRD 的实际字段为准，先读再写）给出模板：selector `role=voice-assistant`、egress 目的地网关 overlay IP + port 7800/TCP、TTL 可选；附"为什么 default-deny"三行说明与 spec 链接。
- [ ] **Step 3: 提交**

```bash
git checkout new_dev && git checkout -b docs/voice-assistant-policy \
  && git add docs/guides/voice-assistant-policy-template.md \
  && git commit -m "docs: voice-assistant LatticePolicy template for LatticeCast v1"
```

---

### Task 16: 真机端到端验收（手动，按 docs/e2e-checklist.md 执行）

**Files:**
- Modify: `docs/e2e-checklist.md`（勾选记录）

**步骤**：
- [ ] 1. 家里：standalone 控制面 + `lattice up`（已有环境）；节点装 `lattice-cast`，写 `config.yaml`（NAS 路径、渲染端 token、`media_base_url`）
- [ ] 2. 电视装 APK、首启配置；cast-agent 日志确认发现
- [ ] 3. 手机（蜂窝网络，验证 mesh 回家）MCP 客户端连 `http://<网关overlayIP>:7800`（Bearer token）
- [ ] 4. 说/输入："把 NAS 里的 <某影片> 投到卧室电视" → 电视播出 ✔
- [ ] 5. 追问"播到哪了" → 返回进度 ✔；"停掉" → 停止 ✔
- [ ] 6. 反例各一条：投笔记本本地文件（应报"电视够不着"类错误）；拔电视电源后投（应报不在线）
- [ ] 7. 按 spec 第一节验收线逐条勾选 `docs/e2e-checklist.md`，提交

```bash
git commit -am "docs: record v1 e2e acceptance run"
```

**验收即 v1 完成。** 任何一步失败：按 spec 第八节错误矩阵定位；APK 侧载/协议问题回 Task 13；MCP 连通问题回 Task 11。

---

### Task 17: macOS/Linux 渲染端（Go + mpv）【v4 新增，无门禁依赖】

**Files:**
- Create: `internal/cast/renderer/server.go`、`internal/cast/renderer/mpv.go`、`internal/cast/renderer/announce.go`、`cmd/latticecast-renderer/main.go`
- Test: `internal/cast/renderer/server_test.go`

**Interfaces:**
- Consumes: `adapter` 类型（Status/PlayRequest 等直接 import 复用）、`docs/protocol.md`、`testdata/contract/*.json`、`github.com/grandcat/zeroconf`（Register）。
- Produces:

```go
package renderer // internal/cast/renderer/
// Controller 抽象播放后端（mpv 真实现 + 测试假实现）
type Controller interface {
    Load(ctx context.Context, url, title string) error
    Pause() error; Stop() error; Seek(ms int64) error; Volume(level int) error
    Status() adapter.Status
}
// NewServer(token, room, name string, port int, ctl Controller) *Server —— 实现 protocol.md 五端点+401/404/405/400 语义与 idle→playing⇄paused→idle/error 状态机（与 fakerenderer 同构但为产品代码）
func (s *Server) Handler() http.Handler
// Announce(ctx, name, room string, port int) (stop func(), err error) —— zeroconf.RegisterProxy "_latticecast._tcp" TXT: room, v=1
```

`cmd/latticecast-renderer/main.go`：flags `-name`（必填，mDNS 实例名=cast-agent 配置 key）、`-room`、`-token`（必填）、`-port`（默认 7822）、`-mpv`（默认 "mpv"）。mpv 控制：`mpv --input-ipc-server=<unix sock> --idle=yes --keep-open=yes --fullscreen` + JSON IPC（loadfile/pause/stop/seek/volume 属性轮询或事件）。mpv 不存在 → 启动即报错退出，错误信息给安装提示（`brew install mpv`）。

**测试策略**：`Controller` 用假实现跑协议契约（用 Task 4 的 `latticecast.Client` 从外部驱动 `Server.Handler()` —— 渲染端一致性测试，断言与 fakerenderer 相同的全序列行为 + 401/404/405）；mpv IPC 层用假 mpv 脚本（shell 假 unix-socket server）测 loadfile 参数与 status 解析；Announce 冒烟测试放 `-short` 跳过保护内。

- [ ] Step 1 RED：renderer 一致性测试（外部 client 驱动）→ Step 2 实现 Server → Step 3 GREEN
- [ ] Step 4 mpv IPC（假脚本 RED→GREEN）→ Step 5 Announce + main 装配
- [ ] Step 6 真机冒烟：本机 `brew install mpv`（若未装）→ 起两个不同 -name 的渲染端 → cast-agent `list_cast_devices` 可见两台 → 投一个 NAS 文件到 Mac 全屏播出
- [ ] Step 7 Commit：`feat(renderer): macos/linux latticecast renderer (go+mpv)` 并推送

---

### Task 18: Apple TV 渲染端（tvOS + AVPlayer）【v4 新增，含用户签名步骤】

**Files:**
- Create: `tvos/project.yml`（XcodeGen）、`tvos/LatticeCastRenderer/`（SwiftUI App：`RendererApp.swift`、`PlaybackController.swift`（AVPlayer 包装）、`RendererServer.swift`（GCDWebServer 或等价轻量 HTTP server，实现 protocol.md 五端点）、`Announcer.swift`（NSNetService 发布 `_latticecast._tcp`，TXT room/v=1）、`Prefs.swift`（token/room/name 录入 UI））
- Test: `tvos/LatticeCastRendererTests/RendererServerTests.kt`→`.swift`（复制 `testdata/contract/*.json` 进测试 bundle，逐夹具断言）

**Interfaces:** 与 APK（Task 13/14）完全同构：Bearer Token、五端点、mDNS TXT room/v=1；实例名 = App 首启录入的 name（cast-agent 配置 key，字节级一致）。

**签名约束（用户有付费账号）**：Xcode 选个人 Team → Apple TV（同网）经 Xcode 安装；分发仅限个人设备（一年有效）。不进 App Store。

- [ ] Step 1 XcodeGen 工程 + 空 App 可构建（`xcodegen generate && xcodebuild -project ... -destination 'platform=tvOS Simulator' build`）
- [ ] Step 2【测试先行】XCTest 契约测试（五端点 + 401，夹具驱动）→ RED
- [ ] Step 3 RendererServer（AVPlayer 包装 + 状态机）→ GREEN（tvOS 模拟器跑 connected test）
- [ ] Step 4 Announcer + 首启配置 UI（name/room/token）+ 常显状态
- [ ] Step 5【手动·用户】Xcode 真机装到自家 Apple TV，`dns-sd -B _latticecast._tcp` 可见
- [ ] Step 6 Commit：`feat(tvos): apple tv renderer (avplayer+netservice, contract-tested)` 并推送
