# gVisor 嵌入式网络设计 — 安全模型、出入栈与协议路线

> 状态：v1.1（增补审批流 §四） · 日期：2026-09-23
> 关联：`2026-09-22-apple-embedded-sdk-design.md`（嵌入式 SDK 总设计，其 M0–M3 已落地）、lattice-shim 仓 `docs/plans/2026-09-22-tsnet-style-server.md`
> 实证基础：本文所有"已支持/已验证"均对应已合并代码与 mac-demo 实测；"计划"均为本文新提出的路线。

## 一、背景与动机

lattice 有两条网络栈产品线：

- **内核 TUN 线**（NE 客户端 / Linux agent / 子网网关）：L3 全协议、全机/全网段生效，需要系统授权（NetworkExtension、`NET_ADMIN`）。
- **gVisor 用户态线**（Linux AgentSandbox、`shim.Server` 嵌入式引擎）：L4 语义、进程内生效、零特权。

本文聚焦第二条线的**定位、安全模型、出入栈机制与协议路线**。

**引入 gVisor 的第一动因是安全与策略审批**（用户裁定）：内核 TUN 模式下，overlay 身份属于整台机器——任何本地进程都可以借道它进入 mesh（横向移动通道），mesh 里任何节点也都能触达本机**全部**监听端口（全端口扫描面）。嵌入式形态把网络身份收敛到一个进程、把可达性收敛到一份显式审批清单，从结构上消除这两个暴露。

**次要动因是零授权嵌入**：没有 NetworkExtension 权限的 App（车机、三方应用、MDM 受限设备）和没有 `NET_ADMIN` 的容器，拿不到内核 TUN——用户态栈是它们唯一的入网方式。

**明确不替代**：全系统覆盖、L3 网关仍然是内核 TUN 线的职责（见 §十 分层关系）。gVisor 不是更便宜的 NE，是另一层产品。

## 二、目标 / 非目标

**目标**

1. 把安全模型成文：权限归零、暴露面审批制、攻击面用户态化。
2. 定义出栈/入栈机制与每条路径上的**策略审批点**。
3. 给出 UDP/ICMP 的支持路线（现状 → 分批计划）。

**非目标**

- 替代 NE/子网网关（全系统透明、L3 转发不在本设计内）。
- 做成 CNI（CNI 是节点级 L3 基建：IPAM、veth、内核路由/eBPF；嵌入式是进程内 L4 端点，两层互不替代）。
- 性能对标内核零拷贝路径（用户态有一份搬运开销，按家庭/办公码率设计）。

## 三、安全模型

### 3.1 三层安全机制

| 机制 | 内核 TUN（对照） | 嵌入式 gVisor |
|---|---|---|
| **权限归零** | 需要 VPN 授权 / NE / `NET_ADMIN` | 无任何系统授权；无 TUN 设备、无 VPN 图标 |
| **暴露面审批制** | overlay 身份属于整机：任意本地进程可借道入 mesh（横向移动）；mesh 可触达本机全部监听端口 | 身份只属于宿主进程；入站只到显式审批的端口（无 `Listen` 一律 RST）；出站过策略引擎 |
| **攻击面用户态化** | mesh 来的不可信包在内核 TCP/IP 栈解析（经典提权面） | 不可信包在 Go 用户态栈解析；打穿也只是一个应用进程，不是内核 |

### 3.2 审批原则：所有可达性必须经过一个显式审批点

- **出站**：连接过 `PolicyChecker`（`EgressFilter`：CIDR 白名单 + DefaultDeny）+ `AuditWriter` 审计，宿主可按身份/目标/端口批或拒（shim 已实现，`WithPolicy`/`WithAudit` 注入）。
- **入站**：默认拒绝（无 `Listen` 即 RST）；放行 = 显式 `Listen`（等价现有「共享本地服务」发布的语义）或显式配置通配策略（§6.2）。

### 3.3 策略开关谱系（安全 ↔ 便利是显式选择）

