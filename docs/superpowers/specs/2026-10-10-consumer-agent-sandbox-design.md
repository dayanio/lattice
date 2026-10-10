# C 端 Agent 沙箱设计 — 域名级过滤 × 权限弹窗 × agent 身份

- 日期：2026-10-10
- 状态：Draft（brainstorming 多轮会话收敛，待评审）
- 涉及仓库：`lattice`（引擎 / shim / 控制面）、`lattice-apple`（Mac / iPhone 客户端 UI）
- 关联文档：
  - [国内直连分流设计](2026-09-24-domain-split-routing-design.md)（注意其 B2 结论的适用边界，见 §二 D2）
  - [出口节点路由与 DNS](../../design/exit-node-routing-and-dns.md)
  - [LatticeDNS 技术说明](../../latticedns-technical.md)
  - [gVisor 嵌入式网络设计](2026-09-23-gvisor-embedded-networking-design.md)
  - [CAPABILITIES.md](../../../CAPABILITIES.md)（缺口 #1 由本设计正式立项）

## 一、定位与问题

### 1.1 定位修正

本设计明确 **C 端定位**（个人 / 家庭用户的 Mac、iPhone、家庭服务器），不面向 B 端：

- 不做"沙箱即服务"（E2B 式的 session API、预热池、快照恢复、按秒计费是"给开发者卖 API"的生意，与 C 端无关）；
- 沙箱不做独立 SKU，作为 **Pro 订阅的核心理由之一**（"agent 上网要过审 + 家庭 mesh 细粒度授权"），Community 保留基础出网管控；
- 一句话价值主张：**"我机器上跑的 AI agent，不能背着我乱来"** —— 面向消费者的 "Little Snitch for AI agents"。

> ⚠️ 遗留事项：`docs/superpowers/specs/pricing.md`（2026-04-27）仍是 B 端模板（按节点数、SSO、SLA、客户成功经理），与 2026-05 之后的 C 端实践（personal mode、Apple 客户端、投屏）已经脱节，需按本定位重写。见 §七。

### 1.2 问题

用户在本机跑 AI coding agent（Claude Code 等），agent 的 shell 工具可以访问任意文件、连任意网站：上传代码到陌生域名、下载并执行任意包、把 API key 发往第三方。现有防护（Claude Code 内置 `/sandbox`、Anthropic devcontainer 的 iptables 白名单）要求用户手写配置，且没有 mesh 维度（"允许访问我家 NAS、永远不许碰公司资源"单机方案给不了）。

### 1.3 三个已确认的技术事实（本设计的地基）

1. **netstack 已跨平台就位**：Linux 走 `sandbox run` 的 tproxy 档（`internal/agent/tproxy` + `internal/agent/gvisor`），Apple 走 NE + 内嵌引擎（`apple/engine`）。策略与审计钩子都在 netstack 层，是天然的执行与观测点。
2. **LatticeDNS 内嵌 resolver 已存在**（engine commit 6cb51a13）：引擎自己就是 DNS 应答者，域名 → IP 映射天然可得——这是域名级过滤在引擎侧可行的关键前提。
3. **`sandbox run` 包装的 agent 天然有身份**：它不是"全机流量里猜出来的一条流"，而是 lattice 网络里一个具名、有 WireGuard 身份的成员。Mac 上"全机流量的进程归属"是出了名的难做，包装路线直接绕开它——弹窗授权有了主语，审计有了归属。

### 1.4 竞品参照

| 产品 | 做法 | 与本设计的差异 |
|---|---|---|
| Claude Code `/sandbox` | macOS Seatbelt / Linux bubblewrap，文件路径 + 网络域名白名单，要手写 settings.json | 本设计图形化 + 默认安全 + mesh 联动 + 中央审计 |
| Anthropic devcontainer | iptables 域名白名单（只放行 Anthropic API 与包仓库） | 同目标；lattice 版多身份、中央策略、审计流水 |
| Codex CLI | Seatbelt（macOS）/ Landlock+seccomp（Linux），默认断网 | 同流派（本机 OS 沙箱），本设计加 mesh 维度 |
| Little Snitch | macOS 出网防火墙 + 弹窗授权，卖了二十年的消费级付费软件 | 交互范式直接参照；无身份/mesh/agent 语义 |

## 二、决策记录

