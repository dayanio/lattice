# 客户端能力补全 P5（对外发布 v1 · 网关模式）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 发布规则注册表（服务端）+ 发布网关角色（agent 进程内的 HTTP 反代）+ 共享页真实现（App）。

**Architecture:** 网关 = 一台 mesh 成员（v1 跑在容器里）。公网 HTTP → 网关 `:8090/{name}/…` → 网关从本地 netmap 解析目标节点 overlay IP → 反代 `http://ip:port/…`。规则表 `t_publish` 由管理 API 增删；每次变更（及周期性）服务端经 NATS `lattice.signals.publishes` 广播全表，网关订阅更新，无初始拉取（等首个广播，表小、周期短）。

**Tech Stack:** Go（models/store/service/router、agent NATS 订阅 + httputil.ReverseProxy）、Swift/SwiftUI（CoreImage 二维码）。

**Spec:** `docs/superpowers/specs/2026-09-22-mac-client-capabilities-design.md` §六

## Global Constraints

同前几期（单 commit、`-s`、无 Co-Authored-By、lint、中文文案）。三处构建：`go build ./...`、`go build -o bin/latticed ./cmd/latticed`、App `BUILD`。

## Tasks

### Task 1: 服务端注册表 + API + 广播

- `internal/server/models/publish.go`（新）：`Publish` → `t_publish`（workspace_id 索引、name 唯一、peer_name、port、enabled、created_by）。migrate.go 注册。
- `internal/agent/store/store.go`：`Store` 加 `Publishes() PublishRepository`；新接口 ListByWorkspace/GetByName/Create/Delete。
- `internal/db/gormstore/publish.go`（新）+ `store.go` 装配（字段/new/accessor）。
- `internal/server/service/peer.go`：`PeerService` 接口加 `ListPublishes/CreatePublish/DeletePublish`（workspaceID 从 ctx `infra.WorkspaceKey` 取）；实现：name 正则 `^[a-z0-9-]{3,32}$`、port 1-65535、peer 存在（store.Peers().ListByWorkspace 匹配 name）；每次变更后 `p.signal.Publish(ctx, infra.PublishesChangedSubject, 表JSON)` + 周期广播由网关订阅侧容忍（服务端在 Create/Delete 时各推一次即可，v1 网关启动后 60s 内必有下一次变更——不够，**另加**：服务启动时由 handler 侧不推，网关侧轮询兜底见 Task 2）。
- `internal/agent/infra/notify.go`：`PublishesChangedSubject = "lattice.signals.publishes"` + `PublishPublishesChanged(ctx, signal, wsID, tableJSON)`。
- `internal/server/server/publish.go`（新 handler）+ `api.go` 路由组 `/api/v1/publish`（GET /list、POST ""、DELETE /:name；写操作 RoleAdmin）。
- 验证：`go build ./...`；重启 latticed 后 curl 三接口。

### Task 2: agent 网关角色

- `internal/agent/config/config.go`：`IngressAddr string \`mapstructure:"ingress-addr"\`` + `v.SetDefault("ingress-addr", "")`（env `LATTICE_INGRESS_ADDR` 即生效）。
- `internal/agent/node.go`：NewNode 里把 `natsSignalService` 存到 node 字段（`signalService`）；节点启动完成后若 `config.Conf.IngressAddr != ""` 调 `c.StartIngress(ctx)`。
- `internal/agent/ingress.go`（新）：
  - `StartIngress`：SubscribeRaw(`lattice.signals.publishes`) 更新内存表（`atomic.Value` 存 `[]PublishRoute`）；起 `http.Server{Addr: IngressAddr}`。
  - handler：`/{name}/…` 查表 → peer 名 → `GetPeerManager().GetAll()` 匹配 Name/AppID 且 Address 非空 → `httputil.NewSingleHostReverseProxy`（去 name 前缀、Host 改写、X-Forwarded-For）；未知 name=404，peer 不可解析=502 中文错误页；可选 token 规则 v1 不做（表里无字段，留 v2）。
  - 兜底：订阅前/无广播时表为空 → 全部 404；注释写明 v1 无初始拉取。
- 验证：`go build ./...` + 单测 proxy handler 的路径剥离/查表（httptest 起一个 upstream + 伪造表，断言转发与 404/502）。

### Task 3: App 共享页

- `apple/Shared/LatticeAPI.swift`：`PublishItem`（name/peerName/port/enabled）+ `listPublishes()/createPublish(name:peer:port:)/deletePublish(name:)`（request 助手已带 X-Workspace-Id）。
- `apple/LatticeMac/NetworkPages.swift` 的 `ShareView` 重写：
  - 顶部列表：每行 name + `peer:port` + 完整 URL（服务器 host + :8090）+ 复制按钮 + 二维码按钮 + 删除按钮；空态文案。
  - "新发布"表单（SheetScaffold 弹窗）：节点选择（displayPeers 里 approved 的）、端口、名称 + 保存。
  - 二维码：`CIFilter.ciQRCodeGenerator` 生成 CGImage → Image；SheetScaffold 弹窗展示 + URL 文本。
  - 保留"该能力尚在 v1：HTTP、路径前缀路由、无 TLS"的说明文案。
- 验证：`BUILD` + relaunch。

### Task 4: 端到端演示与提交

- 重建 `bin/latticed` 重启；`GOOS=linux GOARCH=arm64 go build -o /tmp/lattice-gw ./cmd/lattice`。
- 起网关容器（lattice-conn 镜像 + 覆盖 entrypoint：docker cp 新二进制 + env 同 mac-node-a + `-p 8090:8090` + `--ingress-addr :8090`）。
- curl 创建 `demo → node-a:8080`，`curl http://127.0.0.1:8090/demo/` 应返回 node-a busybox httpd 页面；DELETE 后应 404。
- `git commit -s -m "feat: publish gateway v1 ..."`（服务端 + agent + App 一期一个 commit）。

## Self-Review

- Spec §六四点：注册表/API、网关角色、Share 页、诚实边界文案 → Task 1-3 覆盖；TLS/token/自定义域名明确留 v2。
- 已知妥协（代码注释注明）：NATS 无初始拉取（等首个广播）、flat subject 不分 workspace、无鉴权 token。
- 人工验证项：App 二维码扫码真实可达性（依赖网关所在网络的公网性）。
