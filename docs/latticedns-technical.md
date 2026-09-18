# LatticeDNS 技术原理

> 版本: v1（对应提交 `1d0030e2` + `c4403e4c`）
> 读者: Lattice 维护者与评审者
> 关联代码: `apple/engine/packet_tun.go`（拦截应答器）、`apple/LatticeTunnel/PacketTunnelProvider.swift`（Split DNS 声明）、`apple/Shared/`（名字归一化与 TunnelManager）
> 状态: 待评审

---

## 一、解决什么问题

家庭自托管场景的核心矛盾：**服务在家里（NAS/Mac/容器），人在外面**。

传统方案的痛点：

| 方案 | 问题 |
|---|---|
| 公网暴露 | 不安全，需要公网 IP/端口映射 |
| 手输 IP | 地址难记、会变、无法跨网 |
| 全量 VPN | 所有流量都绕道家里，其他 App 受影响 |
| 按 App 分流 | iOS 系统限制，消费级应用拿不到 per-app VPN 权限 |

**LatticeDNS 的答案：给家里每个服务起一个名字。** App 访问 `名字.lattice` = 自动回家。

```
https://immich.lattice      → 家里 NAS 上的 Immich
http://macbook-pro.lattice  → Mac 上的任何服务
http://ollama.lattice:11434 → Mac 上的本地大模型
```

关键性质：**只有 `*.lattice` 的查询和流量进入隧道**，其他一切 App 的流量 100% 不受影响。集成 N 个服务 = 注册 N 个名字，不是 N 次定制开发。

## 二、总体架构（三件套）

```
┌─────────────────────────────────────────────────────────┐
│ ① iOS Split DNS 声明（PacketTunnelProvider.makeSettings）│
│    matchDomains = ["lattice"]                            │
│    → 只有 *.lattice 的 DNS 查询进隧道                     │
│    → 其余域名走系统默认 DNS，零影响                       │
├─────────────────────────────────────────────────────────┤
│ ② 引擎 DNS 拦截应答器（packet_tun.go）                    │
│    拦截隧道内发往 UDP/53 且 qname 为 *.lattice 的查询      │
│    → 按设备表解析出 overlay IP → 构造应答包回注            │
├─────────────────────────────────────────────────────────┤
│ ③ 名字归一化（infra.NormalizeAppID）                      │
│    设备名 → NATS 安全的主题 token（客户端/服务端同规则）    │
└─────────────────────────────────────────────────────────┘
```

## 三、数据包的一生（端到端）

以手机 Safari 访问 `macbook-pro.lattice:8000` 为例：

```
① Safari 发起 DNS 查询："macbook-pro.lattice 是什么 IP？"
        │
        ▼ iOS Split DNS（matchDomains=["lattice"] 命中）
② 查询发往隧道内 DNS 服务器 10.96.0.1:53
        │  经 NEPacketFlow → 引擎 SendPacket
        ▼
③ packetTUN.WriteInbound 收到该 IP 包
        │  LatticeDNS 拦截器：
        │  IPv4? ✓ UDP? ✓ dstPort=53? ✓ qname 后缀 .lattice? ✓
        ▼
④ 查设备表：NormalizeAppID("MacBook Pro") = "macbook-pro"
   ≡ "macbook-pro" → 命中 → overlay IP 10.96.0.2
        │
        ▼ 构造应答包（源/目的对调 + 重算校验和）
⑤ 应答注入 outbound 队列 → PopOutbound → packetFlow
        │
        ▼ Safari 收到 A 记录：macbook-pro.lattice = 10.96.0.2
⑥ Safari 发起 TCP 连接 10.96.0.2:8000
        │  经 TUN → WireGuard 加密 → 隧道 → Mac
        ▼
⑦ Mac 上的服务应答 —— 完成
```

## 四、关键技术点

### 4.1 包方向与拦截点

`packetTUN` 实现了 wireguard-go 的 `tun.Device`，双队列方向（见文件头注释）：

