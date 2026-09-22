# LatticeRun——一条命令安全运行任意 AI Agent（开发者沙箱 + 策略模板生态）设计

> 日期: 2026-09-19
> 性质: 特性设计（feature spec）——v1 为实现规范，v2/v3 为方向性规划，各自实施前可再出细化 mini-spec
> 关联: `2026-05-16-lattice-future-vision-and-roadmap.md`（路线 A 的开发者前门）、
>       `roadmap.md`（消化 #5 域名级出口过滤，衔接 #8 SandboxPod、#11 microvm）、
>       `2026-05-09-ai-agent-isolation-design.md`（AgentIdentity/ExecuteTool 是 mesh 模式的上游）、
>       `2026-09-11-personal-mode-and-ai-trust-layer-design.md`（standalone 模式先例）、
>       `2026-09-18-latticecast-design.md`（双仓库策略与"咬死一条线"先例）
> 状态: 待评审

## 修订记录

- **v1（2026-09-19）**: 初稿。定位为愿景文档路线 A（AI Agent 安全运行时）的自下而上入口：单机、零控制面、免费、profile 模板生态。将 roadmap #5（域名级出口过滤）拉前并入本特性 v1——没有域名级白名单，profile 无法"说人话"，生态无从谈起。
- **v2（2026-09-19）**: 增补全平台隔离分级（第三节）——gVisor 拆为 Sentry（进程隔离，Linux-only）与 netstack（网络栈，跨平台）两个能力分别决策：profile 全平台、隔离如实分档（Linux gVisor 完整档；macOS/Windows 默认 netstack-only 档，`--strict` 走 micro-VM 完整档）。macOS 需求澄清结论、开源边界表、v2 规划、风险表与非目标同步更新。

---

## 一、目标与定位

**一句话**：`lattice run --profile claude-code -- claude-code "修掉这个 bug"`——开发者一条命令把任意 AI Agent 装进零信任沙箱：域名级出口白名单 + 默认拒绝 + 全量审计 + 会话报告，零控制面、离线可用、全功能免费。

**用户旅程（v1 验收线）**：

1. 开发者 `brew install lattice`（或 `go install` / curl 脚本 / Docker 镜像），Linux 原生、macOS 走 Docker；
2. `lattice run --profile npm-ci -- npm install && npm test`——首次运行提示内置 profile 已就位，无需任何注册、token、控制面；
3. Agent 运行期间：registry.npmjs.org、api.github.com 正常放行；agent 被 prompt injection 后试图 `curl evil.com` → 网络层直接阻断（不是靠模型自觉）；
4. 进程退出，终端打印**会话报告**：放行/阻断的连接按域名统计、违规尝试明细、时长、profile 版本；同时落盘 JSONL 审计文件；
5. 开发者把 Markdown 报告贴进 PR/安全评审——报告即传播物；
6. 想要更多？`lattice profile search/install` 从社区注册表 `lattice-profiles` 拉模板；企业用户后续把同一份 profile 带进 mesh 模式，接 AgentIdentity + 集中审计（Pro 升级路径）。

**对 Lattice 的战略意义**：愿景文档路线 A 的企业销售是自上而下，本特性是同一引擎的自下而上入口——单机免费版建立信任与安装基数，开发者带着用惯的 profile 自然滑入企业版（组织分发、集中审计、Fleet 管理）。**profile 注册表是生态飞轮**：每多一个社区模板，下一批用户的冷启动成本就降一格；模板生态一旦形成，"AI Agent 出口管控的事实标准格式"这一位置就被占住了——这是比产品功能更难复制的资产。

**竞争卡位**：E2B/Daytona 卖的是云沙箱 API（Agent 的宿主），LatticeRun 卖的是本地网络层管控（Agent 的防火墙）——不抢 Agent 跑在哪，只管 Agent 连出去什么。与各家内置 sandboxing（如 Claude Code 自带网络限制）的差异：跨工具统一、网络层强制（不依赖厂商自觉）、审计可带走、免费单机到付费组织的连续升级路径。

