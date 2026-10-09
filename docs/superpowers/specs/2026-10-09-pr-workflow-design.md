# 全 PR 化工作流设计(问题 → PR → 合并归档)

- **日期**: 2026-10-09
- **状态**: 已采纳(CLAUDE.md「Git Workflow / 工作与 PR 流程」为本规范的速查版)
- **参照**: reflux 仓(zynoio/zyno)`docs/superpowers/specs/2026-10-09-pr-workflow-design.md`,按 lattice 单仓实际适配
- **取代**: CLAUDE.md 旧「Push after commit 立即推送」直推惯例

---

## 1. 目标与原则

- **一切修复与设计有记录**:issue ↔ 设计文档 ↔ PR ↔ commit 四点成链,任一环可追溯其他环
- **单一问题入口**:bug / 功能 / 疑问统一开 issue(`.github/ISSUE_TEMPLATE/`),PR 在 body 回链它
- **对外动作两道人审闸门:开 PR 前询问,合并永远人审**:分支/commit/删分支等本地机械环节由 Claude Code 直接执行;但 `git push`(分支)与 `gh pr create` 是对外可见动作,**push + 开 PR 前必须先向用户展示草稿并征得同意**(§4);合并按钮永远是人按(或用户明示"直接合")
- **不设直推豁免**:docs-only、单行修复、hotfix 都走 PR(hotfix 打 `hotfix` 标签优先审);全 PR 化的仪式成本由 agent 承担

## 2. 入口分流(提出问题时发生什么)

```
用户提出问题
  └─► Claude 判断类型并建 issue(模板二选一)
       ├─ bug_report.md:       现象 / 复现步骤 / 期望 vs 实际 / 日志或截图 → 标 bug
       ├─ feature_request.md:  背景 / 目标 / 非目标 / 边界 → 标 design(设计类走此入口)
       └─ 微小改动(typo/单行): 可跳过 issue 直接 fix PR,PR body 必须写清动机
                                 ——记录链可以缺 issue 环,不可以缺动机
```

设计类若成规模,先出 spec(`docs/superpowers/specs/<日期>-<slug>.md`)经 PR 合并,再出实施 PR。

## 3. 两条路径

### 3.1 bug 路径

```
issue → 复现与根因(先根因后修复)
      → fix/<slug> 分支(基准 upstream/master,见 §5)
      → 修复(单 commit 原则)
      → PR(询问确认 §4;body: Fixes #issue号 + 根因一句话 + 验证方式)
      → 验证绿(make lint / make test;影响部署面时加 label,见 §6)
      → 人审合并 → 删分支(远端+本地)并切回 master 拉取最新 → issue 经 Fixes 自动关闭
```

### 3.2 feature / design 路径

```
issue → 方案讨论(需要决策时进 plan mode,方案落 plan/spec)
      → spec PR(docs/<slug> 分支,docs/superpowers/specs/;询问确认 §4;body: Ref #issue号)
      → 人审合并 spec
      → feat/<slug> 分支实施 → 实施 PR(询问确认 §4;body: Ref #issue号 + Spec: <specs 文件>)
      → 人审合并 → 删分支(远端+本地)并切回 master 拉取最新 → issue 手动关闭并写落地结论
```

小 feature(spec + 实现可同 PR)不强制拆两段:PR body 写清设计取舍即可。

## 4. push / 开 PR 前的询问(两道闸门之一)

分支与 commit 就绪后、`git push` + `gh pr create` 之前,agent 必须向用户展示并等待确认:

- 目标分支名、commit 清单(标题 + 变更摘要)
- PR 标题与 body 草稿(Fixes/Ref 回链是否正确)
- 改动可拆多个 PR 时,给拆分建议

用户可答:开 / 改完再开 / 拆 / 先不开。一次询问同时覆盖 push 分支与开 PR 两个动作。

## 5. 命名、提交与 PR 目标

