# 下一阶段规划：从"能跑"到"有牙齿"再到"有人用"

**日期**：2026-09-10
**性质**：战略执行规划（替代 roadmap.md 中与当前定位冲突的条目）
**定位结论**：主线 = AI Agent 沙箱 + MCP 安全网关（mesh 是强制执行层）；副线 = NAS/家庭实验室获客口；冻结一切 mesh 泛化功能。

---

## 背景结论（本文档的依据）

1. mesh 数据面已够用（v0.2.0），继续投入只会进入 Tailscale/Netbird/Headscale 的主战场，单人无胜算。
2. MCP gateway 品类已拥挤（Proofpoint/Microsoft/Tetrate/TrueFoundry），唯一空档是"拥有网络层的 gateway"——策略在协议层 + last mile 网络层同时强制执行。
3. 沙箱"笼子"正在商品化（Codex/Claude Code 自带 Seatbelt/Bubblewrap；E2B 们做笼子云），**治理层（身份/策略/审计/网络）没有商品化**——这是 Lattice 的层。
4. 代码现状：PeerIdentity/AgentIdentity/MCPServer/AgentPolicy 已落地；`runSandbox` 的 policyChecker/auditWriter 被丢弃（无牙齿）；`internal/agent/sandbox`(GVisorInstaller+Runner)、`internal/agent/security`、`internal/agent/audit` 为孤儿包；前端拓扑/监控页为 mock；对外文档仍宣传 gVisor netstack（与实现不符）。

---

## Iteration 0 — 清场（2~3 天）

目标：消除叙事不一致和死代码，让后续迭代在干净地基上进行。

| # | 任务 | 说明 |
|---|------|------|
| 0.1 | 更新对外叙事 | run.go Long 帮助文本、README、CAPABILITIES.md：从"gVisor netstack"改为"内核 TUN + 分层隔离（netns → gVisor → microVM）"，与 Plan A 实现对齐 |
| 0.2 | 处理孤儿代码 | **保留** `internal/agent/sandbox`（I2 要用）、`internal/agent/security`、`internal/agent/audit`（I1 要用）；**删除** deprecated sidecar 及 `internal/agent/tproxy`、`internal/agent/gvisor`（netstack 用法已废弃） |
| 0.3 | 仓库卫生 | 移除已提交的 `lattice.db`、`cover.out`，补 .gitignore；修复 Makefile `test-latticed` 指向不存在的 `internal/nats` |
| 0.4 | 重写 roadmap.md | 用本规划替代 v0.2/v0.3 旧条目 |

**退出标准**：`make lint && make test` 通过；README/CAPABILITIES/`--help` 三处叙事一致。

---

## Iteration 1 — 有牙齿的沙箱（1~2 周）⭐ 最高优先级

目标：Linux 上 `lattice sandbox run test -- claude` 全链路可用：**有身份、出口受策略管控、行为进审计**。这是其它一切的地基。

| # | 任务 | 说明 | 关键文件 |
|---|------|------|---------|
| 1.1 | 接线 policyChecker | egress 策略在 wf0 路径生效（IP/CIDR 级），默认 deny 可选 | `cmd/lattice/cmd/sandbox/shared_linux.go`（现 `_` 参数）、`internal/agent/controller/policy_evaluator.go` |
| 1.2 | 接线 auditWriter | 沙箱流量事件写入审计链，前端 sandbox/audit 页显示真实数据 | 同上 + `internal/agent/audit`（孤儿包接线） |
| 1.3 | 域名白名单（proxy 路径） | HTTP_PROXY 路径支持域名级 allow/deny：允许 `api.anthropic.com`/`api.openai.com`，默认 deny 其余。CIDR 追不上 CDN，这是"沙箱里跑 coding agent 实用化"的先决条件 | `internal/agent/mcpproxy/`（已注入 HTTP_PROXY，policyDialer 已有 hostname deny 底子） |
| 1.4 | 参数级安全检查接入 | `internal/agent/security`（SQL 注入/路径遍历/危险命令/风险评分）接入 MCP proxy 调用链 | `internal/agent/security`（孤儿包接线） |
| 1.5 | TTL auto-GC | AgentIdentity/LatticePolicy 到期自动清理（roadmap 遗留 S 项） | controller 层 |
| 1.6 | 端到端 demo | 录制/脚本化：agent `curl attacker.com` 超时被拦 + 审计留痕 + dashboard 可见 | — |