**定位：路线 A 的第一块交付物**。本特性不新增任何隔离原语，全部能力来自已有资产（gVisor netstack、iptables 透明拦截、shim EgressFilter、mcpproxy、fileAuditWriter），新做的只有三件事：**域名级出口（拉前 roadmap #5）、profile 格式与 CLI 体验、模板注册表**。两条边界约束：进程内工具管控（AllowedTools）不做——那是 AgentIdentity/mesh 模式的层，单机模式用 MCP proxy 覆盖同等内容；文件系统细粒度策略不做——gVisor 已提供进程级隔离，per-path 策略留给 v3 评估。

---

## 二、需求澄清结论与决策

| 问题 | 结论 |
|---|---|
| 出口白名单粒度：CIDR 还是域名？ | **域名级（v1 硬性要求）**。profile 的用户是开发者，`allow: api.anthropic.com` 是唯一能"说人话"的形式；CIDR 白名单只有安全工程师能写，生态无从谈起。技术上即 roadmap #5（netstack 内 DNS 拦截 + 动态 IP 授权 + TTL 缓存），拉前并入本特性，是 v1 最大单项风险，验收线单列（见第六节）。 |
| 能不能依赖控制面？ | **v1 主路径零控制面（`--local` 模式）**：无注册、无 token、无 NATS、无 WireGuard overlay，纯沙箱 + 互联网出口管控。理由：开发者笔记本场景装一个控制面等于劝退；现有 `sandbox run`（需 server-url + token）保留为 **mesh 模式**，语义不变，是同一命令的第二个运行档位。 |
| macOS 怎么办？ | **profile 全平台、隔离如实分级**（能力拆分与平台矩阵见第三节）。gVisor 的进程隔离部分（Sentry）Linux-only 且无跨平台可能，但核心卖点（网络管控+审计）落在纯 Go 的 netstack 上可全平台：**v1 Linux 原生（完整档）+ macOS 经 Docker**（`lattice run` 检测到非 Linux 自动给出 `--docker` 包装，复用现有 ghcr 镜像 + `--cap-add NET_ADMIN`）；**v2 macOS/Windows 原生 netstack-only 默认档 + `--strict` micro-VM 严格档**。不做 Apple 原生进程沙箱（sandbox-exec 已被弃用，Endpoint Security 超出自研边界）。 |
| 开源边界怎么切？ | **单机能力全免费（Community），组织能力收费（Pro）**。域名级出口、默认拒绝、本地审计、会话报告、profile 注册表消费、`--local` 模式 MCP proxy → Community；集中审计（NATS→latticed）、组织级 profile 私有分发、Fleet/AgentIdentity 联动、合规导出、microvm → Pro。注意：这是对现有 tier 门控的一处**反向调整**（现有 `sandbox run` 的 CIDR egress 为 Pro），理由与影响见第九节，需商业化评审确认。 |
| profile 只管什么？ | **只管网络出口与生命周期，不管进程内行为**。文件系统、工具调用语义分属 gVisor 与 MCP proxy 层；profile 保持声明式、可审计、可跨模式移植（`--local` 与 mesh 模式读同一份格式）。 |

---

## 三、总体架构