| 档位 | 行为 | 适用 |
|---|---|---|
| 显式清单（默认） | 逐端口 `Listen`，无清单即拒绝 | 默认；安全卖点所在 |
| 规则混合 | 默认拒绝 + 端口段/CIDR 规则（`22→127.0.0.1`、`80,443→192.168.1.0/24`…） | 家庭/办公常驻节点 |
| 全端口通配 | 任意端口转本机回环 | 明示牺牲暴露面换取便利（等价内核语义） |

## 四、暴露与访问的审批流

§3.2 的审批原则落到工程上，就是一张**审批流**：凡"可达性从无到有、或范围扩大"的事件，都必须经过一次显式批准并留痕。

### 4.1 审批对象（三类）

| 对象 | 示例 |
|---|---|
| 入站暴露规则 | 放行 `tcp:22 → 127.0.0.1:22`；放行 `tcp:80,443 → 192.168.1.10`；放行 `udp:11434` |
| 出站白名单变更 | `EgressFilter` 新增 CIDR / 端口（访问新网段） |
| 能力档位升级 | 开启通配转发、SOCKS UDP ASSOCIATE、ICMP 探测等能力开关（§8 P 系列） |

### 4.2 审批主体：双通道

- **宿主本地通道**：宿主 App UI 即时审批（横幅/弹窗，复用设备审批的 pending 形态与 `hasPendingApprovals` 模式）——适合「共享本地服务」这类自持场景。
- **工作区管理员通道**：控制面注册表 + 审批 API（写操作 RoleAdmin，P5 发布注册表已立先例）——适合管理员集中治理，以及"被共享方发起访问申请"的场景。
- 两通道按 workspace 策略配置为"本地批即生效"或"本地 + 管理员双批"。

### 4.3 审批载荷（Grant）：身份级，不是裸端口

Grant 的最小单元是"**谁**可以对**什么协议/端口**访问**哪个目标**、到**什么时候**"：

- 主体：指定 peer 身份 / peer 组 / 全部（默认最小化：显式指定）；
- 协议：tcp / udp / icmp；
- 端口/网段：单端口、端口段、CIDR；
- 目标：回环端口映射或 LAN 地址（入站）；目标 CIDR（出站）；
- 有效期：永久 / 到期自动回收；
- 状态机：`requested → pending → approved / denied → active → expired / revoked`——与设备审批状态机同构。

### 4.4 执行点与分发

- **分发**：服务端注册表（形同 `t_publish`）+ NATS 信号广播（形同 `lattice.signals.publishes`）→ 宿主策略引擎热更新，无需重启引擎。
- **入站执行**：Forwarder handler / `Listen` 白名单先查 grant 表；无有效 grant → RST/拒绝 + 审计。
- **出站执行**：`PolicyChecker` 查 grant 表；deny 事件可升级为一条 pending 审批请求（UI 上呈现为"申请放行"）。
- **审计**：`AuditWriter` 记录审批动作（谁批、范围、何时）与**每次命中**（来源身份、目标、时间）——命中审计是扫描检测与事后追责的数据基础。

### 4.5 与既有体系对接

- 状态机/横幅复用设备审批的成熟形态；
- 注册表与 RoleAdmin 模式沿用 P5 发布注册表；
- 「共享本地服务」从"直接添加"升级为"申请 → 审批 → 生效"；
- §6.2 的三档策略谱系映射到 grant：显式清单 = 一组已审批 grant（默认）；规则混合 = grant 集合（端口段/CIDR）；**全端口通配 = 一张高危全端口 grant**——强制短有效期 + 管理员通道 + UI 显著警示 + 命中审计必开。

## 五、架构与组件