| # | 决策 | 理由 |
|---|---|---|
| D1 | **产品边界**：C 端保护功能，非独立 SKU；v1 主战场是 Mac，runsc/microVM 降为"高阶档"另行立项，不阻塞本设计 | C 端主场景是"本机跑 agent"；B 端隔离强度军备赛不是 C 端需求 |
| D2 | **域名过滤落点**：引擎 netstack 内的 DNS 拦截 + 域名→IP 动态映射（CAPABILITIES 缺口 #1 正式立项）；**与 domain-split-routing 的 B2 结论不矛盾**——B2 否决的是"引擎侧分流 DNS"（按 IP 归属做路由选择，依赖 GeoIP 判断，已实测不可行）；本设计是**访问控制**：引擎自身就是 resolver，DNS 答案可控，决策点是"这个域名允不允许连"，不依赖 IP 归属库 | 执行点与决策点都在引擎内，无 GeoIP 依赖 |
| D3 | **默认语义**：被包装运行的 agent，出网默认 **deny**（白名单制）；首连未知域名触发弹窗；未被包装的流量不受影响（保持现有 mesh 策略） | C 端默认安全；"包装"是白名单制的前提（有主语才谈得上默认拒绝谁） |
| D4 | **弹窗授权动作**：允许一次 / 总是允许 / 拒绝；"总是允许"落入该 agent 的个人策略集并同步控制面（跨设备一致） | 手机权限弹窗的成熟范式；跨设备一致是 mesh 的差异化 |
| D5 | **agent 接入形态**：v1 一律走 `sandbox run` 包装（补 `--image` / `--mount` / `--env`）；**不做**全机流量的进程归因识别 | 归因是老大难且被包装路线结构性绕开（§1.3-3）；全机归因列为远期研究，不承诺 |
| D6 | **隔离强度分层**：本设计只覆盖"网络收编 + 域名策略 + 弹窗 + 审计"（现有 netstack 档）；进程级隔离（Linux runsc / macOS-Windows VM 档）另行立项，作为 Pro 高阶档 | 本设计的价值（策略/弹窗/审计/身份）不依赖进程隔离；分档推进避免一个大 spec 卡死 |
| D7 | **Linux 档防绕与防污染以专属 netns 为先**：agent 与其 REDIRECT/TPROXY 规则整体住进专属 network namespace，**宿主机 iptables 各表零写入**；agent 进程摘除全部 caps + `no_new_privs`。iptables 劫持保留（在 netns 内），直至 runsc 档将其取代 | 一并解决两个问题：防绕（agent 无钥匙；netns 内居民唯一，无需 owner match）与防污染（宿主表不感知 lattice，与 Docker/kube-proxy/Clash 零交互；崩溃随 netns 蒸发，无孤儿规则）（详见 §4.2） |

## 三、范围：三件核心事 + 一个支撑件

### 3.1 域名级 egress 过滤（引擎，M0）

数据面路径（以包装运行的 agent 为例）：

```
agent 进程
  │ DNS 查询（resolver 指向引擎内置 DNS）
  ▼
LatticeDNS/策略 resolver：判定域名 → 允许则应答并记录 (FQDN → IP, TTL) 映射；拒绝则应答 NXDOMAIN（或可配置为空应答）
  │
  ▼
netstack 连接决策点（扩展现有 PolicyChecker）：
  目的 IP ∈ 映射 且 对应域名在策略集 → 放行
  目的 IP ∈ 映射 但域名被拒        → 拒绝（RST/ICMP unreachable）
  目的 IP ∉ 映射（未经理 DNS 的直连）→ 默认拒绝（包装模式下）
```

**IP 与域名是并存的两层规则，不是二选一**：IP 层即现有 `--egress-allow` CIDR（今天已可用），M0 在其上叠加域名维度。每条连接按序判定，四类流量都有归属：

| agent 行为 | 判定层 | 结果 |
|---|---|---|
| 经 DNS 连白名单域名（如 api.anthropic.com） | 域名策略 | 放行 |
| 硬编码 IP 连已放行 CIDR（如家庭网段 192.168.0.0/16 的 NAS） | CIDR 规则 | 放行 |
| 硬编码 IP 连陌生公网地址 | CIDR 未命中 | **默认拒绝** |
| 未包装的普通流量 | 现有 mesh CIDR 策略 | 行为不变 |

优先级规则：映射命中的连接**域名优先**（域名比 IP 具体）；未命中映射的连接 **CIDR 优先**。注意"用 IP 绕过域名封锁"仅在黑名单制下成立——本设计是白名单制（"未允许 = 拒"），硬编码 IP 是被兜住的行为而非漏网行为。

- 策略模型：per-agent 规则集，条目 = `{domain, port?, action: allow-session|allow-always|deny}`；CIDR 规则保留（现有 `--egress-allow` 语义不变），域名维度叠加在其上；
- 防绕边界（v1，诚实声明）：覆盖明文 DNS（53）与已知 DoH 域名（拒绝/重定向策略可配）；agent 硬编码 IP 直连、私有 DoH 属**残余风险**，由"包装 + 默认拒绝"结构兜底（IP 不在映射即拒），不做深度对抗；
- 引擎侧改造点：`internal/agent/gvisor` 的 PolicyChecker 扩域名维度；LatticeDNS 增加策略判定钩子与映射表（含 TTL 过期回收）；未经理 DNS 流量的默认拒绝逻辑。

