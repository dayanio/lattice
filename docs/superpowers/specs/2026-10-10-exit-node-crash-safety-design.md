# 出口节点崩溃自愈设计 — lattice 挂了不能断网

- 日期：2026-10-10
- 状态：Draft（用户报告真实故障后立项）
- 涉及仓库：`lattice`（Linux agent / 引擎 / 部署脚本）；Apple 客户端不受此问题影响（见 D7）
- 关联文档：
  - [C 端 agent 沙箱设计 §4.4](2026-10-10-consumer-agent-sandbox-design.md)（出口节点零污染 v2 方向的出处，本设计是其"近期修复"部分的前置）
  - [出口节点路由与 DNS](../../design/exit-node-routing-and-dns.md)
  - [ADR-0005 FERRY](../adr/0005-ferry-relay-protocol.md)（部署形态：`hack/deploy-cloud.sh` + systemd，本设计依赖）

## 一、问题

### 1.1 现象

设备选择出口节点后，lattice 进程崩溃或被 `kill -9`，整机断网：域名解析失败、流量黑洞。用户被迫手工修路由/换网络，对 C 端用户等于"这个软件碰不得"。

### 1.2 根因

消费端与供给端把五类**进程外状态**写在宿主机上，状态生命周期与进程生命周期没有绑定，异常退出没有清扫路径；其中 DNS 硬依赖把局部故障放大为"完全断网"：

| 状态 | 现状写法 | 进程死亡后的后果 |
|---|---|---|
| 路由（0/1 + 128/1、出口路由） | 主表直写；若曾"删默认路由再覆盖" | 默认路由未恢复 → 无默认；或残留死路由 → 黑洞 |
| 策略路由 `ip rule` | 直写 | **自动残留**（fwmark 规则不随进程死） |
| DNS | 改 `/etc/resolv.conf` | 指向已死的引擎 resolver → 全部解析失败（体感 = 断网） |
| iptables | 内建链直插 | 残留劫持/转发规则 |
| sysctl（供给端 `ip_forward`） | 宿主级设置 | 泄漏转发能力 |

## 二、决策记录

| # | 决策 | 理由 |
|---|---|---|
| D1 | **消费端永不删除原默认路由**：出口接管用 `0.0.0.0/1` + `128.0.0.0/1` 指向 TUN 的覆盖法（Tailscale 同款），默认路由原地不动 | TUN 随进程死亡被内核删除，指向它的路由被内核**自动撤销**，默认路由天然浮出——崩溃即恢复主路径，不依赖任何清扫逻辑 |
| D2 | **DNS 不改文件**：优先 systemd-resolved 链路级配置（`resolvectl dns <tun-iface>`），配置随 TUN 接口消亡；无 resolved 环境回退为"原子备份 + 标记文件恢复" | resolv.conf 直改是 DNS 断网的一半根源；链路级 DNS 的生命周期与接口绑定，平台代管清理 |
| D3 | **iptables 规则全部住专属链**（`LATTICE-EXIT` 等），内建链只留一条 `-j LATTICE-EXIT` jump | ufw / Docker / libvirt 同法：清理 = flush 专属链 + 摘一条 jump，幂等且不可能误伤他人规则 |
| D4 | **路由写专属表**（table 5180）+ `ip rule` 打标引入；主表永不直写 | 清理 = `ip rule del` + `ip route flush table 5180`，两条命令精确清扫 |
| D5 | **自愈双保险 + 手工兜底**：systemd `ExecStopPost=lattice net cleanup`（任何死法都执行）+ `ExecStartPre=lattice net cleanup`（开机/重启先清扫陈旧状态）+ `Restart=always`；另提供 `lattice net cleanup` 手工命令，覆盖"守护进程起不来"的死角 | 断网窗口 = 崩溃到重启的秒级；手工命令保证最坏情况一条命令自救 |
| D6 | **供给端走 netns 包裹**（沙箱 spec §4.4 近期方案）：`ip_forward`（per-netns sysctl）、FORWARD、MASQUERADE 全部 netns 内，宿主表零写入 | 与沙箱 M0 共享同一套 netns 基建；崩溃随 netns 蒸发 |
| D7 | **Apple 客户端排除**：NE 隧道由系统管理，引擎死 = 系统自动收走路由与 DNS，平台已自愈 | 本问题为 Linux（及后续 Windows）特有 |

