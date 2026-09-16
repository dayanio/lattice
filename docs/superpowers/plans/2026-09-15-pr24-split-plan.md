# PR #24 拆分为小颗粒度 PR 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把已关闭的巨型 PR #24（`dev`→`master`，141 个 dev 独有提交，81104 行）拆分成一批可审查的小 PR，且不触发与 `master` 的合并冲突。

**Architecture:** 这不是常规的"写新代码"任务，而是 git 历史重组。核心发现：`upstream/master` 自 2026-05-30（PR #28 `feat(mcp)`）之后再未更新过。dev 分支 5/16-5/30 的提交里，有 43 个已经通过其它 PR（#1-#21, #23, #25, #26, #28）以字节级相同或 squash 合并的方式进了 master——这些**直接丢弃**，不进任何新 PR。剩余 ~98 个提交（PeerIdentity/AgentIdentity 身份系统 + 2026-09-10 之后的 standalone/Apple 客户端/exit-node/LRP relay 全部工作）在 master 上完全不存在对应文件，是真正的净新增，按功能点分 9 个批次，每批从**当前 `upstream/master`**（而不是从 `dev`）拉新分支、`cherry-pick` 对应提交区间，逐批开小 PR。因为 master 三个半月没变过，这些 cherry-pick 预期无冲突或只有边界文件（`api.go`/`server.go`/`node.go`）的小冲突。

**Tech Stack:** git cherry-pick, gh CLI, make lint / make test（Go 1.25 + golangci-lint v1.64.5）。

## Global Constraints

- 所有新分支必须从 `upstream/master`（`abe3b6704d2bf2856297b75cce4444f89cd316e6`）切出，**不要**从 `dev` 切出——从 `dev` 切出会把已经废弃/重复的 43 个提交也带进去，重新引入冲突。
- 每个批次 cherry-pick 完成后必须跑 `make lint` 和相关 `make test`，通过后才能推送开 PR，这是 CLAUDE.md 的强制要求。
- Commit 归属：保持原作者 `winstonfly <lauxinchi@gmail.com>`，`git commit -s` 签名，不加 Co-Authored-By（已在 dev 分支批量清理过，cherry-pick 会保留清理后的提交信息）。
- 遇到 cherry-pick 冲突：优先保留 dev 侧的实现（它是更晚、更完整的版本），除非该文件在 master 侧的 #26/#28 里有明显更新的架构（例如 sandbox 从 gVisor 迁移到 kernel TUN），此时需人工判断，不要盲目 `--ours`/`--theirs`。
- 批次之间有依赖顺序（见下方 Task 顺序），不要并行乱序合并——后面的批次假设前面批次已经进了 `master`。
- 本地已有安全网：备份分支 `backup/dev-pre-cleanup-20260915`（指向清理 Co-Author 之前的 dev 原始状态），如果 cherry-pick 过程中怀疑内容丢失，可以随时 `git diff backup/dev-pre-cleanup-20260915 <commit>` 核对。

---

## 丢弃清单（不进任何 PR，仅记录留档）

