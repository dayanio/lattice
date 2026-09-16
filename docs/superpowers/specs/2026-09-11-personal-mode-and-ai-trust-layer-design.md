# Lattice 个人模式与 AI 信任层（轻量免费版 Tailscale）设计

> 日期: 2026-09-11
> 性质: 程序级设计（program spec）——Phase 1 为实现规范，Phase 2/3 为方向性设计，各自实施前需出独立 mini-spec
> 关联: `2026-05-01-ice-relay-wireguard-design.md`、`2026-09-10-standalone-reconcile-design.md`、
>       `2026-04-30-opencore-licensing-design.md`、`2026-05-09-ai-agent-isolation-design.md`
> 状态: 待评审

---

## 一、目标与定位

**一句话**：在现有 Lattice standalone（零 K8s）模式之上，交付"出门在外，一键连回家里私人 AI"的个人自托管组网能力——轻量版 Tailscale，全功能免费（Community，Apache-2.0）。

**核心用户旅程（v1 验收线）**：

1. 家里电脑 `docker run` 起 standalone 控制面 + `lattice up` 加入（已有能力）；
2. Dashboard 点"添加手机"→ 出二维码；
3. 手机官方 WireGuard App 扫码 → 直连家里，打开 `http://<家里overlayIP>:11434` 用上本地大模型；
4. 不需要自研手机 App、不需要公网 IPv4、不经过任何第三方服务器转发流量。

**设计原则**：

- 手机端走**官方 WireGuard App**（标准 wg-quick 配置 + 二维码），零自研移动端；服务端协议与客户端无关，未来自研 App（gomobile/ICE 打洞）可直接复用。
- 全部新能力进 **Community 版**，不依赖任何 PRO 组件（策略执行用 iptables 后端，不用 eBPF）。
- 控制面只做注册/下发/打洞协调，流量 P2P 直连优先；复用现有 netmap、IPAM、策略引擎、TTL，不造新轮子。

**非目标（v1 明确不做）**：

- 自研手机 App / iOS NetworkExtension；
- 对称 NAT 下手机与家里直连（官方 App 无 ICE 能力，文档如实说明并给出端口转发/VPS UDP 转发的逃生通道）；
- SaaS/托管控制面；
- MagicDNS 域名体系（v1 用 overlay IP 直访，后续版本再评估）。

---

## 二、总体架构

现有资产全部复用，新增/改动集中在四处：

```
手机 [官方 WireGuard App]                        家里电脑
      │ ① 扫 wg-quick QR                        ┌──────────────────────────────┐
      │ ② UDP 直连（IPv6 / UPnP / 端口转发）      │ latticed（standalone 控制面）  │
      ▼                                         │  - external peer 注册/导出配置 │
┌─────────────────────────────┐                 │  - 连通性体检上报 + 三态展示    │
│ t_peer (type=external-wg)   │◀──netmap───────│  - v1.5: 浏览器网关 / MCP 网关 │
│ 公钥 + overlay IP + endpoint │                 ├──────────────────────────────┤
└─────────────────────────────┘                 │ lattice agent（已有数据面）     │
                                                │  - 固定 WG 端口 + UPnP 映射    │
      策略：default-deny，只放行                  │  - 握手状态上报（在线判定）     │
      手机 → 家里 :11434（TTL 可选）               │  - wireguard-go (wireflowio fork)│
                                                └──────────────────────────────┘
```

关键洞察——**家里 agent 对手机零改动即可放行**：netmap 本就下发全部 peer 公钥，wireguard-go 按 AllowedIPs 放行已知 peer，且 WireGuard 天然支持对端 endpoint 漂移（手机换基站自动跟随）。手机只是"不跑 agent、不连信令"的特殊 peer。

---

## 三、Phase 1（v1）：个人模式 —— 实现规范

### 3.1 数据模型

`t_peer`（`internal/server/models/peer.go`）新增字段：