| 队列/方法 | 方向 | 内容 |
|---|---|---|
| `WriteInbound(pkt)` | Swift → WG | **手机发出的包**（DNS 查询在这里拦截） |
| `Read(bufs)` | WG → 加密 | 手机出站包被 WG 读取加密外发 |
| `Write(bufs)` | WG → Swift | 远端解密后的包（应答从这注入回手机） |
| `PopOutbound()` | WG → Swift | 同上，Swift 侧弹出 |

LatticeDNS 的拦截点 = **`WriteInbound` 顶部**：手机发出的 DNS 查询在进入 WireGuard 之前被截获。

### 4.2 拦截判定（五道关卡，全部通过才本地应答）

```go
len(packet) ≥ 20              // 至少一个完整 IPv4 头
packet[0]>>4 == 4             // IPv4（IPv6 一律放行，v1 不处理）
packet[9] == 17               // 协议 = UDP
dstPort == 53                 // 目标是 DNS
qname 后缀 == ".lattice"      // 名字属于本网络
```

任何一关不过 → 原样转发 WireGuard（fail-open）。**绝无静默吞包**。

### 4.3 DNS 报文构造（miekg/dns）

使用 `github.com/miekg/dns`（go.mod 既有依赖）：

```go
query := new(dns.Msg)
query.Unpack(udp[8:])        // 解析查询（ID/问题段）
reply := new(dns.Msg)
reply.SetReply(query)        // 回显 ID/问题段，置 QR 位
reply.Answer = append(reply.Answer, rr)   // A 记录（TTL 10）
reply.Rcode = dns.RcodeNameError          // 未注册名字 → NXDOMAIN
dnsPayload, _ := reply.Pack()
```

### 4.4 IP/UDP 头手工构造与校验和

应答包 = 原查询包的 IP/UDP 头**源目的对调** + 新 DNS 载荷：

```
resp[0]        = 0x45                  (IPv4, IHL 5)
resp[2:4]      = 总长
resp[4:6]      = 沿用查询包 IP ID
resp[6:8]      = 0x4000               (DF)
resp[8]        = 64                    (TTL)
resp[9]        = 17                    (UDP)
resp[12:16]    = 原查询目的 IP          (即隧道 DNS 10.96.0.1)
resp[16:20]    = 原查询源 IP           (手机)
UDP 源端口      = 53（原目的端口）
UDP 目的端口    = 原查询源端口
```

校验和重算两处：**IPv4 头校验和**（含总数/TTL 变化）与 **UDP 校验和**（IPv4 伪头：源 IP + 目的 IP + 协议 17 + UDP 长度 + UDP 载荷）。

### 4.5 名字归一化（NormalizeAppID）

设备名来自用户手机（如 "iPhone 15 Pro Max"），**含空格**。而 NATS 主题（`lattice.signals.peers.<appID>`）与 LatticeDNS 域名（`<appID>.lattice`）都派生自 AppID——空格会导致主题非法、域名不可解析。

`infra.NormalizeAppID`：`TrimSpace` + `[^A-Za-z0-9._-]+ → "-"`。**客户端（注册前）与服务端（注册时）应用同一规则**，保证两端派生的名字一致。

归一化只用于 LatticeDNS/NATS 派生；**设备显示名（displayName）保持原样**，不受影响。

### 4.6 Fail-open 原则

LatticeDNS 的所有失败路径都是放行：

- 拦截器解析失败（非 IP/非 UDP/非 53）→ 放行
- qname 不是 `.lattice` → 放行
- 名字未注册 → NXDOMAIN 应答（明确告知，而非吞掉）
- 应答构造失败 → 放行原始查询包

**LatticeDNS 永远不会成为流量的单点阻塞**。

### 4.7 Split DNS 语义（为什么流量不绕路）

iOS `NEDNSSettings.matchDomains = ["lattice"]`：