```
开发者终端（Linux 原生 / macOS Docker）
   │
   │  lattice run --profile <name> -- <agent 命令>
   ▼
┌────────────────────────────────────────────────────────────┐
│  Profile Loader                                             │
│    解析顺序：--profile 显式路径 → ~/.lattice/profiles/      │
│             → 主仓库内置 profiles/ → 注册表远程拉取          │
│    JSON Schema 校验 + SHA256 完整性校验                     │
└──────────────┬─────────────────────────────────────────────┘
               ▼
┌────────────────────────────────────────────────────────────┐
│  沙箱引擎（已有资产组装，v1 不换内核）                       │
│  ├─ gVisor netstack（用户态网络栈，零特权、无 TUN）          │
│  ├─ iptables 透明拦截（Agent 流量全量进 netstack，已有）     │
│  ├─ EgressFilter（shim.PolicyChecker）                      │
│  │    ├─ v1 新增：域名级授权（DNS 拦截 → 动态 IP 白名单）    │
│  │    └─ 已有：CIDR/端口白名单、default-deny                │
│  ├─ DNS 拦截应答器（v1 新增，netstack 内，见第六节）         │
│  ├─ mcpproxy（已有；--mcp-proxy 时工具级策略+审计）          │
│  └─ fileAuditWriter（已有；本地 JSONL）                      │
└──────────────┬─────────────────────────────────────────────┘
               ▼
   会话报告（stdout 摘要 + JSONL/Markdown 落盘）
   mesh 模式额外路径（不改语义）：注册/NATS/AgentIdentity → overlay 出口管控
```

**关键决策——两个运行档位，一个引擎**：

| | `--local`（v1 主路径，新增） | mesh（已有，保留） |
|---|---|---|
| 控制面 | 无 | server-url + enrollment token + NATS |
| 网络出口 | 互联网（按 profile 白名单） | overlay + 互联网 |
| 身份 | 匿名（本地随机会话 ID） | WireGuard 公钥 + AgentIdentity |
| 审计 | 本地 JSONL + 报告 | + NATS 集中审计（Pro） |
| 适用 | 开发者笔记本、CI | 企业内网、生产 |

两个档位共享 profile 格式、EgressFilter、审计格式——企业版拿到的不是新格式，是开发者已经用熟的那份。

### 仓库策略（主仓库 + 注册表仓库）

- **主仓库**：引擎改造（DNS 拦截、`--local` 模式）、`lattice run` / `lattice profile` 命令、profile JSON Schema、内置官方 profile（`profiles/` 目录）。理由：全部是核心 Go 代码树的能力，与"网络编排 + agent 沙箱"叙事一致。
- **新仓库 `lattice-profiles`**：社区模板注册表。理由与 lattice-cast 双仓库同理——贡献者画像不同（安全/运维背景，不懂 Go 内核代码）、更新节奏独立（模板滚动更新 vs 引擎发版）、社区 PR 不稀释主仓库评审带宽。
- **依赖方向**：主仓库运行时只依赖 profile 格式（schema），不依赖注册表仓库；注册表仓库依赖主仓库的 schema 定义。内置官方 profile 的**权威版本**放主仓库，注册表通过 CI 同步镜像——保证离线可用与"开箱即用"。

### 组件职责边界

| 组件 | 职责 | 不负责 |
|---|---|---|
| Profile Loader | 解析/校验/多来源解析顺序 | 策略执行 |
| DNS 拦截应答器 | `.内` 域名查询应答、上游转发、解析结果→动态授权登记 | 连接放行判定 |
| EgressFilter | 连接级放行/阻断（域名授权 ∪ CIDR 白名单，default-deny） | DNS 解析、审计 |
| mcpproxy | MCP 工具调用级策略与审计（可选开启） | 网络出口管控 |
| fileAuditWriter | 连接/工具事件落盘 JSONL | 报告汇总 |
| Report Generator | 会话结束汇总 JSONL → 终端摘要 + Markdown/JSON 报告 | 策略判定 |
| lattice-profiles 仓库 | 模板托管、schema 校验 CI、索引生成 | 运行时行为 |

### 全平台隔离分级（能力拆分与平台矩阵，修订 v2 增补）

**能力拆分**：gVisor 是两个东西，跨平台命运完全不同——

- **Sentry（用户态内核，进程隔离）**：依赖 Linux 的 seccomp/systrap/KVM 机制与 ELF ABI；macOS/Windows 没有等价的 syscall 截获挂载点（平台设计哲学，非工程难度——Apple 与微软的官方答案一致："想隔离就开 VM"，Virtualization.framework / WSL2）。全平台的唯一路径是"弄个 Linux 内核"（Docker / WSL2 / micro-VM）。
- **netstack（`pkg/tcpip`，用户态 TCP/IP 栈）**：纯 Go，天生跨平台。LatticeRun 的核心卖点（出口管控 + DNS 拦截 + 审计 + 报告）恰好主要落在这一半。

