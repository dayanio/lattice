# 出口模式的 IPv6：能力自适应的双栈隧道

**日期**：2026-09-25
**状态**：一期已实现，并在 iPhone 真机上验收了 A、D、E、F、G（2026-09-25，见 §8.1）；B、C 待有带 IPv6 的出口机。验收发现并修复了一个缺陷（§5.3：IPv6 默认路由必须是两条 `/1`）
**范围**：开源仓库的服务端（netmap、心跳/presence）、Linux agent（出口能力探测与网关）、引擎 `apple/engine`；私有仓库 `lattice-apple` 的两个 NE 扩展（`LatticeTunnel`、`LatticeTunnelMac`）。**不改 WireGuard 本身、不改 Windows/macOS/Linux agent 作为出口使用者的路径（二期）。**
**关联文档**：
- `docs/design/exit-node-routing-and-dns.md`（路由 / DNS / NAT 三层；其 §8 把「补 IPv6 接管」列为下一步）
- `docs/superpowers/specs/2026-09-24-domain-split-routing-design.md`（§8.1 记录了 IPv6 泄露的实测，并把它排在 M2 之前）
- `docs/superpowers/specs/2026-09-23-exit-node-dataplane-design.md`（Linux 出口网关的 IPv4 数据面）

## 一、背景与动机

开启出口节点后，只有 IPv4 流量进入隧道。设备的 IPv6 流量仍走本机网络：真机验收时，`api64.ipify.org` 显示的是设备自己的 IPv6 地址，而 `api.ipify.org` 显示出口的 IPv4 地址。用户看到的是「开了出口，但有些流量没经过出口」，行为不可预期，也存在泄露真实地址的问题。

**期望：** 开出口后，IPv4 与 IPv6 的出口位置一致：
- 出口机有可用的 IPv6 出口时，IPv6 也经出口出去；
- 出口机没有 IPv6 时，设备的 IPv6 被让位给 IPv4（应用立即回退），而不是绕过出口直连。

## 二、目标 / 非目标 / 成功标准

**目标**

1. 选了出口节点后，设备的 IPv6 流量不再绕过出口。
2. 按出口机的 IPv6 能力自适应：有能力则双栈经出口，无能力则 IPv6 不可用、应用立即回退 IPv4。
3. 出口能力由 agent 探测并汇报，不由管理员手填，避免声明与实际不符。
4. 新旧版本混跑、以及回滚都是安全的。

**非目标（本期不做）**

- 国内 IPv6 直连（对应 M1 的国内 IPv4 段）——三期。
- peer 之间互访 IPv6、`*.lattice` 解析 `AAAA`——四期。
- Linux / macOS / Windows agent 作为出口**使用者**（它们的 `provision_*` 目前只处理 IPv4 路由）——二期。
- 非 Linux 的出口机。
- 对 peers 的 IPv6 直连端点做排除路由（见 §九 已接受的取舍）。
- NPTv6、真实前缀分配等其它 IPv6 转换方式。

**成功标准（一期验收）**

| # | 场景 | 通过条件 |
|---|---|---|
| A | 无 IPv6 的出口 + 选了出口 | `api64.ipify.org` 显示 IPv4 且是出口的地址；不再出现设备本地 IPv6；网页正常、无明显卡顿；引擎日志里 ICMPv6 不可达的计数 > 0 |
| B | 有 IPv6 的出口 + 选了出口 | `api64.ipify.org` 显示**出口的 IPv6** |
| C | 能力撤回与恢复 | 出口的 IPv6 出站被阻断后，客户端在约 3 分钟内切到黑洞；恢复后切回隧道模式 |
| D | 没选出口 | 行为与现状完全一致（`api64` 显示本机 IPv6） |
| E | M1 国内直连 | IPv4 的国内直连仍然成立，不受影响 |
| F | 服务端开关关闭 | netmap 与升级前逐字节一致（回归） |
| G | 稳定性 | ICMPv6 限速生效，无包风暴，扩展进程不重启 |

## 三、现状（读代码 / 实测得到）