```
宿主 App（进程内）
├── 宿主业务代码 ── Dial/Listen/Ping ──┐
├── Socks5Server（127.0.0.1:1080）─────┤ 出站：本进程 + 代理进来的外部进程
├── ForwardListener / Forwarder ───────┘ 入站：显式清单 或 通配策略
└── shim.Server = gVisor netstack（用户态 TCP/IP）
        │ channel.Endpoint（原始 IP 包）
        ▼
   wireguard-go（加密/解密、IpcSet peer 管理）      ← 两条产品线共用，零改动
        ▼
   ICE/LRP（P2P 打洞 / 中继）→ 公网
```

组件盘点（shim 仓，均已存在）：

| 组件 | 状态 |
|---|---|
| `Server`（netstack + `Dial`/`Listen`/`Channel`/`AddPeer`） | ✅ M0 |
| `Sandbox`（`Socks5Server` + `ForwardListener` + Policy/Audit 组合） | ✅ 已有 |
| `Socks5Server` | ✅ TCP CONNECT；UDP ASSOCIATE 无（§8 计划） |
| `PolicyChecker`/`EgressFilter`/`AuditWriter` | ✅ 已有 |
| gVisor `tcp.NewForwarder` / `udp.NewForwarder`（任意端口拦截，已对照 pin 版本确认存在） | 计划暴露 |
| 主仓扩展点：`internal/agent.NewNode` 的 `CustomTUN`/`ProvisionerFactory` + `gvisor.NewTUNAdapter`/`InjectIntoChannel`/`NewSandboxProvisionerFactory` | ✅ 已验证（M1–M3 全程零改动复用） |
| 主仓 `apple/engine/embedded` + `EmbeddedKit`（Swift Package）+ LatticeMac「嵌入式引擎」页 | ✅ M1–M3 |

## 六、入栈（Ingress）设计

### 6.1 显式清单模式（现状，默认）

宿主对每个放行端口 `engine.Listen(:port)` → `Accept` → `dial 127.0.0.1:<本地端口>` → 双向 `io.Copy`。语义与「共享本地服务」发布完全一致；overlay 端口与本地端口可映射（`overlay:8443 → local:8080`）。本地服务看到的是一条 127.0.0.1 回环连接——**只绑回环的服务可以精确共享给 mesh 而不暴露给局域网**。无 `Listen` 的端口：netstack 回 RST（默认拒绝）。

### 6.2 通配/策略模式（计划）

不预注册端口，注册 **`tcp.NewForwarder` / `udp.NewForwarder`**：无人认领的 SYN/数据报回调 `handler(port, proto)`，由策略引擎现场裁决：

```
handler(port=22, tcp) → 转发 127.0.0.1:22        （本机服务）
handler(port=445, tcp) → 转发 192.168.1.10:445   （LAN 其他机器——NAT 形态的迷你子网网关）
handler(port=54321, tcp) → 拒绝                  （默认）
```

目标地址由策略表决定（回环 / LAN 网段 / 拒绝），因此"中继到本机或本网络内"是同一机制的两个配置。

Tailscale userspace-networking 模式即此实现，生产级先例。

策略表的内容与变更来源即审批流（§四）：每条规则都是一张 grant，变更走审批、生效走广播、命中进审计。

TCP 回程靠四元组命中 endpoint（无路由环节）；UDP 回程靠会话表（§8）。

## 七、出栈（Egress）设计

按"谁发起"分层：

| 发起方 | 通道 | 状态 |
|---|---|---|
| 宿主 App 自身 | `engine.Dial`（TCP 双向、UDP 出站） | ✅ |
| 本机其他进程（动态目标） | 本地 SOCKS5（`Socks5Server`），`ssh -o ProxyCommand='nc -x …'`、`curl -x socks5h://…`、浏览器/系统代理 | ✅（TCP） |
| 本机其他进程（固定目标） | 静态本地转发 `net.Listen(127.0.0.1:p) → engine.Dial(target)` | ✅（宿主自行挂载） |
| 全系统任意进程 | 内核 TUN：NE（macOS）/ `NET_ADMIN` 子网网关（Linux） | ✅ 另一条产品线 |