- **解析层**：只有 `*.lattice` 的 DNS 查询交给隧道 DNS（10.96.0.1）；其他域名由系统默认 DNS 解析——不受隧道影响；
- **路由层**：进入隧道的流量由 `NEIPv4Settings.includedRoutes` 控制（overlay 子网 + 已选子网路由 + 可选 Exit Node）。

两层正交：解析决定"名字指向哪"，路由决定"包走哪条路"。

## 五、名字解析规则

| 注册设备名（displayName 可不同） | 归一化 AppID | LatticeDNS 名字 | overlay IP |
|---|---|---|---|
| MacBook Pro | macbook-pro | macbook-pro.lattice | 10.96.0.2 |
| iPhone | iphone | iphone.lattice | 10.96.0.8 |
| 001f676e5c3e（容器） | 001f676e5c3e | 001f676e5c3e.lattice | 10.96.0.7 |

- 名字冲突：注册表按 workspace 内唯一（`idx_peer_ws_name`），重名注册会被判为同一设备恢复或拒绝。
- 解析大小写不敏感（DNS 语义），`NormalizeAppID` 保留大小写、比较用 `EqualFold`。

## 六、边界与限制（v1）

1. **仅 IPv4 A 记录**：AAAA（IPv6）查询返回空应答；设备表尚无 IPv6 地址。
2. **仅解析组网成员**：NAS 等局域网设备的名字解析需要 Phase B（出口节点转发/NAT + 服务端注册表 v1.1）。
3. **无上游转发**：`.lattice` 之外的 DNS 查询不经过 LatticeDNS（由系统默认 DNS 处理）。
4. **无缓存**：每次查询实时查设备表（内存 map 语义，微秒级）；TTL 10 秒供下游缓存。
5. **名字归一化大小写保留**：`NormalizeAppID` 保留大小写，解析比较用 `EqualFold`。

## 七、安全考量

- `*.lattice` 查询只在隧道内解析（matchDomains 拦截 + 引擎应答），**不会泄漏到公网 DNS**；
- 设备表只含当前 workspace 的组网成员，跨 workspace 不可见；
- 应答只含 overlay 地址（10.96/24），不暴露家庭局域网拓扑（局域网拓扑需要 Phase B 的子网路由 + 显式选择）；
- DNS 应答器不做任何上游递归——它不是通用 DNS 服务器，只是名字表。

## 八、验证方法

**自动化（已落地）**：`apple/engine/packet_tun_dns_test.go` 三用例

1. `TestLatticeDNS_InterceptsLatticeQuery`：`.lattice` 查询 → NOERROR + A 记录 + 应答包源/目的对调正确；
2. `TestLatticeDNS_NonLatticePassesThrough`：非 lattice 查询原样进 WireGuard 队列、不本地应答；
3. `TestLatticeDNS_UnknownNameReturnsNXDOMAIN`：未注册名字 → NXDOMAIN 应答。

**真机清单**：

1. 连接隧道后，Safari 访问 `http://macbook-pro.lattice:8000`（Mac 上 `python3 -m http.server 8000`）→ 看到标记页；
2. `immich.lattice` 等未注册名字 → NXDOMAIN（浏览器报域名不存在）；
3. 关闭隧道后 `.lattice` 查询失败（符合预期：隧道关闭即无解析）；
4. 非 `.lattice` 域名（如 baidu.com）解析与访问行为与改动前完全一致。

## 九、实现文件索引

| 文件 | 职责 |
|---|---|
| `apple/engine/packet_tun.go` | TUN 双队列 + `WriteInbound` 拦截点 + `interceptLatticeDNS` 应答构造 + `SetDNSResolver`/`SetPeerSource` 接线 |
| `apple/LatticeTunnel/PacketTunnelProvider.swift` | iOS 隧道：`makeSettings` 的 Split DNS 声明 |
| `apple/LatticeTunnelMac/PacketTunnelProvider.swift` | macOS 隧道：同上 |
| `apple/Shared/LatticeAPI.swift` | `NormalizeAppID`（名字归一化，两端共用） |
| `internal/server/service/peer.go` | 注册时归一化（服务端同规则） |
