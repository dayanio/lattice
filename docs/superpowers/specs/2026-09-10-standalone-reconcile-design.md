# 单机 Reconcile：脱离 K8s 的调谐循环设计

**日期**：2026-09-10
**状态**：Draft
**关联文档**：[下一阶段规划](./2026-09-10-next-iteration-plan.md)、[PeerIdentity Zero Trust](./2026-05-30-ai-agent-zero-trust-network-design.md)、[Agent Sandbox Monitoring](./2026-05-30-ai-agent-sandbox-monitoring-design.md)

---

## 一、背景与动机

### 1.1 问题的根源

Lattice 的调谐（reconcile）体系目前完全构建在 controller-runtime 之上：

- `internal/agent/controller/run.go:145`：`ctrl.NewManager(ctrl.GetConfigOrDie(), ...)` —— **必须存在一个可连的 K8s API server**，否则启动即失败；
- `cmd/latticed/cmd/latticed.go:60`：all-in-one 守护进程的调谐层调用的正是上面这个 `Start()`。

这意味着"单机 all-in-one 模式"名不副实：**latticed 的控制面调谐仍然骑在一个 K8s API server 上**。这是 Quick Start 必须提供 `lattice-k3s`（内置整个 k3s 的容器镜像）的根本原因，也是新增功能必须同时维护"CRD 路径 + DB 路径"两套实现的根源。

与此同时，DB 路径（`internal/db/gormstore` + `internal/server/service` + server controller）只有 CRUD，没有任何调谐循环。已知的直接后果：

- **TTL auto-GC 在 DB 路径上不存在**（roadmap 遗留项，`CAPABILITIES.md` 标记 📋）；
- AgentIdentity 的 Pending/Active/Expired/Revoked 生命周期转换只在 K8s reconciler 中实现；
- PeerIdentity 的宽限期（gracePeriod）到期清空 `previousPeerRef` 只存在于 K8s 语境的设计中。

### 1.2 Reconcile 与 K8s 本无绑定

Reconcile 的本质是一个**水平触发（level-triggered）的收敛循环**：

```
循环 {
    期望状态 = 读 spec
    实际状态 = 观察真实世界（status / 系统实际配置）
    diff → 执行动作使之一致
}
```

收敛性来自三条纪律，与 K8s 无关：

1. **每次从当前状态全量重算**——重复执行无害，做错下轮自动纠正（幂等）；
2. **触发只是加速器**——变更事件让本轮立刻发生，周期性 resync 兜底保证最终一致；
3. **定时重排队**——"未来某时刻需要再调一轮"（如 TTL 到期）是一等公民。

controller-runtime 提供的 watch/workqueue/限速只是这套模式的基础设施实现。脱离 K8s，需要的全部东西是一个约百行的 loop runner 加三个触发源。

### 1.3 代码中已存在的雏形

无 K8s 的收敛循环在仓库中已出现三次，只是未上升为统一模式：

| 位置 | 模式 | 对应 reconcile 语义 |
|------|------|---------------------|
| `internal/agent/run.go:87` | 轮询 `GetNetMap` → 全量对比 → 应用本地配置 | resync + 全量重算 |
| `internal/agent/mcpproxy/policy_cache.go:148` | 15s ticker 全量拉策略、原子替换 | resync + 原子应用 |
| `internal/agent/heartbeat.go:56` | 心跳 ticker | 周期性观察 |

本设计把这些散落的实践命名为统一模式，并提供共用基础设施。

---

## 二、目标与非目标

### 目标

1. **latticed 真正单机化**：控制面调谐循环不依赖 K8s API server，`lattice-k3s` 镜像被纯 latticed 容器替代；
2. **一套调谐逻辑，两种驱动**：同一份业务收敛逻辑，单机模式由 loopRunner + gormstore 驱动，K8s 模式由 controller-runtime + CRD 驱动（冻结，不再为新功能开发）；
3. **补齐 DB 路径缺失的调谐功能**：TTL auto-GC、AgentIdentity 生命周期、PeerIdentity 宽限期；
4. **新功能停止双路径并建**：默认只在 DB 路径实现。

### 非目标

- 不重写 controller-runtime，不追求通用框架——loopRunner 只服务 Lattice 自身；
- 不支持多 latticed 实例共享一个 SQLite（单机模式假设单实例）；
- 不改变 agent 侧行为（agent 的 netmap 同步机制保持现状）；
- 不删除 K8s 部署形态的代码（冻结保留，作为未来企业适配层）。

---

## 三、总体架构

```
                        ┌──────────────────────────────────────┐
                        │           latticed（单进程）           │
                        │                                      │
  HTTP API (Gin) ──写──▶│  service 层 ──写后通知──▶ Notifee      │
                        │       │                   │          │
                        │       ▼                   ▼          │
                        │  gormstore(SQLite)          │         │
                        │                             ▼         │
                        │              ┌────────────────────────┴┐
                        │              │        LoopRunner        │
                        │              │  触发源① 变更通知(立即)    │
                        │              │  触发源② 周期 resync(兜底) │
                        │              │  触发源③ 定时堆(requeue)   │
                        │              └────────────┬─────────────┘
                        │                           ▼
                        │              Reconciler（业务收敛逻辑）
                        │                           │
                        │            读 spec / 写 status / 操作真实世界
                        └──────────────────────────────────────┘

  K8s 部署形态（冻结）：同一套 Reconciler 语义，由 ctrl.Manager + CRD 驱动（现状不动）
```

