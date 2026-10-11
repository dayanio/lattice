# Roadmap

> Last updated: 2026-10-10
>
> Positioning: Lattice = AI Agent security infrastructure (identity + network + isolation)
>
> Current capabilities: [CAPABILITIES.md](./CAPABILITIES.md)
> Long-term vision: [docs/superpowers/specs/2026-05-16-lattice-future-vision-and-roadmap.md](docs/superpowers/specs/2026-05-16-lattice-future-vision-and-roadmap.md)

---

## Now — C 端 Agent 沙箱（2026-10-10 立项）

定位修正：**C 端保护功能**（个人/家庭，非 B 端沙箱即服务），Pro 订阅核心理由之一——"我机器上跑的 AI agent，不能背着我乱来"（Little Snitch for AI agents）。
设计：[2026-10-10-consumer-agent-sandbox-design.md](docs/superpowers/specs/2026-10-10-consumer-agent-sandbox-design.md) · issue #58

| 里程碑 | 内容 |
|---|---|
| M0 | 引擎域名级过滤（LatticeDNS 拦截 + 域名→IP 映射 + netstack 决策点）+ Linux 档能力分离加固（agent 专用 UID / 摘除 caps / owner-match）——即 v0.3 #5 正式提前至 Now |
| M1 | Mac 权限弹窗：首连弹窗（允许一次 / 总是允许 / 拒绝），策略热下发 |
| M2 | `sandbox run --image/--mount/--env`（Linux netstack 档），Claude Code 北极星场景演示 |
| M3 | 人话审计时间线 + iPhone 弹窗转发 |

隔离强度阶梯（档位关系见 spec §4.3）：**netstack（现有，全平台，永久执行面）** → runsc（Linux，另行立项，`--isolation` flag 随其落地）→ VM（macOS VZ / Windows WSL2）→ microVM（见 Later #11）→ 组合纵深。iptables 劫持是过渡品；netstack 承载策略/审计/DNS/身份，被所有后续档位复用。

---

## Parallel — v0.2 存量收尾（B 端/企业向）

Goal: **Close the "read-and-burn" loop + lower adoption friction**

| # | Task | Effort | Description |
|---|------|--------|-------------|
| 1 | TTL expiry CRD auto-GC | S | Auto-delete AgentIdentity/LatticePolicy resources on expiry |
| 2 | Community egress control | M | Basic `--egress-allow` CIDR whitelist for Community edition |
| 3 | natsAuditWriter implementation | M | Push sandbox audit events to NATS → latticed storage |
| 4 | Capability matrix public docs | S | Sync CAPABILITIES.md to docs site |

---

## Next — v0.3 Deep Isolation

Goal: **From network isolation to process isolation — agents cannot bypass the sandbox**

| # | Task | Effort | Description |
|---|------|--------|-------------|
| 5 | Domain-level egress filtering | M | DNS intercept + dynamic IP mapping + TTL caching inside gVisor netstack（→ 已由 C 端沙箱 M0 立项并提前，见上） |
| 6 | PID-to-TUN binding (eBPF cgroup/connect4) | L | Force agent process traffic through WireGuard, block direct eth0 bypass（能力分离加固先行缓解；runsc 档落地后该档内结构性不需要） |
| 7 | seccomp notify sidecar | L | Zero-instrumentation agent egress interception via seccomp user-space notify → sandbox policy decision |
| 8 | SandboxPod mode | M | `sandbox: pod` — K8s Pod + seccomp lightweight isolation (Community) |

---

## Later — v0.4+ Platform

| # | Task | Effort | Description |
|---|------|--------|-------------|
| 9 | L7 HTTP filtering (path/method/header) | M | HTTP parsing + rule matching inside gVisor netstack |
| 10 | Global topology visualization | M | D3.js force graph, real-time P2P/LRP path display |
| 11 | Firecracker MicroVM sandbox (Pro) | L | `sandbox: microvm` — true hardware-level isolation（隔离阶梯顶端，见 C 端沙箱 spec §4.3） |
| 12 | Managed control plane MVP (SaaS) | L | Optional lightweight hosted control plane, policy declarations only (no traffic) |
| 13 | Helm chart | S | Standard Helm deployment |
| 14 | Prometheus + Grafana integration | M | Standard monitoring integration |

---

## Priority Principles

1. **Security closure over polish** — Incomplete network isolation (eth0 bypass) is the biggest security gap; fix it first
2. **Community covers core scenarios** — Essential capabilities should not hide behind the Pro tier
3. **Every release eliminates entries from CAPABILITIES.md's 📋 column**