**可达范围模型**：SOCKS/`Dial` 本身不做地址过滤——你拨什么它送什么；真正裁决的是 **mesh 路由表（WireGuard AllowedIPs/netmap）**：

- overlay 段 → 恒通；
- 子网网关宣告的网段 → 通（扩展可达）；
- 出口节点宣告 `0.0.0.0/0` → 全量经该节点出网（产品已有 ExitNode 概念）；
- 路由表外地址 → 黑洞超时（无归宿，非显式拒绝）；
- `EgressFilter` 可在路由之上再收一道策略（CIDR 白名单 + DefaultDeny），白名单变更走审批流（§四）。

**透明性边界**：协议层端到端透明（SOCKS 只搬 TCP 字节流，TLS/SSH 不终止）；配置层不透明（程序需指向代理；DNS 建议 `socks5h` 走代理侧解析，LatticeDNS 名字才可解析）；ICMP 结构上不过 SOCKS。

## 八、协议支持现状与路线

### 8.1 现状

| 能力 | 状态 |
|---|---|
| TCP 出站 + 入站 | ✅ |
| UDP 出站（`Dial("udp",…)`） | ✅ |
| UDP 入站监听 | ❌（gVisor 支持，API 未暴露） |
| ICMP 被动应答（被 ping） | ❌（netstack 原生能力，NIC 未注册 ICMP 协议） |
| ICMP 主动探测（ping 出去） | ❌（需新 API + 会话表） |
| SOCKS5 UDP ASSOCIATE | ❌（`socks5.go` 仅接受 CONNECT） |

**协议判断原则**：客户端主动发起的 TCP/UDP 单/多通道协议都能跑（SSH、SFTP、HTTP/HLS、SMB、NFSv4…）；服务器回连型老协议（active FTP、NFSv3 辅助端口）需换现代形态；ping 是诊断工具不是功能通道。

### 8.2 分批计划

| 批次 | 内容 | 要点 | 规模 |
|---|---|---|---|
| **P1** | 被动 ping：建栈时注册 ICMPv4 协议（`netstack_core.go`） | netstack 原生应答，行级改动 + 包泵回环测试 | 小时级 |
| **P1** | `Server.ListenUDP`（`net.PacketConn` 语义） | 与 `ListenTCP` 同构；gomobile 侧 `(data, addr)` 多返回值不可绑，需包结构体（M2 教训） | 一天级 |
| **P1** | SOCKS5 UDP ASSOCIATE（RFC 1928 §7） | `socks5.go` 加命令分支 + 每客户端中继 socket + 空闲超时 | 一天级 |
| **P2** | `Server.Ping(ctx, host, timeout) (RTT, error)` | gVisor ICMP endpoint 发 echo；按 `(identifier, seq)` 会话表认领 reply；gomobile 形态 `ping(host, timeoutMs) → rttMs` | 一天级 |
| **P3** | Forwarder 通配 + 策略引擎（TCP + **UDP NAT 会话表**） | `tcp/udp.NewForwarder`；会话表 `(远端, 端口) → 本机 socket`，空闲超时回收；与 §6.2 策略档位一起交付；客户端「共享本地服务」同步升级 | 最重，随入站通配里程碑 |

### 8.3 横切注意

- **MTU**：WireGuard 加密开销压低有效 MTU，UDP 大包靠 IP 分片——测试必须覆盖大报文（QUIC/视频类首当其冲）。
- **会话表是公共件**：UDP NAT 与 ping 认领共用（超时回收、`-race` 覆盖）。
- **gomobile 绑定形态**：多返回值/`time.Time`/context 不可跨桥，统一包结构体 + 整型参数（M2 已验证的约束）。

## 九、数据流示例

### 9.1 SSH 回家（外面 gVisor 客户端 → 家里 NAS）