**结论：追求"profile 全平台"，而不是"gVisor 全平台"——隔离级别如实分档标注**：

| 平台 | 网络管控路径 | 进程隔离 | 档位与时间 |
|---|---|---|---|
| Linux 原生 | iptables REDIRECT → netstack（已有） | gVisor 完整 | v1，完整档（默认） |
| Docker / CI（任意宿主 OS） | 容器内 runsc（ghcr 镜像 + NET_ADMIN，已有） | gVisor 完整 | v1，完整档 |
| macOS 原生 | utun + 路由劫持 → netstack（CLI + sudo）；NETransparentProxy 档需 Apple 受限 entitlement，公司化后评估 | netstack-only | v2，默认档 |
| Windows 原生 | wintun（WireGuard 官方签名 DLL，开源可用，管理员权限）→ netstack | netstack-only | v2，默认档 |
| macOS / Windows `--strict` | 同上，强制要求 VM（macOS：Lima/colima，libkrun 轻量档评估；Windows：WSL2） | gVisor 完整（VM 内 runsc，GKE 验证过的成熟组合） | v2，严格档 |

- **分级标注**：profile 格式与策略/审计/报告层全部平台无关（shim 的 PolicyChecker/AuditWriter 接口本就平台中立，各平台只差"包从哪进来"一个适配器）；实际达到的隔离级别在运行前摘要与会话报告中如实标注（`isolation: gvisor | netstack-only | microvm-gvisor`）。`--strict` 语义 = 隔离达不到完整档即拒绝运行。
- **性能账**：netstack 本就在用户态；VM + gVisor 双层虚拟化的损耗主要在 VM exit 与 virtio 转发，开发场景（agent 调 API、拉包）非 I/O 密集，通常无感。
- **macOS 适配待拍板项（v2 mini-spec）**：DNS/流量拦截是复用 iOS 端手搓包解析先例（`apple/engine/packet_tun.go` 的 IP/UDP 手工解析，两端共享代码），还是桌面端直接引入 netstack？**倾向后者**——桌面端没有移动端的包体积/框架顾虑（iOS 当时是刻意不把 netstack 拖进移动框架，见 `apple/engine/provisioner.go` 注释），且 netstack 的 TCP 流拦截比手搓 IP 头/校验和可靠。

---

## 四、Profile 格式（v1）

```yaml
apiVersion: lattice.io/v1alpha1
kind: EgressProfile
metadata:
  name: claude-code
  version: 1.2.0          # 语义化；注册表按 name+version 索引
  description: Anthropic Claude Code——API + npm/GitHub 包管理出口
  maintainer: lattice-team
spec:
  isolation:
    backend: gvisor        # gvisor（v1 唯一后端）| pod（v2）| none（逃生口）
  egress:
    internet:
      defaultDeny: true
      allow:
        - domain: api.anthropic.com
          ports: [443]
        - domain: registry.npmjs.org
          ports: [443]
        - domain: github.com
        - cidr: 10.0.0.0/8     # 内网例外（显式声明，默认不存在）
        - port: 53             # DNS 基础设施类例外（配合 dns 段）
      block:
        - cidr: 169.254.169.254/32   # 元数据端点永远显式封禁（防 SSRF 窃取凭据）
    dns:
      mode: filtered          # filtered（按 allow 表过滤后转发上游）| direct | block
      servers: [system]       # system = 继承 /etc/resolv.conf
    overlay:                  # 仅 mesh 模式生效；--local 忽略
      allowCIDRs: []
  session:
    ttl: 4h
    onExpire: terminate       # terminate | drain（停止新连接，存量自然结束）
  audit:
    sink: local-file          # local-file | nats（Pro/mesh）
    path: ~/.lattice/audit/   # 默认；--audit-path 覆盖
  report:
    formats: [terminal, md]   # terminal | md | json
```