---

## 四、核心抽象

### 4.1 Reconciler 接口

新包 `internal/reconcile`（K8s-free，不引入 controller-runtime 依赖）：

```go
package reconcile

// Request 标识一类资源的一个实例。
type Request struct {
    Kind string // "LatticePolicy" / "AgentIdentity" / "PeerIdentity" / ...
    Key  string // 主键或 (tenantID, name) 复合键的编码
}

// Result 支持定时重排队，语义与 controller-runtime 的 reconcile.Result 一致。
type Result struct {
    RequeueAfter time.Duration // >0 表示"After 之后请再跑一轮"（如 TTL 到期前）
}

type Reconciler interface {
    Reconcile(ctx context.Context, req Request) (Result, error)
}
```

`Reconciler` 的实现方必须遵守三条纪律（写进 code review 检查项）：

1. **幂等**：从 spec 全量重算，不依赖上一轮的记忆；
2. **不阻塞**：慢操作（外部调用）设超时，失败返回 error 交给 runner 退避重试；
3. **不吞错**：临时性错误返回 error（runner 重试），永久性错误写 status condition 后返回 nil。

### 4.2 Store 侧接口

每个迁移的资源族定义一个窄接口，把"读 spec / 写 status"从存储实现中隔离：

```go
// 以 LatticePolicy 为例
type PolicyReader interface {
    ListKeys(ctx context.Context) ([]reconcile.Request, error)
    Get(ctx context.Context, key string) (*models.LatticePolicy, error)
    UpdateStatus(ctx context.Context, key string, status models.PolicyStatus) error
}
```

- 单机实现：gormstore（已有表结构，status 直接映射列/JSON）；
- K8s 实现：现有 `client.Client`（冻结保留，不新增）。

业务逻辑尽量复用已有的纯函数层——例如 `PolicyEvaluator.Evaluate()` 接收 `[]*infra.Policy` 普通结构体，本身已与存储解耦，两侧直接共用。

### 4.3 LoopRunner

```go
type Runner struct {
    queue    chan Request
    dirty    map[Request]struct{} // 去重合并：同一 key 待处理只保留一份
    timers   timerHeap            // RequeueAfter / 到期时间的定时堆
    notifee  *Notifee             // 供 service 层写入后通知
    resync   map[string]time.Duration // per-Kind 兜底周期，默认 30s
}

// Register 注册一个 Reconciler 及其 key 枚举器
func (r *Runner) Register(kind string, rec Reconciler, lister KeyLister) error

// Notify 由 service 层在成功写入后调用，立即触发该 key 的下一轮
func (r *Runner) Notify(kind, key string)

// Start 阻塞运行；Stop 优雅退出（复用现有 signal 处理）
func (r *Runner) Start(ctx context.Context) error
```

语义约定：

- **每个 key 串行**：同一 Request 不会并发执行两轮（对齐 workqueue 的 per-key 独占语义），不同 key 并行（worker 数默认 4，可配）；
- **合并（coalesce）**：处理期间的多次 Notify 合并为处理结束后的一轮，避免惊群；
- **退避**：error 后按 `100ms → 1s → 5s → 30s`（上限 30s）退避重试，成功后归零——限速重试的最简等价物；
- **resync 兜底**：即使通知全部丢失，每个 Kind 按 `resync` 周期全量枚举 key 入队，保证最终一致；
- **RequeueAfter**：放入定时堆，到点入队；若期间 key 被正常触发，堆中条目作废（幂等使重复无害）。

### 4.4 变更通知

单机模式下 latticed 是单进程，通知走进程内调用即可：

```go
// service 层示例
func (s *PolicyService) Update(ctx context.Context, ...) error {
    if err := s.store.UpdatePolicy(...); err != nil { return err }
    s.notifier.Notify("LatticePolicy", key) // 写成功 → 立即触发一轮
    return nil
}
```

通知丢失不破坏正确性（resync 兜底），只影响收敛延迟。

### 4.5 与 controller-runtime 的关系

- `internal/agent/controller/`（manager 体系）**原样保留**，仅由 `cmd/manager` 与"K8s 部署形态的 latticed"使用；
- latticed 增加启动分支：

```
if flags.K8sMode {                      // 未来企业形态，冻结
    return controller.Start(flags)      // 现有 GetConfigOrDie 路径
}
return standalone.Start(store, runner)  // 默认：gormstore + LoopRunner
```

迁移期间 `K8sMode` 默认 false；`GetConfigOrDie` 从默认路径中消失。

---

## 五、首个案例：TTL auto-GC

选它的原因：既是 roadmap 遗留项，又是最简单的收敛语义，适合验证模式。

**资源与规则**（与 K8s 路径现行语义一致）：