### 3.2 权限弹窗（Mac 客户端，M1）

- 事件流：引擎捕获"未授权域名首次访问"→ 上报客户端（信令通道）→ Mac 弹窗（人话文案："Claude Code 想访问 api.openai.com:443"）→ 用户选择 → 策略即时下发 → 引擎热生效（不重启引擎、不杀 agent 会话）；
- "允许一次" = 会话级（引擎进程生命周期内）；"总是允许" = 持久化并同步控制面；
- 静默模式：弹窗可整体关闭，退化为"默认拒绝 + 时间线留痕"，由用户事后在设备页补授权；
- iPhone 弹窗转发（在手机上批准 Mac 上 agent 的请求）列 M3，不阻塞 M1。

### 3.3 agent 身份与包装（`sandbox run` 增强，M2）

- 补 flag：`--image`（go-containerregistry 拉取 OCI 镜像并解包为 rootfs，不要求用户装 docker）、`--mount <host>:<container>`、`--env KEY=VAL`；
- 身份语义：包装的 agent 在设备上表现为一个具名成员（如 `claude-code@macbook`），弹窗主语、审计归属、mesh 授权（它能不能访问 NAS）全部挂在这个身份上；
- 平台：M2 先落 Linux（现有 netstack/tproxy 档直接复用）；Apple 端用嵌入式引擎形态承接（`apple/engine` 已内嵌 netstack），与 NE 全机模式并存（NE 管全机，嵌入式管被包装的 agent），阶段计划见 §五；
- 端到端演示用例（也是本设计的北极星场景）：`lattice sandbox run claude-code --image claude-code:latest --mount ~/proj:/workspace --env ANTHROPIC_API_KEY=... -- claude`。

### 3.4 支撑件：人话审计时间线（M3）

- 数据源：netstack 层已有的连接审计记录，按 agent 身份聚合；
- 展示为日级 / 会话级人话摘要："今天 Claude Code：连 Anthropic 312 次、git 推送 2 次、仅触碰项目目录、尝试访问 1 个被拒域名"；
- Mac 客户端 + iPhone 设备页均可查看；数据不出本地（除非用户开启云备份，另行评估）。

## 四、威胁模型与档位自洽

### 4.1 威胁分层（netstack 档的真实定位）

| 对手画像 | netstack 档（+D7 加固）覆盖度 | 说明 |
|---|---|---|
| 误伤型 agent（提示词注入后乱连、误传文件） | **全量覆盖** | DNS 默认拒绝、弹窗、审计正是为此设计；对手不会主动拆防线 |
| 对抗型 agent（被诱导主动拆防线） | 加固后从"一步之遥"降为"需要内核漏洞" | 天花板在档位本身——这正是 runsc / VM 档存在的理由（D6） |
| macOS 侧 | 执法点是系统管理的 NE，无"拆规则"问题 | Mac 档的真实短板是**归因**，由包装路线解决（§1.3-3） |

### 4.2 专属 netns：防绕与防污染一体解决（M0 交付）

在宿主机上写透明代理规则是这类工具的经典翻车点：与 Docker（`DOCKER`/`DOCKER-USER` 链）、kube-proxy（数千条规则）、用户自装的 Clash TUN 互相打架；lattice 被 `kill -9` 后孤儿 REDIRECT 规则把宿主流量送进黑洞。因此规则**不进宿主表**——每个 agent 一个专属 network namespace：

```
宿主机 iptables：一条不加，永远不动
│
├── lattice 主进程（netstack + wireguard-go + 控制面信令）
│        ▲ AF_PACKET 收发
│        │ veth（对端）
└────────┼────────────────────────────────
   netns "lattice-sbx-<id>"（每个 agent 一个）
   ├── agent 进程（无 caps，眼里只有 lo 和 veth0）
   └── REDIRECT/TPROXY 规则【只存在于本 netns】
       全部 TCP + UDP53 → 本 netns 代理端口
```

- **防污染**：netns 表对宿主完全不可见，与宿主上任何网络软件零交互；**崩溃即自愈**——netns 随其中最后一个进程消亡，规则同灭，无孤儿规则；多 agent 各自 netns，规则互不可见；
- **防绕**：netns 内只有 agent 一个居民，全量 REDIRECT 无需 owner match；agent 摘除全部 caps + `no_new_privs`，拆规则、raw socket、改 UID 均无着力点；硬编码 IP 由 IP 层默认拒绝兜底（§3.1）；
- **能力形状**：建 netns 需 `CAP_SYS_ADMIN`，或走 rootless 路线（`unshare -Urn`，user namespaces；部分发行版默认限制需评估）。从"宿主 NET_ADMIN"换成"netns SYS_ADMIN / userns"，换来宿主表零污染；
- **拓扑与 runsc 档同构**：runsc 档本就规划"专属 netns + veth + lattice 桥"，桥侧代码两档复用——netns 化等于提前铺好 runsc 档的一半路基。