设计原则：

- **声明式、可 diff、可评审**——profile 进 Git 就是安全策略评审材料，这是"报告贴进 PR"叙事的格式基础；
- **显式封禁优先于显式放行**——`block` 段独立于 `defaultDeny`，元数据端点（169.254.169.254 等）在官方模板中强制出现，这是模板生态的安全底线（CI 校验强制项）；
- **域名授权 ≠ IP 白名单**——授权绑定"该域名经 DNS 解析出的地址上的连接"（见第六节），不落成裸 CIDR，避免 CDN 共享 IP 导致的越权放行；
- **跨档位可移植**——`spec.egress.internet` 与 `spec.egress.overlay` 分段，两档位各读各的，格式只有一份；
- **版本化 + 摘要**——审计与报告中记录 profile name+version+SHA256，事后可复现"当时跑的是哪份策略"；
- `docs/profile-spec.md` 是唯一权威定义（主仓库），Go loader 与注册表 CI 共用同一 JSON Schema 保证不漂移。

---

## 五、CLI 体验规范

```
lattice run [--profile <name|path>] [--isolated] [--ttl 30m]
            [--egress-allow ...]（内联覆盖，调试用）
            [--mcp-proxy] [--docker]（macOS 包装）
            -- <agent 命令及参数>

lattice profile list|search|install <name>|update|info <name>|validate <path>
lattice report <session-id> --format md|json
```

- `lattice run` 是 `sandbox run` 的开发者前门别名：无 `--profile` 时等价于 `--profile none`（default-deny 无白名单 = 全阻断，宁可全断不可全开）；`sandbox run` 原有 flag 全部保留、语义不变，脚本不破坏。
- 首次运行输出 profile 摘要（名字/版本/放行域名数/来源），确认策略再放行进程——**运行前可见，运行后可报告**。
- `--ttl` 到期行为遵循 profile `session.onExpire`；进程先于 TTL 退出则自然结束。
- 退出码：0 成功；非 0 = Agent 自身退出码；**策略违规不改变退出码**（报告承担披露职责），`--strict-violations` 时违规即非零（CI 用）。

---

## 六、域名级出口执行（v1 核心新增，消化 roadmap #5）

```
Agent 发起 DNS 查询（任意上游 DNS 服务器地址）
   → netstack 拦截 UDP/53 → DNS 拦截应答器
   → 按 profile dns 段处理：
       allow 表内域名 → 转发真实上游 → 应答 + 解析结果登记进 EgressFilter 动态授权表（TTL=记录 TTL，上限 5m）
       表外域名       → NXDOMAIN（filtered 模式不外泄查询）
Agent 发起 TCP 连接
   → netstack → EgressFilter 判定：
       目标 ∈ 动态授权表（未过期）∪ CIDR 白名单 → 放行
       否则 → 阻断 + 审计事件（域名未授权/直连 IP 未授权）
```

- **授权表绑定的是"域名→本次解析结果"**，TTL 取 DNS 记录 TTL（上限 5 分钟，防超长缓存固化失效 IP）；
- 已知残留风险，如实写进文档不做过度承诺：目标 IP 在授权期内被该域名之外的连接复用（同一 netstack 内）可能放行——v2 以 SNI/Host 校验收紧；对开发者威胁模型（防外泄、防 C2）够用；
- 直连 IP（Agent 硬编码 IP 不走 DNS）在 defaultDeny 下被阻断，审计可见——行为本身就是可疑信号，报告单列；
- UDP 非 53 端口默认阻断（QUIC/HTTP3 同理，官方模板注明）；ICMP/raw socket 由 gVisor netstack 天然限制，文档如实说明能力边界；
- **SLA**：域名首连放行开销 ≤ 50ms（一次上游 DNS 往返），不得引入可感知卡顿。

---

## 七、审计与会话报告

