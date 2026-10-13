# Lattice ↔ Tailscale 全面对标(2026-10)

- 日期:2026-10-11
- 状态:Draft(供立项决策;取代 `docs/comparison.md` 的 2026-05 快照,该文已加过时标注)
- 依据:lattice 侧以 [CAPABILITIES.md](../../../CAPABILITIES.md) 已验证能力 + 本周真机验收(PR #62/#65)为准;Tailscale 侧以其公开文档与 2025 秋季更新周(Fall Update Week)发布为准
- 关联:`docs/design/exit-node-routing-and-dns.md` §6、[issue #63](https://github.com/dayanio/lattice/issues/63)、zyno 智能投屏设计(同类竞品对标先例)

## 一、定位对齐(先说清在比什么)

- **Tailscale**:零配置团队 VPN 起家,2025 年起长成"连接 + 零信任 + AI 治理"平台:Services(机器无关的命名资源)、Peer Relays(任意节点当 UDP 中继)、多 tailnet、工作负载身份联邦、**Aperture(AI 治理:跨模型/MCP server/agent 的安全与度量)**。
- **Lattice**:AI Agent 安全基础设施(身份 + 网络 + 隔离)+ 个人/家庭 mesh(C 端)。与 Tailscale 的交集只在"mesh 组网与出口";AI 这一格,双方是**不同路线的正面竞争**(见 §4-D4)。

## 二、全功能对照

图例:lattice 状态以已验证为准(✅ 可用 / 🔨 部分 / ❌ 无);Tailscale 状态为其公开能力。

### 2.1 组网基础

| 能力 | Tailscale | Lattice | 差距评估 |
|---|---|---|---|
| WireGuard mesh | ✅ | ✅ | 平 |
| NAT 穿透 | ✅(久经考验) | ✅ ICE(STUN/TURN)| TS 略优:边界场景打磨深 |
| 中继 | DERP **全球网络**(区域化)+ **Peer Relays**(任意节点当 UDP 中继,接近直连速度) | **FERRY 单点**(云端 1 台) | **显著差距**:跨网体验受中继位置与带宽限制;无冗余 |
| 协议开放性 | DERP 私有实现(客户端开源) | FERRY 开源(Apache-2.0 仓内)| lattice 略优:协议可自审自建 |

### 2.2 出口节点(本 tonight 主战场)

| 能力 | Tailscale | Lattice |
|---|---|---|
| 出口接管(0/0) | ✅ | ✅(table 5180,默认路由零触碰) |
| provider 掉线撤收 | ✅ | ✅(实测 ~25s) |
| 崩溃后宿主机零残留 | ✅(久经打磨) | ✅(#62:专属链 + 60s 门控 + `net cleanup`,真机验证) |
| 运行时分流(CN direct) | ❌(官方无视中国场景) | ✅ 静态 IP 分流已上线,域名分流 M2 暂缓 |
| IPv6 出口 | ✅ | 🔨 Phase 1(默认关,未真机验证 v6 出口) |
| 自动故障转移(多出口) | 社区求而未得(脚本方案) | ❌(同样无,平) |

**小结:出口节点功能面已对齐;可靠性刚补到"不输"的门槛;分流是 lattice 独有场景优势。**

### 2.3 DNS / 文件 / SSH / 服务暴露

| 能力 | Tailscale | Lattice | 差距 |
|---|---|---|---|
| MagicDNS(全网域名 + 搜索域 + Split DNS) | ✅ 成熟体系 | 🔨 LatticeDNS 仅 `*.lattice` 解析 | **大**:内网域名体验是 TS 高频卖点 |
| Taildrop 文件直传 | ✅ | ❌ | 差距(C 端有感知,非刚需) |
| Tailscale SSH(带审计的 SSH) | ✅ | ❌ | 不追(B 端特性) |
| Serve(内网服务暴露给 tailnet) | ✅ | ✅ 发布网关(publish gateway + 分享页)| 部分对齐 |
| Funnel(暴露到公网) | ✅ | ❌ | 不追(C 端定位无需公网暴露) |

### 2.4 策略 / 身份 / 多租户

| 能力 | Tailscale | Lattice |
|---|---|---|
| ACL / Grants(细粒度授权) | ✅(Grants + App capabilities) | 🔨 LatticePolicy(CIDR + 标签选择;域名/L7 未做) |
| SSO/IdP | ✅ 全家桶 | 🔨 Dex OIDC(Pro) |
| 多 tailnet / 多租户 | ✅(alpha) | ✅ Workspaces 隔离(自托管形态,路线不同) |
| 工作负载身份 | ✅ 身份联邦(免长期密钥) | ✅ AgentIdentity CRD + TTL(路线不同:声明式生命周期) |

### 2.5 客户端与平台

| 能力 | Tailscale | Lattice |
|---|---|---|
| 平台覆盖 | 全平台(桌面/移动/路由器/容器) | Linux/macOS/Windows CLI + Mac/iOS App + tvOS(cast) |
| GUI 成熟度 | ✅(窗口化 macOS UI + Taildrop) | 🔨 Mac/iOS 可用;Windows GUI 无 |
| 自动更新/钥匙串/开机自启 | ✅ | 🔨 部分 |
| 免 root 组网 | ✅ userspace | ✅ 用户态 netstack(同路线) |

### 2.6 控制面与部署

| 能力 | Tailscale | Lattice |
|---|---|---|
| 官方自托管控制面 | ❌(Headscale 为社区第三方) | ✅ 单二进制 latticed(K8s operator 可选) |
| 开源 | 客户端开源,控制面闭源 | ✅ core Apache-2.0(Pro 闭源) |
| 托管 SaaS | ✅ 主营 | ❌(规划中) |

### 2.7 AI / Agent / 沙箱(差异化主战场)

| 能力 | Tailscale | Lattice |
|---|---|---|
| AI 治理平台 | ✅ **Aperture**(跨模型/MCP/agent 的安全与度量)| ❌ 不做治理面板 |
| **Agent 网络隔离** | ❌ | ✅ gVisor 沙箱 + egress 控制(域名/L7 未完成) |
| **Agent 网络身份** | 🔨(Aperture 管身份与观测)| ✅ AgentIdentity CRD + TTL + 每 agent 独立密钥 |
| MCP server / 工具审计 | 🔨(Aperture 覆盖 MCP 观测)| ✅ 14 工具 MCP + tool spans |
| 出口节点/cast/mesh | ✅(基础设施本身)| ✅ 同栈复用 |

## 三、差距清单(按对 C 端定位的伤害排序)

1. **中继网络**(FERRY 单点 vs DERP 全球 + Peer Relays):跨网可用性与速度的天花板,C 端"在外面连家里"的体验直接受制于此;
2. **MagicDNS 完整度**:内网设备用名字互访是 mesh 的日常刚需;
3. **客户端打磨**:自动更新、开机自连、Windows GUI;
4. **域名级分流 M2**(自家 roadmap,断在 B2 结论上);
5. IPv6 出口真机验证;
6. 文档与社区规模(非代码差距,但影响获客与信任)。

## 四、优势清单(lattice 独有或领先)

1. **真自托管**:单二进制控制面,数据不出门;TS 官方无此能力(Headscale 是社区降级实现);
2. **开源 core**:FERRY 协议、引擎、控制面核心可审;
3. **AI Agent 沙箱**:gVisor 零特权隔离 + AgentIdentity + egress 控制 + MCP 审计——TS 生态无对应物;Aperture 做"AI 的身份与治理",lattice 做"AI 的网络与隔离",路线不同但同抢客户预算;
4. **中国网络场景**:CN direct 分流、国内可达的自建中继/控制面——TS 官方在此几乎不可用;
5. **Cast/投屏**:mesh 直投电视,Tailscale 无任何对应物;
6. **K8s 原生**:CRD operator + Pod 级策略。

## 五、立项决策(建议)

| # | 决策 | 理由 |
|---|---|---|
| D1 | **基础功能完备度不追平 TS**:Taildrop、Tailscale SSH、Funnel 明确不做;MagicDNS 保持 `*.lattice` 最小集 | C 端定位下这些是"TS 用户迁移理由"而非"留存理由";追平 = 无限军备赛 |
| D2 | **中继网络立项**:先 2-3 个云中继地域化(成本最低的体验杠杆),中期评估"FERRY Peer-Relay 化"(节点贡献中继,对齐 TS Peer Relays) | 差距清单第 1 项;跨网体验是 C 端留存命门 |
| D3 | **AI Agent 沙箱继续投入**(sandbox 域名过滤 M0 优先):与 Aperture 的差异化 = 它做治理与观测,lattice 做网络与隔离;两者甚至互补(Aperture 客户可能是 lattice 的集成对象) | 差异化主战场,且已有技术壁垒(gVisor + netstack 执行点) |
| D4 | **中国场景做成护城河**:CN direct 域名分流 M2 换路线重启(服务器侧分流 DNS,绕开 B2 的引擎侧死路)+ 国内中继 | TS 官方不可用 = 结构性空白 |
| D5 | **客户端打磨继续**:Mac/iOS 体验与自动更新;Windows GUI 不投 | C 端留存;与 D1 一致 |
| D6 | **企业多租户/SSO 深打磨不投**:Workspaces/RBAC/Dex 维持现状 | 与 C 端定位冲突 |

**总建议:继续立项,但换赛道打法——不与 Tailscale 拼"组网功能清单",拼"自托管 + 中国场景 + AI Agent 隔离"三个它结构性进不去的阵地(前两个是商业选择,后一个是技术路线)。**

## 参考来源

- Tailscale Fall Update Week 2025 总结:https://tailscale.com/blog/fall-update-week-25-wrap
- Tailscale changelog:https://tailscale.com/changelog
- Tailscale subnet routers 文档:https://tailscale.com/docs/features/subnet-routers
- lattice 能力基线:CAPABILITIES.md;真机验收:PR #62/#65 评论