| 层 | 现状 | 位置 |
|---|---|---|
| WireGuard | 双栈，无限制；中继端点已在用 IPv6（`fd6c:7270::`） | — |
| 地址分配 | 纯 IPv4：`AllocateAddress` 只在 `10.96.0.0/24` 分配，`Peer` 只有一个 `Address` 字段 | `internal/server/reconcilers/netmap_builder.go`、`internal/server/models/peer.go` |
| netmap / AllowedIPs | 硬编码 `<addr>/32`，散在多处 | `netmap_builder.go:279`、`internal/server/transport/probe_factory.go:306/356/394`、`internal/agent/message_handler.go:86/168` |
| 出口通告 | `0.0.0.0/0` 由管理员在服务端声明（`Peer.AdvertisedRoutes`），agent 见到通告就配网关 | `internal/agent/message_handler.go:175-185` |
| Linux 出口网关 | 只有 `net.ipv4.ip_forward`、`iptables` 的 FORWARD 与 MASQUERADE；`wf0` 只配 IPv4 地址 | `internal/agent/provision/exit_gateway.go`、`provision_linux.go` |
| 心跳 | 载荷只有 `{appId, configVersion}`，每 30 s 一次，服务端写入内存的 `NodePresenceStore` | `internal/agent/heartbeat.go`、`internal/server/server/server.go`、`internal/server/nats/presence.go` |
| 引擎 | 对所有收到的 IPv6 包直接丢弃；`isLocalDst` 读 `pkt[16:20]`，只认 `10.96.x`；AAAA 空应答只在引擎拦截 DNS 的路径上生效，而出口模式的系统 DNS 是 `8.8.8.8` 直连，不经过它 | `apple/engine/packet_tun.go` |
| 引擎 ⇄ NE 的包封装 | 出站是长度前缀批；入站是数据报 socketpair；都不携带协议族，靠包的第一个字节判断 | `apple/engine/engine.go`、`packet_tun.go` |
| NE 扩展收包 | 每个收到的包都以 `AF_INET` 交给系统（iOS、Mac 各 4 处硬编码），**IPv6 包会被当成 IPv4 丢弃** | `LatticeTunnel(Mac)/PacketTunnelProvider.swift` |
| NE 设置 | 只设了 `NEIPv4Settings`，没有 IPv6 设置 | 同上 |
| 出口云主机（101.36.119.12） | 没有全局 IPv6 地址、没有 IPv6 默认路由，`curl -6` 不通 | 实测 |
| 扩展进程的 socket | 无法到达落在隧道路由内的地址（`ENETUNREACH`），只有排除路由内的地址能走物理网卡 | 分流 M0 (d) 实测 |

## 四、已定决策

| # | 决策 | 备选（已否决）及理由 |
|---|---|---|
| 1 | **按出口机能力自适应**：有 IPv6 则双栈经出口；无则 IPv6 走黑洞，应用回退 IPv4 | 只支持有 IPv6 的出口：泄露问题依旧；强制要求：体验最差 |
| 2 | **overlay IPv6 由 IPv4 确定性推导**：固定 ULA /64 前缀 + peer 的 IPv4 作为低 32 位 | 独立分配（要新字段、分配器、数据库迁移）；只给出口客户端分配（overlay 不是真双栈，以后要返工） |
| 3 | **出口用 NAT66 伪装**，与 IPv4 的 MASQUERADE 对称 | NPTv6（需要专用可路由 /64，云主机很难提供）；路由前缀 + 代理 ND（云主机基本不支持） |
| 4 | **黑洞由引擎合成 ICMPv6 不可达**，应用立即回退 | 静默丢弃（应用要等连接超时，网页明显变慢）；DNS 里过滤 AAAA（做不到：系统 DNS 是 `8.8.8.8` 直连，引擎看不到） |
| 5 | **服务端加开关，默认关闭**；关闭时 netmap 与升级前逐字节一致 | 不设开关：旧客户端会被 IPv6 CIDR 搞坏（§5.4） |
| 6 | **分期**：一期做最小闭环（服务端 + Linux 出口 + Apple 客户端） | 一次全做：跨层太多，风险过大 |

## 五、设计

### 5.1 地址与 netmap

