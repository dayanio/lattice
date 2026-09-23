# 出口节点（Exit Node）数据面设计 — 二期

> 状态：v1（随实现提交） · 日期：2026-09-23
> 关联：capabilities 设计 §五（广播子网路由）、P4 计划（明确"本机转发能力二期另做"）、`2026-09-23-gvisor-embedded-networking-design.md` §九

## 一、现状（一期已通/未通）

| 环节 | 状态 |
|---|---|
| 声明：节点广播 `0.0.0.0/0` 等网段 | ✅ `advertised-routes` API → `t_peer.AdvertisedRoutes` 列 |
| 选择：消费端 opting-in | ✅ `route-selection` API → `t_peer_route_selection` |
| 信令：netmap 编译 | ✅ `netmap_builder` 对已选中 provider 的 `AllowedIPs` 追加其宣告网段（含 `0.0.0.0/0`） |
| 消费端路由（iOS/macOS） | ✅ 引擎 `OnRoutesChanged` → NE `includedRoutes` 含默认路由 + 排除管理面地址（防回环） |
| **提供端转发（数据面）** | ❌ 本文范围 |

一期结论：消费端会把去往 `0.0.0.0/0` 的流量交给 provider 的 WG 会话，但 provider 不转发（无 `ip_forward`、无出口方向 NAT），流量到 provider 即终止——选择后等于断网。

## 二、设计原则：宣告即网关职责

节点**主动宣告路由**（尤其是 `0.0.0.0/0`）= 自愿承担网关职责。agent 从 netmap 的自身条目读到宣告网段后，**幂等安装转发规则**；不宣告则什么都不装。消费端选择与否仍走审批/选择流，与提供端解耦。

## 三、提供端数据面（Linux v1）

触发：netmap 应用后，若自身条目 `advertisedRoutes` 非空。

规则（全部幂等：check→add，复用 `provision_linux` 既有模式）：

```bash
sysctl -w net.ipv4.ip_forward=1
iptables -C FORWARD -i <wg-dev> -j ACCEPT  || iptables -A FORWARD -i <wg-dev> -j ACCEPT   # mesh 入站
iptables -C FORWARD -o <wg-dev> -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT \
                          || iptables -A FORWARD -o <wg-dev> -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT  # 回程
DEV=$(ip route show default | awk 'NR==1{print $5}')
iptables -t nat -C POSTROUTING -o "$DEV" -s <meshCIDR> -j MASQUERADE 2>/dev/null \
                          || iptables -t nat -A POSTROUTING -o "$DEV" -s <meshCIDR> -j MASQUERADE                  # 出口 NAT
```

- `<wg-dev>`：节点 WG 接口名（`deviceManager.GetDeviceName()`）。
- `<meshCIDR>`：本机 overlay 地址所在 /24（如 `10.96.0.0/24`），从自身地址推导——NAT 只对 mesh 来源收口，不做全量 NAT。
- 平台范围：**Linux v1**（容器/服务器是最现实的提供端）。macOS NE 作为提供端的转发走 NE 自身能力，后续单独设计；Windows 同。

## 四、消费端（已具备，本文补齐口径）

- iOS/macOS NE：netmap 追加的 `0.0.0.0/0` 经 `OnRoutesChanged` 进 `includedRoutes`，管理面地址进 `excludedRoutes` 防回环（已实现）。
- Linux 消费端：provisioner `ApplyRoute(0.0.0.0/0, add)` 即默认路由进隧道（注意多路由度量，`ip route replace` 幂等）。
- **禁止环回**：消费端不得把发往管理面/中继的流量再送进隧道（NE 已按服务器地址排除；Linux 侧依赖默认路由粒度，二期不做全量 0/0 的 Linux 消费端强制）。

## 五、信令增量

`infra.Peer` 增加自描述字段：

```go
AdvertisedRoutes []string `json:"advertisedRoutes,omitempty"`
```

`netmap_builder.dbToInfraPeer` 为**每个** peer 填充其宣告网段（含自身条目）——agent 由此得知"我宣告了什么"。旧 agent 忽略新字段，向后兼容。

## 六、安全与审计

- 转发规则只对宣告网段来源收口（`-s meshCIDR`），不裸开全量转发。
- `FORWARD` 默认策略不变（不 ACCEPT 全部），只加显式放行条目。
- 网关规则安装/移除打日志（`agentlog`），命中级审计随连接审计管线（不做逐包审计）。
- 提供端拒绝转发的回程由消费端超时可见（如实失败，不静默黑洞——与 §六 错误透传原则一致）。

## 七、验收（mac-demo / 云端环境）

1. 云端 `lattice-cloud-node` 宣告 `0.0.0.0/0`；Mac 选择它为出口。
2. Mac `curl ifconfig.me` 返回云主机出口 IP（而非本机出口）——**数据面端到端成立**。
3. 取消选择后 Mac 直连出口恢复。
4. 幂等：重复应用 netmap 不产生重复 iptables 条目（`iptables -C` 前置）。

## 八、已知限制

- 提供端限 Linux；macOS/Windows 提供端转发后续设计。
- 消费端 Linux 的 0/0 默认路由接管依赖 `ip route replace` 度量，多出口环境需人工确认。
- 提供端容器化部署需 `NET_ADMIN`（lattice-cloud-node 现有部署满足）。