**已验证字节级完全相同（跳过）：**
`996c7840`(#3) `2dd29336`(#1) `bfc84f96`(#2) `424c7195`(#4) `b8ac9bf1`(#9) `cda20a6a`(#10) `a6036c6c`(#11) `c446e232`(#12) `ae0404ac`(#13) `ce19a6a3`(#14) `9e6ae987`(#16) `dc3f798b`(#17) `28cb332c`(#18) `8934586f`(#19) `966a48b6`(#20) `8708dd91`(#21) `ca83babc`(#23) `aa03a852`(#25)

**已验证被 master #26/#28 squash 吸收（文件覆盖率 1/1~4/4，跳过）：**
`2446dc93` `f23f4fee` `4b896dde` `9a286faf` `1a893d2f` `aeab2fb5` `d13cdf3f` `b663313e` `34a31b9d` `f98b3e33` `a63fdc94` `c263bd19` `c611b9c4` `f2cb58af` `73cdc0bd` `157293d2` `52a9adf6` `73cc9697` `2bfba21f` `1423903e` `2e3887ac` `657f7de6` `220ccc5a` `de6812ad` `2c67c723`

**净零对（功能上线又被撤销，跳过）：**
`28ff6c27`（晶格设计语言）+ `3da59418`（其 Revert）

**⚠️ 需要人工复核后再决定（暂不进入任何批次）：**
`df97e800` `d9eb29ef`（sandbox 三层防御监控设计文档）、`1acc57a4`（netns+veth helpers）、`8e4d5da5`（agent-security 检查器/审计/风险计算）、`cb10a803`（gVisor installer/runner）——这 5 个提交是 2026-05-30 产物，但 dev 自己在 5/29 已经把 sandbox 方案从 "gVisor+netns+tproxy" 重构成了 "kernel TUN 直接路由"（见 `f2cb58af` `73cdc0bd` 两个 refactor 提交，均已确认被 master 吸收）。这 5 个提交却又引入 gVisor/netns 相关代码，方向与已采纳的架构矛盾，疑似当时的探索性分支，**不要**不加区分地 cherry-pick。

---

### Task A: PeerIdentity + AgentIdentity 零信任身份系统

**分支:** `feat/peer-agent-identity`（从 `upstream/master` 切出）

**涉及提交（9 个，按时间序）:**
```
c503d2df bc760182 b5b59af3 78b34963 ff132613 cf3fa348 b037e080 f20660ef 4b3a9f18
```

**Interfaces:**
- Produces: `PeerIdentity` / `AgentIdentity` CRD 类型（`api/v1alpha1/peer_identity_types.go`）、`internal/agent/controller/identity_resolver.go`、`internal/agent/controller/policy_evaluator.go`、前端 `frontend/src/pages/manage/peer-identities/` 与 `agent-identities/`。后续批次不依赖这部分产物，可独立审查合并。

- [ ] **Step 1: 从当前 master 切分支**

```bash
cd /Users/francis/workspc/lattice
git fetch upstream master
git checkout -b feat/peer-agent-identity upstream/master
```

- [ ] **Step 2: 依次 cherry-pick**

```bash
git cherry-pick c503d2df bc760182 b5b59af3 78b34963 ff132613 cf3fa348 b037e080 f20660ef 4b3a9f18
```

预期冲突点：`api/v1alpha1/zz_generated.deepcopy.go`、`internal/server/server/api.go`、`internal/server/server/server.go`、`internal/agent/node.go`——这些文件在 master 的 #28 里也被改过。冲突时以 dev 侧（cherry-pick 进来的这一份）为主，把 #28 已有的 MCPServer/AgentPolicy 路由/deepcopy 代码保留在旁边，两者是并列功能不是互斥修改。

- [ ] **Step 3: 冲突时逐个解决并继续**

```bash
# 编辑冲突文件后
git add <冲突文件>
git cherry-pick --continue
```

- [ ] **Step 4: 验证构建与 lint**

```bash
make lint
go build ./...
```

- [ ] **Step 5: 推送并开 PR**

```bash
git push origin feat/peer-agent-identity
gh pr create --repo alatticeio/lattice --base master --head winstonfly:feat/peer-agent-identity \
  --title "feat(identity): PeerIdentity + AgentIdentity zero-trust identity system" \
  --body "拆分自已关闭的 PR #24。引入 PeerIdentity/AgentIdentity CRD、identityRef 策略解析、Server+DB CRUD 及前端管理页。"
```

---

### Task B（跳过，仅占位提醒）: agent-security / gVisor installer / netns helpers

**不创建分支。** 先由你确认这 5 个提交（见上方"需要人工复核"清单）对应的方向是否还有效——如果确认过时，直接从 dev 分支永久移除；如果确认仍需要，单独开一个 task 补充到此计划中再执行。

---

### Task C: standalone reconcile 核心（K8s-free 控制面）

**分支:** `feat/standalone-reconcile-core`（从 `feat/peer-agent-identity` 合并后的 master，或独立从 master 切出——两者互不依赖，可并行）

**涉及提交（6 个）:**
```
9a0cf63f ca23f198 2c4ed396 de086031 347da767 41dae094
```

- [ ] **Step 1:** `git checkout -b feat/standalone-reconcile-core upstream/master`
- [ ] **Step 2:** `git cherry-pick 9a0cf63f ca23f198 2c4ed396 de086031 347da767 41dae094`
- [ ] **Step 3:** 冲突处理（预期无冲突，master 无对应文件）
- [ ] **Step 4:** `make lint && go build ./...`
- [ ] **Step 5:** push + `gh pr create`（title: `feat(standalone): K8s-free reconcile runner + peer registry`）

---

### Task D: standalone e2e + 真实 agent 连通性

**分支:** `feat/standalone-e2e`（依赖 Task C 已合并到 master，否则 cherry-pick 会失败因为引用了 C 的代码）

**涉及提交（7 个）:**
```
feeb2864 f6f6fa89 c08b29bb 4d17fdfb 352ba5de 5b4ec6c4 4fb182a3
```

- [ ] **Step 1:** 确认 Task C 已合并，`git fetch upstream master`
- [ ] **Step 2:** `git checkout -b feat/standalone-e2e upstream/master`
- [ ] **Step 3:** `git cherry-pick feeb2864 f6f6fa89 c08b29bb 4d17fdfb 352ba5de 5b4ec6c4 4fb182a3`
- [ ] **Step 4:** `make lint && make test`（这批含 e2e suite，本地可先跑 unit test，e2e 留给 CI 的 `run-e2e` 标签）
- [ ] **Step 5:** push + PR（title: `feat(standalone): zero-K8s e2e suite + real-agent convergence`）

---

### Task E: description-as-policy

**分支:** `feat/description-as-policy`（依赖 Task C/D 已合并）

**涉及提交（9 个）:**
```
e0b339bf 83ac178b 274b12ed 6b581469 6e862d6f 4740f802 bd3b7b4e cba8fd7a 1f6b6ff3
```

- [ ] **Step 1-5:** 同上模式，title: `feat(policy): description-as-policy (P1-P6) + client daemonize`

---

### Task F: Apple 客户端核心引擎（macOS/iOS）

**分支:** `feat/apple-client-core`（不依赖 C/D/E，可与它们并行）

**涉及提交（20 个）:**
```
295efcd0 10cad0fa 238e710e 88dc859b 2959f5f2 d7365d6f b1dc1df9 120085f3 f4102df2
532254dd 289f83db 977bf0f3 ea957140 a138f336 fb6b920e 77f58d07 4cccfdbf 0604dd6e
80c0c969 41a94d95
```

- [ ] **Step 1-5:** 同上模式，title: `feat(apple): macOS/iOS Network Extension client running the real engine`
- 注意：`apple/` 目录下的 `.xcodeproj`、Swift 文件走 Xcode 构建，不受 `make lint`（golangci-lint）覆盖；cherry-pick 后建议本地 `cd apple && xcodebuild -scheme LatticeMac build` 验证一次。

---

### Task G: Apple 菜单栏 UI + AI 助手

**分支:** `feat/apple-menubar-ui`（依赖 Task F 已合并）

**涉及提交（11 个）:**
```
eacd03bb 73bc9b4a c9cbd17b 4f5481b0 fd040fad de42357b 139d60aa d6c174c4 01d58540
e7a728e9 f44fd9d0
```

- [ ] **Step 1-5:** 同上模式，title: `feat(apple): menu-bar UI + AI assistant sidebar`

---

### Task H: Exit Node / Subnet Route

**分支:** `feat/exit-node-subnet-route`（依赖 Task C（standalone 后端）与 Task G（Apple UI）都已合并，因为这批同时改了后端 API 和 Apple 前端）

**涉及提交（18 个）:**
```
21f94bf3 d1c308cc 43a047fb 9ad1cf60 11ca46b7 2e9d6fed 3917ce09 7fdf13d1 81c28041
526d0c8a f9686673 de15d21b 020ef028 f28f55a5 826add3e 0671927f f7957b04 19b9bfa2
```

- [ ] **Step 1-5:** 同上模式，title: `feat(routing): Exit Node / subnet route selection (backend + apple)`

---

### Task I: 近期修复与收尾（LRP relay / auth / apple 稳定性）

**分支:** `fix/recent-stability-batch`（依赖 Task F/G/H 都已合并，这批是在它们之上的修复）

**涉及提交（10 个）:**
```
13a1e8f6 6b6a759b 0f95a3e3 309c0f5c 496a374e df558e09 abaace84 b4b4c591 ca488089 518aea38
```

- [ ] **Step 1-5:** 同上模式，title: `fix(standalone+apple): LRP in-process relay, auth token refresh, NE stability`

---

## Self-Review

**Spec coverage:** 141 个 dev 独有提交 = 43 丢弃（已验证重复/吸收）+ 2 净零对 + 5 待复核 + 91 个分入 A/C/D/E/F/G/H/I 八个批次。数字对得上（43+2+5+91=141）。

**批次依赖顺序：** A、C、F 三者互相独立可并行；D 依赖 C；E 依赖 D；G 依赖 F；H 依赖 C 和 G；I 依赖 F/G/H。建议执行顺序：A、C、F（并行）→ D、G（并行）→ E、H（并行）→ I。

**未决问题：** Task B（5 个提交）需要你确认方向是否还有效才能继续规划，目前占位跳过。