**退出标准**：
- `sandbox run -- claude` 登录态复用（~/.claude）、TUI 交互正常；
- 域名白名单生效：API 可用、其它外联被拦；
- 审计在 UI 真实可见（消灭 sandbox 域的 mock）；
- e2e：sandbox + 策略 + 审计一条链。

---

## Iteration 2 — MCP 托管运行时（1~2 周）⭐ 头牌功能

目标："每个第三方 MCP server 一个一次性电脑"。对外一句话：**MCP 供应链安全的开源答案**。

| # | 任务 | 说明 |
|---|------|------|
| 2.1 | `lattice mcp run <cmd>` 入口 | 编排 tier：检测/自动安装 runsc → 失败降级 netns 档，用户无感 |
| 2.2 | runsc tier 外挂接入 | 复用 `internal/agent/sandbox` 的 GVisorInstaller + Runner（auto 模式）；`runsc --network=host do` 跑在 lattice 的 netns 内——网络归 lattice（身份/策略/审计），进程归 gVisor（syscall 隔离/归因） |
| 2.3 | 安装器加固 | 下载源加国内 mirror（现写死 storage.googleapis.com，国内不可用——**硬条件**）；下载失败静默降级；Ubuntu 24.04+ userns 受限时降级 |
| 2.4 | MCP server = mesh peer | 每个 `mcp run` 实例注册身份，AgentPolicy 工具级策略 + 审计生效 |
| 2.5 | 前端真实化 | mcp-servers 页显示运行时状态/策略/调用记录（真实数据） |

**退出标准**：`lattice mcp run -- uvx mcp-server-git` 一条命令：自动装 runsc → 沙箱内运行 → 加入 mesh 有身份 → AgentPolicy 拦截越权工具 → 恶意外联在网络层被掐死且有审计。

---

## Iteration 3 — NAS 副线打包与发布（1~2 周，可与 I2 交错）

目标：把 Community 版变成"有一个具体的人非用不可"的东西，然后发布收反馈。

| # | 任务 | 说明 |
|---|------|------|
| 3.1 | STUN/中继一键部署 | 基于 5/19 设计落成 compose + 国内部署文档 |
| 3.2 | NAS 打包 | 飞牛OS/群晖/UNRAID 的 docker-compose 模板起步 |
| 3.3 | OpenWrt 路由器包 | 杠杆最高的"客户端"：路由器入网 = 全家设备可达（解决 Apple TV/手机无客户端问题） |
| 3.4 | 拓扑/监控页决断 | 接真实 API 或从导航隐藏（外部用户见 mock 会质疑产品真实性） |
| 3.5 | v0.3 发布 + 推广 | HN / V2EX / Reddit r/selfhosted / 飞牛社区；两条 demo 视频有牙齿的沙箱 |
| 3.6 | 115media showcase | 个人项目节奏推进，完成后作为文档案例（验证普通用户全链路） |

**退出标准**：陌生用户按文档 10 分钟完成 NAS 远程组网；发布后收到真实 issue/反馈。

---

## 冻结清单（写进 roadmap.md，不再动）

- K8s operator 功能演进（保留可用，standalone 优先）
- Firecracker microVM、TEE 远程证明、A2A 协议适配、L7 过滤
- Intent Engine 扩展、拓扑可视化增强、Landing page 打磨
- 桌面 systray 客户端（Wails 计划押后）、iOS/Android 原生客户端

## 押后决策点

- **macOS Seatbelt tier**：等 I1 完成后评估。定位是 netns 的平台翻译（程序化生成 .sb 档案 + sandbox-exec），出口管控仅 proxy 层；有真实用户提出再做。
- **gVisor 强文件隔离**（最小 rootfs）：`runsc do` 文件隔离弱，MVP 够用，有需求再增强。

---

## 总节奏与成功度量

```
I0 清场(3天) → I1 有牙齿(2周) → I2 MCP运行时(2周) → I3 打包发布(2周)
                        ↑ 全程穿插：叙事一致性 + e2e 稳定
```

约 6~8 周到 v0.3。成功不是功能数，是三件事：
1. 一个 30 秒 demo：恶意 MCP/失控 agent 被当场掐死在网络层，全程留痕；
2. 一个陌生人完成 NAS 安装并留下来；
3. GitHub 上出现第一个不是自己提的 issue。