加固后逐条核对绕法：拆/改规则 → netns 内且无权限；raw socket 绕劫持 → 无 NET_RAW；改 UID → 非 root；硬编码 IP 绕 DNS → IP 层默认拒绝（§3.1）。残余路径 = 内核 0day / 容器逃逸，归隔离档（runsc/VM）管辖。

### 4.3 档位关系澄清（避免"过渡方案"误读）

- **iptables 劫持是过渡品**：Apple 侧从未使用（NE），runsc 档不需要它；
- **netstack 是永久承重层**：策略、审计、LatticeDNS、WireGuard 身份全部挂在这里；runsc 档内它站在墙外侧（veth 桥）继续干同样的活，Apple/iOS 上它本来就是唯一形态；
- **runsc / VM / microVM 只接管进程隔离**：阶梯为 netstack（现在，全平台）→ runsc（Linux）→ VM（macOS VZ / Windows WSL2）→ microVM（Pro 终态）→ 组合纵深；
- `--isolation` flag **与 runsc 档同 PR 出生**（netstack 为默认值），当前不引入单值 flag。

### 4.4 同源问题：出口节点的宿主零污染（v2 方向，另行立项）

出口节点今天是更重的宿主侵入：`net.ipv4.ip_forward` 是宿主级 sysctl，FORWARD 链与 MASQUERADE 写在宿主表（v0.4.0 数据面）。收敛分两步，与沙箱共用"宿主机只当网络附件，不当规则场"的原则：

- **近期（netns 包裹）**：出口数据面搬进专属 netns——`ip_forward` 本就是 per-netns sysctl，FORWARD/MASQUERADE 全部写入 netns 内，宿主表零写入；egress 侧选 macvlan/ipvlan 直连物理网（注意 Wi-Fi 不支持 macvlan 的限制），与沙箱 M0 共享同一套 netns 基建；
- **终局（userspace NAT）**：出口本就以 netstack 终结隧道，可不再进内核——会话级 NAT 直接以宿主 socket 出网（gVisor 系 gvproxy / gvisor-tap-vsock 同法），零 sysctl、零 iptables、零 netns，且出口流量从此穿过策略/审计钩子（内核转发的流量目前是审计盲区）。代价是数据面重写与吞吐/UDP 性能对齐，作为出口节点 v2 目标。

## 五、明确非目标

- B 端"沙箱即服务"运营件（session API、预热池、快照、按秒计费）；
- 进程级隔离档（Linux runsc、macOS VZ / Windows WSL2 VM）——另行立项，Pro 高阶档；
- 全机流量的进程归因识别（D5）；
- 对抗性绕行研究（私有 DoH、硬编码 IP 的深度对抗，v1 靠默认拒绝结构兜底）；
- Windows 客户端弹窗 UI（wintun 网络收编已具备，UI 二期）。

## 六、阶段计划与成功标准

| 阶段 | 内容 | 验收 |
|---|---|---|
| M0 | 引擎域名过滤 + netns 零污染加固：DNS 拦截 + 映射表 + netstack 决策点扩展；agent 与其规则入住专属 netns（宿主表零写入）+ 摘除 caps | `sandbox run` 包装的进程：未授权域名 DNS 被拒；白名单域名可连；直连未知 IP 被默认拒绝；**宿主 iptables 各表零条目，`kill -9` lattice 后无残留规则**；agent 进程 caps 为空，netns 内 `iptables` 操作返回 EPERM，raw socket 不可用；`make lint` / `make test` 绿 |
| M1 | Mac 权限弹窗 + 策略下发闭环 | 真机 Claude Code：首连弹窗 → 允许后可连 / 拒绝后连不上且留痕；"总是允许"重启引擎后仍生效 |
| M2 | `sandbox run --image/--mount/--env`（Linux）+ Claude Code 北极星场景演示 | §3.3 演示命令在干净 Linux 容器内跑通，agent 流量全量过策略与审计 |
| M3 | 人话审计时间线（Mac）+ iPhone 弹窗转发 | 时间线可读、按 agent 身份过滤；iPhone 可批准 Mac 上 agent 的首连请求 |

## 七、开放问题

1. "总是允许"策略的存储位置：本地为主 + 控制面同步，还是控制面为唯一真源？（涉及离线行为与隐私取舍）
2. Apple 端嵌入式引擎包装 Claude Code 的落地形态（无 NE 槽位冲突验证，依赖引擎 c-archive 迁移完成度）；
3. 弹窗触发频控与聚合（同域名反复拒绝时的提示策略）；
4. `pricing.md` 按 C 端定位重写（独立小 PR，与本设计解耦）。
