# LatticeDNS 设计（v1）

> 日期: 2026-09-17
> 性质: 实现规范
> 关联: `2026-09-14-exit-node-subnet-route-design.md`（子网路由，本设计 Phase B 的前置）、
>       服务目录（家庭自托管服务接入模板）
> 状态: 已确认方向（用户选定命名 LatticeDNS · *.lattice）

---

## 一、目标

**名字即接入**：家庭内每个服务/设备获得一个 `*.lattice` 域名，任何 App 访问这个名字即自动"回家"——无需公网、无需手输 IP。

```
Immich 官方 App 服务器地址: https://immich.lattice
浏览器:                    http://nas.lattice
任意自研软件:              http://ollama.lattice:11434
```

只有 `*.lattice` 的查询和流量进入隧道（Split DNS），其他一切 App 的流量不受影响。

## 二、解析规则（v1）

- v1 解析范围 = **组网成员**。每台注册设备自动获得 `<NormalizeAppID(名字)>.lattice` → 该设备 overlay IP：
  - `macbook-pro.lattice → 10.96.0.2`
  - `iphone.lattice → 10.96.0.8`
- 归一化复用 `infra.NormalizeAppID`（FIX-4）：设备名带空格自动转连字符，两端规则一致。
- 未注册的 `.lattice` 查询 → NXDOMAIN。
- **非 `.lattice` 查询 → 原样转发 WireGuard**（行为与无 LatticeDNS 时完全一致）。
- v1.1（不在本 spec）：服务端注册表支持非组网目标（如 `nas.lattice → 192.168.1.10`），依赖出口节点转发/NAT 补全（Phase B）。

## 三、组件与数据流

```
手机 App ──"immich.lattice?"──▶ iOS Split DNS（matchDomains=["lattice"]）
                                     │ 查询发往隧道 DNS 10.96.0.1
                                     ▼
                          TUN.WriteInbound（手机发出的 IP 包）
                                     │ LatticeDNS 拦截器：
                                     │ UDP/53 + qname *.lattice？
                                     ▼ 是 → 查设备表 → 构造应答 → outbound 队列
                          PopOutbound → packetFlow → App 收到 A 记录
```

### 组件 1：引擎 DNS 拦截应答器（Go, apple/engine + packet_tun.go）

- `packetTUN.WriteInbound` 顶部挂拦截钩子（手机发出的包先过这里）。
- 解析 IPv4+UDP；dst 端口 53 且 qname 后缀 `.lattice` → 应答；其余返回 false 照常进 WireGuard。
- 应答构造用 `miekg/dns`（go.mod 已有）：解析查询 → `SetReply` → A 记录（TTL 10）→ 打包。
- IP/UDP 头手工换向 + 重算校验和（IPv4 头校验和 + UDP 伪头校验和）。
- 应答注入 `outbound` 队列（PopOutbound → packetFlow 送达 App）。
- 解析源：Node 持有的最近一次 netmap 设备表（`NormalizeAppID(名字) → overlay IP`），每次配置应用时更新。
- 拦截器未设置（nil）时全部照常转发——功能开关天然安全。

### 组件 2：iOS Split DNS 声明（Swift, PacketTunnelProvider.makeSettings）

```swift
let dns = NEDNSSettings(servers: ["10.96.0.1"])
dns.matchDomains = ["lattice"]
settings.dnsSettings = dns
```

- `matchDomains` 语义：仅 `*.lattice` 的查询交给隧道 DNS；其余走系统默认 DNS。
- `10.96.0.1` 为 overlay 内保留未分配地址，包经 TUN 进入引擎即被拦截。

### 组件 3：服务目录卡片（后续任务）

目录里每张服务卡显示 `名字.lattice`，作为该服务的"回家地址"。v1 由设备表自动生成（每台组网设备一张卡）；服务端注册表 v1.1 扩展。

## 四、边界与风险

- 仅支持 IPv4 查询（IPv6 的 `.lattice` AAAA 查询回 NOTIMP——设备表尚无 IPv6）。
- `.home`/`home.arpa` 等其他家庭后缀不在本 spec（避免与路由器习惯冲突；Split DNS 只认 `lattice`）。
- 应答包构造失败/解析失败一律放行转发（fail-open），绝不因 LatticeDNS 阻断正常流量。
- 引擎内 DNS 处理在 WG 例程线程上执行，必须非阻塞（纯内存查表 + 构包）。

## 五、验收

1. Go 单测：查询解析、`.lattice` 匹配、应答构造、非匹配放行、归一化名字解析。
2. 真机：已加入状态下 `ping macbook-pro.lattice` 通；Immich App 服务器地址填 `http://macbook-pro.lattice:2283` 可达（部署 Immich 后）。
3. 回归：非 `.lattice` 域名解析与流量行为与改动前完全一致。