```go
Type       string `gorm:"size:20;default:'agent';index" json:"type"` // agent | external-wg
PublicKey  string // 已有；external-wg 在创建时由服务端生成密钥对，只存公钥
Endpoints  string `gorm:"type:text" json:"endpoints,omitempty"` // agent 上报的候选端点 JSON（v1 新增）
```

约定：

- `external-wg` peer **不连信令、不跑 agent**。`LastSeenAt` 由家里 agent 的握手观察器（`internal/agent/wireguard/handshake_watcher.go`）上报的 per-peer 握手时间戳驱动：家里 agent 检测到与该 peer 的成功握手即向服务端上报，服务端刷新对应 external peer 的 `LastSeenAt`。
- 密钥处理：服务端生成 X25519 密钥对（wg 格式 base64），**私钥只在创建接口的一次性响应中出现，不落库、不可再次获取**；丢失即吊销重加。`t_peer.Token` 注册凭据对外部 peer 置空。

### 3.2 服务端 API

新增（Gin，挂在现有 standalone 路由下）：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/v1/peers/external` | 创建 external peer：生成密钥对、IPAM 分配 overlay IP（复用 `reconcilers.AllocateAddress`）、写 t_peer；响应一次性返回完整 wg-quick 配置文本（Dashboard 将其整体渲染为二维码——官方 WireGuard App 支持从二维码图导入配置） |
| GET | `/api/v1/peers/external/:id/config-status` | 返回该 peer 的端点/在线状态（手机侧排障用，不返回私钥） |
| DELETE | `/api/v1/peers/:id` | 已有吊销；external peer 吊销 = 删记录，家里 agent 下轮 netmap 收敛后自动拒绝其流量 |

**wg-quick 配置渲染**（纯函数，独立包便于单测，建议 `internal/server/service/wgconfig/`）：

```ini
[Interface]
PrivateKey = <一次性返回>
Address = <手机 overlay IP>/32
# DNS 不下发（v1 非目标）