- **前缀**：固定一个 ULA /64，默认 `fd6c:7270:6c74::/64`，可配置。它与现有中继伪地址 `fd6c:7270::` 同属项目的 ULA 空间但不同 /48：中继伪地址是外层端点，这里是内层 overlay 地址，不混用。
- **推导**：peer 的 IPv6 = 前缀 + 其 IPv4 作为低 32 位。例：`10.96.0.4` → `fd6c:7270:6c74::a60:4`。
- **一个共享函数**（新包 `internal/overlay6`，服务端、agent、引擎都引用；引擎在同一个 Go module 内，可以直接引用 `internal/`）统一负责推导与「是否 IPv6 CIDR」的判断，避免各处各写一份导致漂移。
- **netmap 输出**：每个 peer 的 `AllowedIPs` 从 `<v4>/32` 变为 `<v4>/32,<v6>/128`。出口 provider 通告了 `::/0` 时（见 §5.2），消费者除现有的 `0.0.0.0/0` 外再多得到 `::/0`。
- **`AllowedIPs` 里的 `/32` 只有一处产生**：`netmap_builder.go` 的 `dbToInfraPeer`。上表里 agent 侧的五处（`probe_factory.go` 三处、`message_handler.go` 两处）都是「值为空时的兜底」，仍生成 IPv4 `/32`，由下一次 netmap 覆盖；`mergeAllowedIPs` 按字符串取并集，天然保留 IPv6，不会像 `57230f68` 修的那个 bug 一样把扩宽的值缩回去。**唯一会覆盖本机 `AllowedIPs` 的是 `message_handler.go` 里 `Changes.AddressChanged` 那一支**（强制写成 `<addr>/32`），要改成保留已有的 IPv6 `/128`。
- **网关地址**：`<前缀>::1` 留作 ICMPv6 的源地址（§5.3）。它对应 IPv4 `0.0.0.1`，不在 `10/8` 内，不会与任何 peer 冲突。

### 5.2 出口 agent 与能力汇报

**谁声明、谁探测。** 「这个节点是出口」仍由管理员声明（`AdvertisedRoutes` 含 `0.0.0.0/0`），不变。IPv6 能力由 agent 探测并汇报，服务端据此决定是否给消费者展开 `::/0`。

**汇报通道复用心跳。** 心跳载荷加一个可选字段 `ipv6Egress`（布尔）。旧服务端忽略它，旧 agent 不带它，等价于 `false`。`NodePresenceStore` 记录该值，**变化时触发回调**，走与「provider 上下线」完全同一条路径（通知 workspace 刷新 netmap）。

**netmap 展开条件。** 给消费者加 `::/0` 当且仅当：provider 声明了 `0.0.0.0/0`，**且**在线（已有的 `providerLive`），**且** `ipv6Egress=true`。

**探测**（仅在本节点是出口时进行）：
1. 本机有全局作用域的 IPv6 地址（不是 link-local，也不是 ULA）。
2. 真的能通：对 `[2606:4700:4700::1111]:443` 或 `[2001:4860:4860::8888]:443` 任一做 TCP 连接，3 秒超时。连接成功已经蕴含「有到 IPv6 互联网的路由」，所以不再单独检查路由；云安全组拦出站 IPv6 的情况在这一步暴露。

每 60 秒一次；**滞回**：连续 2 次失败才撤回，1 次成功即声明。撤回的代价是客户端切到黑洞，所以不能抖。只有 link-local 或只有 ULA 地址，一律视为「无」。

**能力撤回的端到端时间**（验收 C 的「约 3 分钟」由此而来，最坏情况）：故障发生后最多 60 s 才被下一次探测发现，再连续失败 1 次又要 60 s（合计最多 120 s）；下一次心跳最多再等 30 s（心跳间隔）；服务端刷新 netmap 并推送到客户端最多再 30 s（客户端轮询间隔）。合计不超过约 3 分钟。恢复方向只需 1 次探测成功，比撤回快。

**先装规则，再汇报。** 探测通过后，agent 先安装下面的转发与 NAT66 规则，**规则装成功才汇报能力**；安装失败按探测失败处理，下一次探测重试。否则消费者会把 IPv6 送进一个转发不了的出口，网页全部卡死。

**数据面配置**（Linux，仅在探测通过后才装）：
- WAN 网卡取自 IPv6 默认路由的 `dev` 字段（不是固定第 5 列：无下一跳的默认路由 `default dev eth0 metric 1024` 会让列错位）。先给 WAN 网卡设 `net.ipv6.conf.<wan>.accept_ra=2`，**再**开 `net.ipv6.conf.all.forwarding=1`。**原因**：Linux 打开 IPv6 转发后默认不再接受路由通告（RA），依赖 SLAAC 的云主机会丢掉默认路由，IPv6 立刻断。
- `ip6tables` 的 FORWARD 规则（入向 `wf0`、回程 `ESTABLISHED,RELATED`）加 `-t nat POSTROUTING -o <wan> -s <mesh /64> -j MASQUERADE`（NAT66）。
- `wf0` 上配 `ip -6 addr replace <v6>/128`，并 `ip -6 route replace <mesh /64> dev wf0`，让回给 peer 的包能回到隧道（现在 `provision_linux.go` 只配 IPv4）。本机 overlay IPv6 取自 netmap 里本机 `AllowedIPs` 的 `/128`（服务端开关打开时才有），agent 不需要知道开关状态；配置失败只记警告，不让整个 netmap 应用失败。
- 沿用 `ExitGatewayCommands` 的「先检查再添加」幂等风格，新增 `ExitGateway6Commands`。撤回时不拆规则（无害），只是不再通告。