## 三、设计细节

### 3.1 消费端（Linux，已选出口的设备）

- **状态写入契约（全部可清扫）**：出口路由 → table 5180 + `ip rule` 引入；对端/peer 路由 → 同表；DNS → resolved 链路级；iptables → `LATTICE-EXIT` 链；
- `0/1 + 128/1` 指向 TUN，默认路由永不触碰（D1）；
- 引擎死亡路径：TUN fd 关闭 → 设备删除 → 内核自动撤 `0/1+128/1` → 默认路由恢复 → 原始网络回来；`ip rule` 与专属链由清扫器兜底移除。

### 3.2 清扫器：`lattice net cleanup`（幂等，离线可跑）

按启动时记录的状态清单（落盘 `~/.lattice/net-state.json`，写前留痕）精确回收：

1. 删除本标记的 `ip rule` 条目；`ip route flush table 5180`；
2. flush `LATTICE-*` 链并摘除内建链上的 jump；
3. 恢复 resolv.conf（标记备份原子写回）或 `resolvectl revert`；
4. 复位其改动过的 sysctl（写前值已记录）。

幂等：任何一步目标不存在即跳过；重复执行无害；**不依赖 lattice 服务可用**（单独二进制路径直跑）。

### 3.3 systemd 接线

```ini
[Service]
ExecStartPre=/usr/local/bin/lattice net cleanup
ExecStopPost=/usr/local/bin/lattice net cleanup
Restart=always
RestartSec=2
```

崩溃 → systemd 触发 StopPost 清扫 → 2 秒后重启重新接管。断网窗口为秒级；即使 systemd 本身不在（手工裸跑），启动时进程内仍执行"先清扫再施加"。

### 3.4 供给端（Linux，出口提供者）

netns 包裹（D6），细节同沙箱 spec §4.4 近期方案；本设计只负责把现有 kernel 转发路径迁入 netns，userspace NAT 终局仍归出口 v2（沙箱 spec §4.4），不在本设计范围。

### 3.5 可观测

`lattice net status`：列出当前施加的路由/ip rule/DNS/链/sysctl 及来源标记；`cleanup` 执行时逐项打印"清理了什么"，事后可审计。

## 四、明确非目标

- userspace NAT 出口 v2（数据面重写，独立立项，见沙箱 spec §4.4）；
- macOS 客户端（平台已自愈，D7）；Windows 客户端的等价改造（wintun + 服务恢复选项）列 M2；
- "已断网且 lattice 二进制不可用"的带外修复（cleanup 设计为不依赖服务，但仍需 lattice 二进制存在——这是可接受的底线）。

## 五、阶段计划与验收

| 阶段 | 内容 | 验收 |
|---|---|---|
| M0 | 消费端可清扫状态重构（D1–D4）+ `lattice net cleanup` + systemd 接线（D5） | 选出口 → `kill -9` lattice → **≤5s 自动恢复上网**；`kill -9` 后禁用服务 → `lattice net cleanup` 一条命令恢复；`iptables-save \| grep -i lattice` 为空；`ip rule` / table 5180 无残留；resolv.conf 与写前一致；`make lint` / `make test` 绿 |
| M1 | 供给端 netns 包裹（D6） | 出口提供者宿主 `iptables-save` / `sysctl net.ipv4.ip_forward` 与安装前一致；`kill -9` 后无残留；消费者流量正常 |
| M2 | Windows 等价（wintun 适配 + 服务恢复选项） | 同 M0 语义在 Windows 复现 |

## 六、开放问题

1. 无 systemd 环境（lattice 客户端跑在容器里）的兜底：进程内信号清理 + 下次启动清扫已覆盖大半，剩余场景是否接受"重启容器"作为恢复路径；
2. table 5180 编号与 `ip rule` 优先级区间需要一处全局约定（避免与其他工具冲突），放 `docs/design/` 还是 ADR；
3. `net-state.json` 的落盘位置与权限（0600，XDG vs `/var/lib/lattice`）。