[Peer]
PublicKey = <家里 agent 公钥>
Endpoint = <连通性体检选出的最优端点>
AllowedIPs = <家里 overlay IP>/32        # 默认仅直达家里 peer
# AllowedIPs = 0.0.0.0/0, ::/0          # 可选"全流量走家里"（下发给手机前 UI 二次确认）
PersistentKeepalive = 25                 # 手机侧建议，保活 NAT 映射
```

端点选择优先级：公网 IPv6（agent STUN 探测所得）> UPnP 映射的公网 IPv4:port > 手动端口转发地址。多候选都写入响应备注，Endpoint 取最优。

### 3.3 家里 agent：可达端点（本路线唯一硬前提）

官方 App 不会打洞，家里必须有稳定可达的 WireGuard 端口。新增三项能力（`internal/agent/`）：

1. **固定 WG 监听端口**：agent 配置新增 `personal.wg-port`（个人模式启用；普通模式保持 ICE 动态端口不变）。启用后 wireguard-go device 绑定该固定端口对外提供服务。
2. **UPnP/NAT-PMP 自动端口映射**（opt-in 开关 `personal.upnp=true`）：用 goupnp 将 `personal.wg-port` 映射到公网，失败静默降级并在体检结果中如实标注。
3. **连通性体检（doctor）**：STUN 探测公网 IPv4/IPv6 与映射端口 → 对外端口可达性自检 → 上报服务端 `Endpoints` + 三态结论：
   - `ipv6-ready`：✅ IPv6 直连就绪（国内蜂窝网络 IPv6 普及率高，主推路径）
   - `upnp-mapped`：✅ UPnP 已映射
   - `manual-needed`：⚠️ 需手动端口转发（Dashboard 给出分路由器引导文案）；文档化 VPS UDP 转发逃生通道

> **Spike-1（实现第一步，先于一切）**：确认 wireguard-go device 固定端口与现有 ICE 动态 socket 的共存方式（ fork 内部 device 绑定逻辑）。备选方案：个人模式启用独立静态 WG listener，普通 mesh 走原 ICE 路径，二者并存互不影响。

### 3.4 策略预设："暴露 Local LLM"

复用策略引擎（default-deny + 端口级 ingress），新增预设参数化封装：

- 语义：仅放行 `指定 external peer → 指定家里 peer 的 tcp/<ports>`（默认 11434），可选 TTL（如 30 天后自动过期回收，复用现有 TTL reconciler）；
- 一个 API 调用/Dashboard 按钮完成创建，不要求用户理解 selector 语法；
- 反向默认全禁：手机触不到家里其他设备/局域网，家里 LLM 不暴露给公网。

### 3.5 Dashboard（frontend/）

- **设备入口改造**：`NodeEnrollBanner.vue` 与设备列表新增"添加手机/平板"→ 弹出二维码（`external peer` 创建响应直接渲染，二维码库用前端 qrcode 组件即可，无需服务端出图）；
- **一次性私钥提示**：显示"配置只显示这一次"警示 + 下载 .conf 按钮；
- **连通性体检卡片**：展示 3.3 的三态结论与引导；
- **个人模式向导**（standalone 首次进入）：起服务 → 添加手机（扫码）→ 一键"暴露 Local LLM"（端口可改）→ 完成；
- **设备管理**：external peer 显示在线状态（握手驱动的 LastSeenAt）、一键吊销；
- 中文 quickstart 文档："出门在外，一键连回你家里的私人 AI"。

### 3.6 容量与许可

- `checkNodeLimitStandalone`（`internal/server/service/peer.go`）已有节点上限逻辑；个人场景默认额度放宽到 **8 节点**（社区版配置项，可调）。

### 3.7 测试

- 单测：wg-quick 渲染（含 IPv6/全流量/keepalive 各分支）、密钥生命周期（不落库、一次性）、external peer 注入 netmap（复用 `netmap_builder_test.go` 风格）、doctor 三态判定；
- e2e（扩展 `test/e2e_standalone/`）：API 创建 external peer → 渲染配置 → 容器内 wg 工具导入 → 与家里 agent 容器真实握手 → curl 通 LLM 端口 → 策略默认拒绝其他端口 → 吊销后中断；
- 手工验收清单：iOS/Android 官方 App 扫码、蜂窝网络（IPv6）直连、换基站漫游不断流。

---

## 四、Phase 2（v1.5）：获客入口 + AI 叙事 —— 方向性设计

> 两个能力均复用 netmap/策略/审计，各约一周级；实施前各自出 mini-spec。

### 4.1 浏览器零安装访问（获客杀手锏）

手机**什么都不装**，浏览器直接打开家里 Open WebUI：

- standalone 控制面新增可选"个人网关"：WebAuthn passkey 登录（无密码、免短信）+ 反向代理到本机/overlay 内服务；
- 每条路由映射为一条 Lattice 策略（复用端口级策略与审计）；
- TLS 是前置条件：优先公网 IPv6 + 自动证书（ACME）；无域名的家庭给出 VPS 反代文档。网关默认关闭，显式开启；
- **承载路径分两档（"公网 → 网关"这一段由 Lattice 自有机制解决）**：
  - 家里有公网条件（IPv6 / UPnP 映射 / 端口转发，即 Phase 1 连通性体检的三态）：公网流量直接落到家里网关，无需任何中继；
  - 家里完全无公网条件（双栈 CGNAT）：家里网关**主动出站**连 LRP relay（复用 agent 现有 LRP TCP/QUIC 出站传输与 `lrper` 二进制，relay 跑在用户任一台有公网的 VPS 上），HTTPS 入口架在 VPS，流量经隧道回流家里。NAT 只拦进不拦出，出站隧道必然可达——这正是 mesh 打洞失败兜底的同一套通道，只是换了一个客户端类型（浏览器）；
- 安全边界：网关只暴露白名单路由、全量访问审计、可一键关闭。

### 4.2 个人 MCP 网关（AI 叙事）

把家里的 Ollama/工具/MCP server 变成公司电脑上 Claude、Cursor 可直连的 MCP endpoint：

- 控制面暴露经认证的 MCP endpoint，复用现有 agent JWT 中间件（HS256）做调用方身份；
- 工具调用写入 `tool_spans` 审计（traceID/agentID/tool/status），与现有审计查询 API 打通；
- 复用 `lattice-mcp` 的工具定义与人类审批模式；
- 产品故事："你的私人 AI 大脑有了私人门牌号"。

---

## 五、Phase 3（Later）：护城河与差异化 —— 需独立立项

| # | 项 | 说明 | 前置 |
|---|---|---|---|
| 1 | **抗量子握手** | 在 wireflowio/wireguard-go fork 中做 PQ-PSK 混合（Go 标准库 `crypto/mlkem`，握手时 PQ 封装共享密钥作为 WG PSK）；对外讲"自托管阵营首个抗量子 VPN" | Spike：fork 内 handshake 改造点评估 |
| 2 | **Agent 护照转 Community** | AgentIdentity、子代理委托、tool_spans 审计从 PRO 摘入免费版，占位"AI agent 专用 WireGuard"品类 | **商务决策**：open-core 边界（见 2026-04-30 spec），需用户拍板 |
| 3 | **自然语言策略转免费** | Intent Engine（NL → policy diff → 确认生效）在个人模式开放；是个人场景最好的展示窗 | 同上 |
| 4 | **时效网络** | 把 TTL 策略 + 邀请包装成"给朋友开 2 小时、只到某端口"的独立功能 | 无 |
| 5 | **数据飞地路由** | sidecar 按域名/关键词分流：敏感请求走家里 GPU，批量走云 API；"数据不出家门"的字面实现 | Phase 1 稳定 |
| 6 | **LRP 的 MASQUE 叙事** | LRP 已有 QUIC transport，补 CONNECT-IP 风格封装与文档，覆盖"UDP 全封"环境 | 无 |

---

## 六、风险与开放问题

| 风险 | 应对 |
|---|---|
| wireguard-go 固定端口与 ICE socket 共存方式未验证 | Spike-1 置于实现首位；备选独立静态 listener 方案已明 |
| 家庭网络既无 IPv6 又无法端口转发（双栈 CGNAT） | 文档如实说明 + VPS UDP 转发逃生通道；不做伪"万能直连"承诺 |
| UPnP 安全顾虑 | 默认关闭（opt-in），映射仅限单一 WG 端口，体检结果透明展示 |
| 全流量模式（0.0.0.0/0）与手机本地局域网冲突 | UI 二次确认 + 文档说明官方 App 的 exclude 规则用法 |
| 一次性私钥丢失 | 明确"丢失即吊销重加"的产品语义，不做私钥托管 |
| Phase 3 的 PRO→Community 划转影响商业化 | 作为独立商务决策点，不阻塞 Phase 1/2 |

---

## 七、里程碑与依赖

```
M1  Spike-1 + external peer API + wg-quick 渲染 + 单测        （Phase 1 地基）
M2  agent 固定端口 + UPnP + 连通性体检                          （依赖 M1 的端点字段）
M3  Dashboard 二维码 + 向导 + LLM 预设 + e2e                  （依赖 M1/M2；Phase 1 完成线）
M4  浏览器个人网关（passkey + 反代 + 审计）                     （Phase 2，依赖 M3）
M5  个人 MCP 网关                                             （Phase 2，可与 M4 并行）
M6  PQ 握手 Spike → 实现                                      （Phase 3，独立）
M7  Agent 护照/Intent Engine 转免费                            （Phase 3，商务决策后）
```

Phase 1（M1–M3）预估 1–2 周单人工作量；Phase 2 各一周级。