### 5.3 客户端（引擎与 NE）

**两种模式由引擎判定，Swift 不需要知道。**
- Swift 的规则只有一条：**选了出口节点（路由载荷的 `included` 里有 `0.0.0.0/0`）时，把 `::/0` 装进隧道**；没选出口则不设 `ipv6Settings`（现状）。
- 引擎判定：peers 的 `AllowedIPs` 里有 `::/0` → **隧道模式**（IPv6 包正常送进 WireGuard）；有 `0.0.0.0/0` 但没有 `::/0` → **黑洞模式**；没选出口 → 不接管。
- 路由载荷增加可选字段 `overlay6`（本机 overlay IPv6，由共享函数从 IPv4 推导）。Swift 用它配置接口，**不在 Swift 里重复实现推导**。载荷格式：仍是「无附加信息时为旧的裸数组；有 `excluded` 或 `overlay6` 时为对象」。

**NE 设置。** `NEIPv6Settings(addresses: [overlay6], networkPrefixLengths: [128])`，`includedRoutes` 是**两条 `/1`**：`::/1` 与 `8000::/1`，**不能**用单条 `::/0`（`NEIPv6Route.default()`）。真机实测：单条 `::/0` 会让 iOS 把所有全局 IPv6 目的地视为不可达——`connect()` 立刻 `EHOSTUNREACH`（No route to host），一个包都发不出去，`NWPath.supportsIPv6` 为 `false`，应用一直卡在「连接中」；引擎的黑洞逻辑一次都用不上（此时 `api64` 仍会显示 IPv4，那是 IPv6 整个不可用之后应用回退的巧合，不是黑洞在起作用）。改成两条 `/1` 后 `connect()` 成功、系统选隧道的 ULA 作源地址、`supportsIPv6` 为 `true`。这与 IPv4 默认路由拆成 `0.0.0.0/1` + `128.0.0.0/1` 是同一个原因。MTU 保持 1280，正好是 IPv6 的最小 MTU。DNS 服务器仍是 IPv4 的 `8.8.8.8` / `1.1.1.1`，走隧道，不改。

**引擎数据面。**
- **出站**（应用 → 引擎，`SendPacket`）：按版本位识别 IPv6。隧道模式直接进 WireGuard。黑洞模式在进 WireGuard 之前合成 **ICMPv6 Destination Unreachable（类型 1，代码 0）**，经 `writeFramed` 立刻回给应用。源地址用 `<前缀>::1`；回包带原包头部，总长不超过 1280。**限速**（默认每秒 100 个）；只对全局单播目的地址回应，不对链路本地、组播、以及本身就是 ICMPv6 错误的包回应。已合成的 ICMPv6 不可达数与被限速丢弃的数，写入扩展日志（沿用引擎现有的每 15 s 队列遥测），验收 A、G 据此判断。
- **入站**（peer → 应用，`packetTUN.Write`）：现在对所有 IPv6 包直接丢弃。改为：隧道模式放行。`isLocalDst`（当前只判断「dst 在 `10.96.x`」并读 `pkt[16:20]`）增加 IPv6 分支：目的地址在 overlay `/64` 前缀内才投递给系统，防止噪声包自放大。
- **路由拆分**：`computeExtraRoutes` 把 IPv4 与 IPv6 的 CIDR 拆开，并**跳过 peer 自己的 overlay `/128`**（现在只跳 `/32`），否则每个 peer 的 IPv6 地址都会被当成额外路由发给 Swift。

**一个已存在的 bug，会挡住 IPv6。** 两个扩展的收包循环把每个包都以 `AF_INET` 交给系统（iOS、Mac 各 4 处）。必须按包的第一个字节高 4 位选 `AF_INET` 或 `AF_INET6`。这个选择写成纯函数，用不依赖 Xcode 的逻辑测试覆盖。