- 审计事件沿用现有 JSONL 结构（连接 allow/deny、MCP 工具调用），新增字段：`session_id`、`profile_name/version/sha256`、`run_mode: local|mesh`；
- 会话报告（进程退出时生成）：
  - **终端摘要**：时长、放行连接 Top 域名、阻断事件列表（含目标）、MCP 工具调用统计（若开启）、违规计数；
  - **Markdown 报告**：同内容 + profile 全文快照 + 环境摘要（内核/isolation backend/版本），目标是"贴进 PR 即安全评审材料"；
  - **JSON 报告**：机器可读，CI 断言用（如"阻断事件 = 0 才算过"）。
- 报告落盘 `~/.lattice/reports/<session-id>.{md,json}`，审计原始 JSONL 独立保留（报告可重放生成）。

---

## 八、模板注册表（lattice-profiles 仓库）

```
lattice-profiles/
  profiles/<name>/
    profile.yaml           # 第四节的格式
    README.md              # 人读：威胁模型说明、放行了什么、为什么
    profile.test.yaml      # 契约测试：预期放行/阻断的连接清单（CI 真连测试）
  schema/profile.v1alpha1.json
  index.json               # name、version、sha256、摘要（CLI 消费的唯一索引）
```

- **v1 信任模型**：HTTPS（GitHub raw / jsDelivr 镜像）+ `index.json` 中 SHA256 校验 + 安装后落 `~/.lattice/profiles/` 不再静默更新（`lattice profile update` 显式升级）。签名（sigstore/cosign）v2。
- **CI 门槛**（注册表仓库强制）：JSON Schema 校验、`block` 段含元数据端点封禁、契约测试通过（真实拉起沙箱按 test.yaml 逐条验证放行/阻断）、README 存在。**模板质量即生态信誉，CI 是注册表的护城河**。
- v1 随主仓库内置 5 个官方模板：`claude-code`、`openai-codex`、`generic-web`（通用 LLM API + 包管理）、`npm-ci`、`llm-api-only`（最小面）。
- 贡献流程：标准 GitHub PR → CI 全绿 → maintainers 评审威胁模型合理性（重点审 allow 表有没有过宽）。

---

## 九、开源边界与商业化衔接（待评审项集中区）

| 能力 | Community | Pro |
|---|---|---|
| `--local` 模式全部能力（域名级 egress、default-deny、本地审计、报告） | ✅ | — |
| 跨平台原生档（macOS/Windows netstack-only）与 `--strict` VM 严格档 | ✅ | — |
| profile 注册表消费 + 社区/官方模板 | ✅ | — |
| `--local` 模式 `--mcp-proxy`（单机工具级管控） | ✅（建议） | — |
| mesh 模式（overlay 组网 + AgentIdentity + enrollment） | ✅（现状） | — |
| 集中审计（NATS→latticed）、Fleet 视图、Agent 联动 | — | ✅ |
| 组织级 profile 私有分发（私有 registry、GitOps 推送） | — | ✅ |
| 合规导出、microvm 后端、HTTP L7 过滤 | — | ✅ |

- **一处反向调整需商业化确认**：现有 `sandbox run` 的 CIDR egress flag 为 Pro（tier 门控），本设计把"出口管控"整体下沉为 Community（单机与 mesh 档位一致）。理由：出口白名单是开发者飞轮的钩子，藏进 Pro 等于没有飞轮；Pro 的价值上移到"组织层"（集中、分发、合规）——与愿景路线 A"gVisor 沙箱移入 Community 建立信任"的既定判断一致，与 Priority Principles 第 2 条（核心场景不藏 Pro）一致。
- **升级叙事**：开发者在笔记本上用熟的 profile 原样带进公司 mesh——`lattice run`（local）与 `sandbox run`（mesh）读同一份格式，付费转换点是"组织要集中看、集中管"的那一刻，而非功能阉割。
- 发行渠道：v1 = Homebrew / `go install` / curl 脚本 / ghcr 镜像；GitHub Action 与 npx 包装 v2。

---

## 十、版本切分