- 分支:`feat/<slug>` / `fix/<slug>` / `docs/<slug>`,slug 用 kebab-case 英文(现例:`feat/tvos-cast`、`fix/docker-go-version`)
- commit:一个 feature 一个 commit(多改动攒齐一次提交)、`git commit -s`、禁 `Co-Authored-By`、禁 amend/rebase/force-push(修前提交 = 在其上新建 commit)
- PR 标题与主 commit 同格式:`type(scope): 摘要`
- **PR 目标仓与分支基准**:PR 一律开到 upstream 组织仓 `alatticeio/lattice`,**基准取 `upstream/master`**——`origin` 是 fork(`winstonfly/lattice`),其 master 常滞后;开分支/更新代码前先 `git fetch upstream`。feature 分支推到 fork(`git push origin <branch>`),从 fork 分支向 upstream/master 开 PR(与现历史一致:`Merge pull request #55 from winstonfly/feat/tvos-cast`)
- **`master` 永不直推**;`dev` 分支仅作集成验证(push dev 触发 e2e/helm/benchmark CI),不是合并目的地

## 6. 验证与 CI

- 每个 merger 前:`make lint` + `make test` 绿(客户端/前端改动加对应构建,如 `cd frontend && pnpm build`)
- 按需用 PR label 触发重 CI(见 `.github/PR_LABELS.md`):`run-docker` / `run-e2e`(k3d,~30min) / `run-helm` / `run-readme` / `run-benchmark`——e2e/helm 不必每个 PR 都跑,改动触及网络面/部署面时加
- lint 与单测已对所有 PR 自动跑(lint.yml / unit-test.yml),不用人工触发

## 7. 合并与收尾清单(每个 PR 合并前过一遍)

- [ ] 验证绿:`make lint` / `make test`(重 CI 按 §6 label)
- [ ] 人审批准,或用户明示"直接合"
- [ ] merge(维持 merge commit 方式,与现历史一致;不用 squash/rebase merge)
- [ ] 删远端分支 + 本地分支,切回 master 并 `git fetch upstream && git pull upstream master`(落地状态复位)
- [ ] issue 关闭(Fixes 自动关;design 路径手动关并附落地 commit 清单)
- [ ] **沉淀**:有复用价值的教训/决策写入 memory(`feedback`/`project` 类型),MEMORY.md 加索引行——记录链的最后一环是"下次不再踩"

## 8. 兄弟仓协作(lattice-apple / lattice-cast / lattice-copilot)

生态内其他仓(Apple 客户端、cast 协议、copilot)各自独立 git 仓,流程同本规范:分支 → PR → 人审合并,不开"直推豁免"。跨仓改动:

- 先判归属再动手,不把 A 仓的改动混进 B 仓的 commit
- 跨仓 PR 在 body 互相回链(`Ref:` / `Depends:`),合并顺序自底向上(被依赖方先合)
- 问题仍统一开 lattice 主仓 issue(lattice-apple 闭源、lattice-cast 开源——内部问题不宜全公开时尤其如此)

## 9. 发布衔接

- 发布不走路由 PR:`master` 上的 `v*` tag 触发 goreleaser(二进制)+ docker release(`release.yml`);tag 由维护者在合并后、发布检查做完再打
- Apple 端(lattice-apple 仓)TestFlight 发布见其 `docs/RELEASING.md`,与本仓发布相互独立
- 一个发布 = 一个 tag = 一段 CHANGELOG/发布说明;发出去的 tag 永不移动或删除,问题 fix forward 到下一个 PATCH

## 10. 默认决策(可评审推翻)

| 决策点 | 默认 | 理由 |
|--------|------|------|
| docs-only 微改是否也走 PR | 是 | agent 使成本≈0;记录链不断环 |
| PR 基准/目标 | upstream/master | fork 的 master 常滞后;与现合并历史一致 |
| 合并方式 | merge commit | 保留 PR 内小步历史 |
| e2e 是否每 PR 必跑 | 否(label 触发) | ~30min 太贵;网络面/部署面改动才加 |