### 5.4 兼容性与开关

| 组合 | 行为 |
|---|---|
| 旧出口 agent + 新服务端 | 不汇报 `ipv6Egress`，等价 `false`，客户端走黑洞（安全） |
| 新出口 agent + 旧服务端 | 心跳的新字段被忽略，没有 `::/0`，新客户端走黑洞 |
| **旧客户端 + 新服务端** | **会出问题**：netmap 里多出的 IPv6 CIDR 被旧引擎当成「额外路由」发给旧 Swift，`::/0` 会被误解析成无效的 IPv4 路由，`setTunnelNetworkSettings` 失败，隧道起不来 |

- **对策**：服务端配置项 `overlay-ipv6`（布尔，**默认 `false`**；环境变量 `LATTICE_OVERLAY_IPV6`，与现有的扁平配置键风格一致）；关闭时 netmap 与升级前**逐字节一致**，presence 里的 `ipv6Egress` 仍会被记录但不影响输出。上线顺序：先升级客户端，再打开开关。出问题关掉开关即可回滚。
- 新引擎另有防御：拆分 IPv4/IPv6 CIDR，且跳过 overlay `/128`（§5.3）。

## 六、错误处理与安全

- **总原则：任何不确定的情况都视为「没有 IPv6」，走黑洞，永远不泄露。**
- 探测失败、命令执行失败、云主机无 IPv6：一律 `ipv6Egress=false`。
- 引擎没起来时，`::/0` 进了隧道也没有出口，同样是「关闭失败」的方向。
- ICMPv6 合成只回应全局单播目的地址，并限速，避免被扫描流量放大。
- 非 Linux 出口：汇报 `false`。
- 出口机没有 IPv6 时不会被打开转发（探测通过才装），避免无意改变主机行为。

## 七、测试策略

**单元测试（无需设备）**
- `overlay6`：推导（多个边界 IPv4，含 `10.96.0.4`、`10.96.0.255`）、双向一致、IPv6 CIDR 判断。
- netmap：`AllowedIPs` 双栈输出；开关关闭时与旧输出**逐字节一致**；`::/0` 展开条件（在线 / 离线、`ipv6Egress` 真假）；`Changes.AddressChanged` 分支不再抹掉本机的 IPv6 `/128`。
- presence：`ipv6Egress` 变化触发回调，重复值不触发。
- Linux agent：`ExitGateway6Commands` 命令生成（含 `accept_ra=2` 先于转发）；探测逻辑（注入 runner 与 dialer）；滞回（注入时钟）。
- 引擎：ICMPv6 构造（用独立实现的校验和函数验证伪首部校验和合法）；模式判定表；限速；对链路本地、组播、ICMPv6 错误包不回应；`Write` 与 `isLocalDst` 的 IPv6 分支；`computeExtraRoutes` 拆分与跳过 `/128`；路由载荷 `overlay6` 的编码。
- Swift 逻辑测试：协议族选择函数；载荷解析 `overlay6`。

**真机验收**（§二）：A、D、E、F、G 用现有的无 IPv6 出口即可验证（已完成，§8.1）；B、C 需要一台带 IPv6 出口的机器（§九）。

**教训：** 验收 A 里「`api64` 显示 IPv4」这一条**不足以证明黑洞在工作**，必须同时看引擎的 `icmp6Sent` 计数增长，并用 IPv6 字面地址（如 `http://[2606:4700:4700::1111]/`）确认**立刻**失败而不是转圈。

## 八、分期

**一期内部的实施顺序（每步可单独验证）**

| 步骤 | 内容 | 仓库 |
|---|---|---|
| 1 | 共享包 `overlay6`：推导、IPv6 CIDR 判断，单元测试 | 开源 |
| 2 | 服务端：netmap 双栈（开关默认关）、在 netmap 里产生 `AllowedIPs` 的位置加上 IPv6、presence 记录 `ipv6Egress`、`::/0` 展开条件 | 开源 |
| 3 | Linux agent：`wf0` 配 IPv6、能力探测、心跳汇报、`ExitGateway6Commands` | 开源 |
| 4 | 引擎：路由拆分、模式判定、ICMPv6 黑洞、`Write` 与 `isLocalDst`、载荷加 `overlay6` | 开源 |
| 5 | Swift：协议族选择、`NEIPv6Settings`、载荷解析（iOS + Mac 扩展） | 私有 |
| 6 | 真机验收（已完成 A、D、E、F、G，见 §8.1） | — |