**v1（本 spec 实施范围）**：
1. DNS 拦截应答器 + 动态授权表（netstack 内，消化 roadmap #5，Community）；
2. `--local` 运行档位（零控制面路径）；
3. Profile 格式 v1alpha1 + JSON Schema + Loader（多来源解析）；
4. `lattice run` / `lattice profile` 命令面；
5. 会话报告（terminal/md/json）+ 审计字段扩展；
6. 内置 5 个官方模板 + `lattice-profiles` 仓库（只读消费 + CI）；
7. Linux 原生 + macOS Docker 包装路径。

**v2（各自 mini-spec 后实施）**：① netstack-only 原生默认档（macOS utun + netstack、Windows wintun，见第三节平台矩阵）——优先级最高，补齐两大开发者平台的零依赖体验；② `--strict` 严格档 VM（macOS Lima/colima，libkrun 轻量档评估；Windows WSL2 指南）；③ sigstore 模板签名；④ GitHub Action（Docker 内跑 runner）；⑤ `--isolation pod`（消化 roadmap #8）；⑥ SNI/Host 校验收紧域名授权；⑦ 组织级私有 registry（Pro）；⑧ npx 包装。

**v3（方向性）**：行为分析（阻断模式聚类 → 可疑行为告警，衔接路线 C）；per-path 文件系统策略评估；TEE 证明挂接（衔接愿景场景五：验证通过才发 mesh 身份）；profile 市场化（评分/下载量）。

---

## 十一、非目标（v1 明确不做）

- 进程内工具管控语义（AllowedTools/工具白名单）——mesh 模式 AgentIdentity 层的事，单机由 `--mcp-proxy` 覆盖核心需求；
- 文件系统细粒度（per-path）策略；
- Windows 原生进程隔离（v1 不做；v2 以 netstack-only 档立项 + WSL2 严格档，见第三节平台矩阵）；
- 云端沙箱 API（那是 E2B 的赛道，不抢）；
- 替代各 Agent 厂商内置 sandboxing——共生（他们的进程内限制 + 我们的网络层强制）。

---

## 十二、风险与对策

| 风险 | 对策 |
|---|---|
| 域名级授权被 CDN 共享 IP 打穿 | 授权绑定域名解析结果而非裸 IP（第六节）；v2 SNI 校验；文档如实声明威胁模型边界 |
| gVisor 与部分 Agent runtime 不兼容（ptrace/procfs 依赖） | 兼容列表随官方模板维护；`--isolation none` 显式逃生口（输出大字警告 + 报告标注降级）；兼容问题按 issue 收敛 |
| 模板放行面过宽（生态信誉风险） | 注册表 CI 强制威胁模型 README + 契约测试；官方模板保守基线（宁可用户加白不可默认放开） |
| 与厂商内置 sandboxing 同质化 | 差异点反复强调：跨工具统一、网络层强制、审计可带走、local→mesh 升级路径；对比表进 docs |
| macOS/Windows 体验二等（v1 依赖 Docker） | v1 文档把 Docker 路径做到复制粘贴级；v2 netstack-only 原生默认档排第一位（零依赖体验），`--strict` VM 严格档随后 |
| netstack-only 档被误认为完整隔离（信任风险） | 运行前摘要与报告强制标注 isolation 级别；`--strict` 显式拒绝降级运行；官方文档写明各档威胁模型边界 |

---

## 附：验收清单（v1 出口判据）

1. Linux 裸机：`brew/go install` 后 `lattice run --profile npm-ci -- npm install` 全程可用，零控制面、零注册；
2. 官方 `claude-code` 模板下，模拟外联（curl 未授权域名）被网络层阻断、审计留痕、报告可见；
3. `lattice profile install <社区模板>` → `lattice run` 全链路走通，SHA256 校验生效；
4. 会话 Markdown 报告可直接贴进 GitHub PR 渲染正常；
5. 域名首连放行开销 ≤ 50ms（SLA 实测入 bench 套件）；
6. 现有 `sandbox run` mesh 模式行为与 flag 完全不变（回归测试覆盖）。