```
engine.Dial("tcp", 10.96.0.B:22)
→ gVisor 出 SYN → wg 加密 → ICE 直连或 LRP → 家里
→ 家里侧（嵌入式：Forwarder/显式 Listen accept → 127.0.0.1:22；内核 TUN：内核直达 sshd）
→ SSH 版本协商/密钥交换/认证照常
→ 回程：sshd 响应写回同一条 TCP → wg 加密沿已建会话原路返回
  → 客户端 gVisor 按四元组命中当初 Dial 的 endpoint → net.Conn 的 Read 收到
```

关键点：**回程无路由环节**——TCP 四元组直接定位 endpoint。这是"丢了路由能力、没丢连通性"的技术原因。

### 9.2 外网访问家里 Ollama（两端嵌入式）

外面设备 `engine.Dial(家里:11434)` → 家里宿主一条 `Listen 11434 → 127.0.0.1:11434` 中继 → Ollama 看到普通回环连接，**无需 `OLLAMA_HOST=0.0.0.0`**（对比内核 TUN 模式的已知别扭）。纯浏览器无 lattice 进不来——零公网暴露的本意。

### 9.3 ping 双向

- 被 ping：echo 到达本机 gVisor → 栈自动应答（P1 打开）→ 原路返回。无应用参与。
- 主动 ping：`Server.Ping` → echo request → 对端应答 → 按 `(id, seq)` 会话表认领 reply → 返回 RTT（P2）。ICMP 无端口，会话表是唯一认领手段——这正是 ping 受限而 TCP 不受限的根源。

## 十、与既有产品线的分层关系

| 层 | 载体 | 协议 | 特权 | 场景 |
|---|---|---|---|---|
| 应用层端点 | `shim.Server` 嵌入式（gVisor） | L4：TCP 双向 / UDP（路线） | 零 | App/车机/受限容器自入网；显式暴露 |
| 本机其他进程 | `Sandbox`（SOCKS5 + ForwardListener） | L4 | 零 | 不改造的程序进出 overlay |
| 全系统 | NE（macOS） | L3 全协议 | 系统授权 | 桌面全量 VPN |
| 网段/集群 | 子网网关（内核 TUN，task-H 线） | L3 全协议 | `NET_ADMIN` | 子网路由、k8s 基建层（非 CNI，是 gateway 形态） |

四层互补不互斥；同一 mesh 内可同时存在。WireGuard/ICE/LRP 下半身四层共用，零分叉。

## 十一、测试策略

- shim 侧：沿用 M0 双 netstack 包泵法——UDP 往返、ping 往返、Forwarder 拦截全部纯 Go 单测可覆盖；SOCKS ASSOCIATE 用本地测试客户端。
- 会话表：超时回收 + 并发场景 `-race`。
- 集成：mac-demo 环境（容器 `nc`/`nc -u` + LatticeMac「嵌入式引擎」页），沿用 M1–M3 的环境与健康检查约定。
- 大报文 MTU 用例进必测清单。

## 十二、风险与已知限制

1. **嵌入式节点无出站信令**：ICE 拨号依赖对端先握手（收敛窗口）。常驻对端场景已验证可用；两端同时冷启动的首连依赖收敛。**前置项：把节点的 NATS/relay 信令在嵌入式路径接通**（P 系列之前或并行）。
2. **`agentconfig.Conf` 全局状态**：单进程单引擎；NE 与嵌入式同进程共存需独立设计（当前 macOS 上 NE 在扩展进程、嵌入式在主进程，天然隔离）。
3. **吞吐**：用户态栈低于内核零拷贝，按家庭/办公码率验证，不承诺线速。
4. **UDP NAT 老化误伤**：会话超时回收可能截断长空闲流（如长连接游戏/RTCP）——超时值可配，默认保守。
5. **通配模式的安全权衡**：全端口转发交回"隐式暴露"的大半——必须作为显式配置档位而非默认，并在 UI/审计中明示。
6. **协议覆盖边界**：ICMP 出站探测依赖 P2；SOCKS 不承载 UDP/ICMP 之前，相应程序需直用引擎 API。