步骤 1 是 2、3、4 的前提；5 依赖 4。开源仓库的 1–4 可拆成 2 到 3 个 PR；私有仓库的 5 直接合并，随后按现有流程移动 `LATTICE_ENGINE_REF`。

### 8.1 一期真机验收结果（2026-09-25）

iPhone 15 Pro Max，出口为无 IPv6 的香港云主机，服务端 `overlay-ipv6` 打开：

| # | 场景 | 结果 |
|---|---|---|
| F | 服务端开关关 | 通过：行为与升级前一致（`api64` 仍显示本机 IPv6），`icmp6Sent=0` |
| A | 出口无 IPv6，选了出口 | 通过：`api64` 显示出口 IPv4，IPv6 字面地址页面立刻失败，页面无变慢，`icmp6Sent` 持续增长（累计 448） |
| D | 没选出口 | 通过：取消选择后 netmap 不再带 `0.0.0.0/0`，`icmp6Sent` 停止增长，`api64` 显示本机 IPv6 |
| E | 同时开国内直连 | 通过：`wf0` 上 7369 个包国内 IP 为 0，`wf0` 上无任何 IPv6 包 |
| G | 稳定性 | 通过：同一扩展进程、无 panic、无包风暴（限速丢弃仅 3 个，都是 MLD 组播） |
| B、C | 出口有 IPv6 / 能力撤回 | **未验证**（没有带 IPv6 的出口机） |

**未验证：** 隧道模式、出口 agent 的能力探测与 IPv6 网关规则、能力撤回与恢复的传播时间、macOS 扩展的 `/1` 改动（只保证编译与逻辑测试通过）。

**后续各期**：二期——Linux / macOS / Windows agent 作为出口使用者；三期——国内 IPv6 直连（APNIC 的国内 IPv6 段作为 `NEIPv6Settings.excludedRoutes`，与 M1 对称）；四期——peer 之间互访 IPv6、`*.lattice` 解析 `AAAA`、对 peers 的 IPv6 端点做排除。

## 九、风险与已接受的取舍

| # | 风险 / 取舍 | 应对 |
|---|---|---|
| 1 | **已接受：开出口期间，peer 之间的 IPv6 直连路径会少一批。** 装上 `::/0` 后，引擎自己的 socket 连 peers 的 IPv6 直连候选，会遇到「扩展进程的 socket 到不了隧道路由内地址」的限制，这些候选连接失败，ICE 回退到 IPv4 或中继，不会断 | 四期对 peers 的 IPv6 端点做排除路由 |
| 2 | **验证环境**：现有出口机没有 IPv6，B、C 无法验证（其余前提已在真机上验证：系统选隧道的 ULA 作源地址、路径 `supportsIPv6=true`、IPv6 包会到达引擎，剩下的未知只在出口一侧的转发与 NAT66） | 需要一台带 IPv6 出口的机器（给这台开通 IPv6，或另备一台）；不阻塞设计和实现，只影响双栈分支的真机验收 |
| 3 | 旧客户端遇到 IPv6 CIDR 会起不来隧道 | §5.4：服务端开关默认关，先升级客户端再开 |
| 4 | Linux 开 IPv6 转发会丢 RA 默认路由 | §5.2：先设 `accept_ra=2` |
| 5a | **服务端重启会清空出口能力状态**（存在内存里的 presence 中） | 出口 agent 的下一次心跳（最多 30 s）就会恢复；这段时间选了出口的设备走黑洞，IPv6 回退到 IPv4，不会泄露也不会断。可接受 |
| 5 | NAT66 的 conntrack 规模 | 与 IPv4 的 MASQUERADE 同量级，沿用同样的运维方式；本期不做额外调优 |
| 6 | 黑洞模式下，只有 IPv6 的目标（少见）无法访问 | 这是选择黑洞的代价；有 IPv6 的出口用户不受影响 |
| 7 | 探测目标（Cloudflare、Google 的公共 IPv6 地址）在某些网络不可达导致误判为「无」 | 误判的方向是安全的（走黑洞）；探测目标可配置 |
| 8 | Mac 整机测试受 Clash TUN 冲突限制 | 沿用现有约束：本期只在 iPhone 上做真机验证，Mac 扩展只保证编译和逻辑测试通过 |