| 资源 | 到期行为 | 来源语义 |
|------|---------|---------|
| LatticePolicy | `expiresAt` 过期 → phase=`Expired`（随后可被 GC 删除） | `LatticePolicy.ExpiresAt` |
| AgentIdentity | `expiresAt` 过期 → phase=`Expired`，按 roadmap 自动删除 | `AgentIdentity.ExpiresAt` |
| PeerIdentity | `gracePeriodExpiresAt` 过期 → 清空 `previousPeerRef`，触发受影响策略重调和 | peer-identity 设计文档 |

**实现**（示例：AgentIdentity GC）：

```go
type AgentIdentityGC struct{ store IdentityReaderWriter }

func (g *AgentIdentityGC) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
    id, err := g.store.Get(ctx, req.Key)
    if err != nil { return reconcile.Result{}, err }
    if id.Spec.ExpiresAt == nil || id.Status.Phase == "Revoked" {
        return reconcile.Result{}, nil // 无 TTL，无需重排
    }
    until := time.Until(id.Spec.ExpiresAt.Time)
    if until > 0 {
        return reconcile.Result{RequeueAfter: until + time.Second}, nil // 未到期：到点再来
    }
    return reconcile.Result{}, g.store.Expire(ctx, req.Key) // 到期：转换 phase（+按策略删除）
}
```

配合注册时的 key 枚举器与 30s resync，即使定时堆全部丢失，过期资源最迟一个 resync 周期内被处理。

**验收**：创建 TTL=60s 的 AgentIdentity → 60s 后 phase=Expired → 按 roadmap 规则删除；SQLite 中无残留；期间进程重启不漏处理（resync 兜底）。

---

## 六、迁移计划

按"依赖最少、语义最简单"排序，每步独立可交付：

| 阶段 | 内容 | 说明 |
|------|------|------|
| M1 | `internal/reconcile` 包：Request/Result/Reconciler/Runner/Notifee + 单测（fake store + fake clock） | 纯基础设施，~1 周 |
| M2 | TTL auto-GC 三类资源接入 | roadmap 遗留项清零；latticed 默认路径不再因 TTL 依赖 K8s |
| M3 | LatticePolicy 调谐接入（身份解析 identityRef / labelSelector → 防火墙规则分发） | 复用 `PolicyEvaluator`；通知挂在 policy/token/peer 的写路径上 |
| M4 | Peer/PeerIdentity/AgentIdentity 状态机接入 | 注册/心跳驱动 phase 转换；宽限期清空 |
| M5 | latticed 启动分支切换（默认 standalone）+ Quick Start 替换 `lattice-k3s` | 对外叙事：单二进制名副其实 |

每阶段的 K8s 路径保持可用（冻结回归），`make test` 与 standalone e2e 双绿后合并。

---

## 七、可观测性

- 每类 Reconciler 暴露：`reconcile_total`、`reconcile_errors_total`、`reconcile_duration_seconds`、`queue_depth`（对齐 controller-runtime 现有指标名，便于复用告警模板）；
- 收敛延迟 SLA：写后通知路径 P99 < 2s；仅 resync 路径 < resync 周期 × 2。

---

## 八、测试策略

- **单测**：fake store + 可注入时钟，覆盖幂等（连续两轮无副作用）、退避、合并、RequeueAfter；
- **故障注入**：Reconcile 返回 error 后的重试序列；Notify 丢失后 resync 收敛时间；
- **e2e（standalone）**：`latticed`（无 K8s）+ 两个 agent 容器：建网 → 下发策略 → TTL 到期自动清理 → 策略变更收敛到 iptables；
- **K8s 回归**：现有 k3d e2e 保持通过（冻结路径不回归）。

---

## 九、风险与对策

| 风险 | 对策 |
|------|------|
| 双路径逻辑漂移（同一语义两处实现不一致） | 业务核心收敛逻辑收敛到纯函数层（如 PolicyEvaluator 模式），两个 driver 只做 IO 适配；code review 检查项 |
| 通知风暴（批量导入资源） | dirty set 合并 + per-key 串行已天然抑制；必要时按 Kind 加最小触发间隔 |
| 定时堆内存（海量长 TTL key） | TTL 场景可退化为纯 resync 扫描（扫描 SQL 带 `expires_at < now` 条件），定时堆只作低延迟优化 |
| 迁移期间行为不一致引发用户困惑 | CAPABILITIES.md 每阶段同步标注"该能力在 standalone 模式的可用性" |

---

## 实现范围清单

1. `internal/reconcile` 新包（Runner/Notifee/接口定义）
2. `internal/db/gormstore` 新增 status 写方法与 key 枚举
3. TTL auto-GC reconciler × 3（LatticePolicy / AgentIdentity / PeerIdentity）
4. LatticePolicy → 防火墙规则分发的 standalone reconciler
5. Peer / PeerIdentity / AgentIdentity 状态机 reconciler
6. latticed 启动分支（`K8sMode` flag，默认 standalone）
7. 指标接入与 standalone e2e

不在本设计范围内：多实例部署一致性、agent 侧 netmap 机制改造、K8s 路径新功能、CRD 适配层实现（企业需求出现时另行设计）。
