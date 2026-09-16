# Apple 客户端设计（macOS + iOS）

**日期**：2026-09-13
**状态**：Draft
**前提**：已有 Apple Developer Program 资质（可申请 Network Extension 能力与 Developer ID 分发）
**关联文档**：[下一阶段规划](./2026-09-10-next-iteration-plan.md)、[standalone reconcile 设计](./2026-09-10-standalone-reconcile-design.md)、[AI Agent Secure Mesh](./2026-05-29-ai-agent-secure-mesh-design.md)

---

## 一、定位

Apple 平台（macOS + iOS）成为 Lattice 客户端的**第一批 GUI 平台**，先于 Windows 托盘与 Linux 托盘：

- **macOS**：Developer ID 分发的菜单栏应用 + Network Extension（系统隧道进程），支持 App Store 外分发（需公证）
- **iOS**：App Store / TestFlight 分发的 SwiftUI 应用 + PacketTunnelProvider 扩展
- **服务器端零改动**：`latticed --standalone` 已提供全部所需接口（HTTPS 注册/令牌、NATS/HTTPS 信令、netmap 下发、心跳）

## 二、Apple 平台的根本约束与对策

| Linux 上的做法 | Apple 上的现实 | 对策 |
|---|---|---|
| root + 创建 TUN(wf0) | 不能创建 TUN；唯一合规路径是 **Network Extension**（`NEPacketTunnelProvider`） | NE 扩展进程内嵌 Go 引擎；`NEPacketFlow` 作为包 I/O |
| iptables 策略执行 | 无 iptables/netfilter | v1：**路由级执行**（NE 路由设置 + WireGuard AllowedIPs cryptokey routing）；v2：用户态过滤（端口级） |
| 常驻后台（进程不退） | 隧道活跃时系统保活 NE 进程；但 TCP 长连（NATS）会被断 | 信令重连 + HTTPS 轮询降级 |
| IPC unix socket | 不适用（App Group + 扩展进程通信） | App Group 共享配置 + `NEProviderConnection` IPC |

**核心认知**：Apple 上 WireGuard 数据面跑在 **NE 扩展进程内嵌的 Go 引擎**里——这是 Tailscale iOS/macOS 的已验证模式：Swift 壳 + gomobile 编译的 wireguard-go 引擎，NE 负责"拿包/交包"，Go 负责"加密/解密 + 信令 + 状态"。

## 三、总体架构

```
┌─ iOS App / macOS 菜单栏应用（SwiftUI）───────────┐
│  登录、Token 入网（deep link / 手输）、状态展示    │
│  连接开关（NEPacketTunnelManager）                │
└──────────────┬──────── App Group 共享配置 ───────┘
               ▼
┌─ PacketTunnelProvider 扩展（系统特权进程）────────┐
│  NEPacketFlow ←→ Swift 包桥 ←→ gomobile Go 引擎   │
│                                                   │
│  Go 引擎（复用现有代码）：                          │
│   wireguard-go（自有 fork）+ pion ICE/STUN         │
│   + 信令（NATS/HTTPS 降级）+ netmap 同步 + 心跳    │
│   + IPAM（服务端分配的 overlay 地址）              │
└──────────────┬───────────────────────────────────┘
               ▼ WireGuard UDP（ICE 打洞照旧）
        latticed --standalone ↔ 其他节点（零改动）
```

### 关键接缝：`tun.Device` 的 NE 适配器

`internal/agent` 的 NewNode 已通过 `tun.Device` 接口抽象 TUN（wireguard-go 接口）。Apple 适配器 = 实现该接口，包 I/O 桥接到 Swift 侧的 `NEPacketFlow`：

```
NEPacketFlow.readPackets ──→ PacketTun.ReadPacket ──→ wireguard-go 加密 ──→ UDP
UDP 收包 ──→ wireguard-go 解密 ──→ PacketTun.WritePacket ──→ NEPacketFlow.writePackets
```

WireGuardKit（WireGuard 官方 Swift 包）的 WireGuardKitGo 模块就是这个模式的现成参考——适配其构建脚本以编译 Lattice 的 wireguard-go fork。

## 四、Go 引擎对外接口（gomobile 暴露给 Swift）

```go
type MobileEngine struct{ ... }

func NewMobileEngine(cfg MobileConfig) (*MobileEngine, error)
  // cfg: ServerURL, JoinToken, InterfaceName, NATS/HTTPS 端点, PrivateKeyHex(可选,重注册复用)

func (e *MobileEngine) Start(ctx) error      // 注册 → 拉取 netmap → 启动 WG
func (e *MobileEngine) Stop()                // 优雅关闭
func (e *MobileEngine) Status() StatusInfo   // 状态、overlay IP、appliedVersion、peers
func (e *MobileEngine) SetMTU(mtu int)
// 包 I/O 由 Swift 侧桥接（gomobile 绑定 ReadPacket/WritePacket 字节切片方法）
```

## 五、策略执行（Apple 上的分级）

| 级别 | 机制 | 覆盖 |
|---|---|---|
| v1 路由级 | NE 路由设置：只把 `AllowedIPs`/允许网段路由进隧道 + WG cryptokey routing（目的地址不在 AllowedIPs = 加密前即丢弃） | 网段级可达性控制；默认拒绝天然成立（不路由 = 不可达） |
| v2 用户态过滤 | Go 引擎内对出站包做五元组过滤（复用 PolicyEvaluator 的判定逻辑） | 端口/协议级 |
| v3 上报 | 拦截计数经心跳上报（补齐命中统计） | 可观测性 |

与 Linux 的 iptables 语义对齐：**默认拒绝 + 显式放行**；差异（无法做端口级 drop）在 CAPABILITIES 文档明确标注。

## 六、与现有代码的接缝

| 现有资产 | Apple 处置 |
|---|---|
| `tun.Device` 抽象 | ✅ 实现 NEPacketTun 适配器 |
| wireguard-go fork | ✅ gomobile 编译（WireGuardKitGo 模式） |
| NATS 信令 | ✅ 可用；iOS 需重连健壮性 + 轮询降级 |
| join token 入网（HTTPS） | ✅ 直接可用 |
| iptables/ebpf enforcer | ❌ 不可用；Apple 上策略执行降级为路由级（v1），端口级 v2 |
| `RunNetmapSync` | ✅ 直接复用（2s/30s 间隔由服务端下发节奏决定） |
| 心跳 | ✅ 复用（带 configVersion） |
| IPC unix socket | ❌ Apple 上由 NE 框架替代（App Group + 扩展进程通信） |
| `lattice up -d`/service install | ❌ Apple 上由 NE 框架替代（系统就是"服务管理器"） |

**服务器端零改动**——这是 standalone 化的直接红利。

---

## 七、iOS 硬约束（风险表）

| 风险 | 量级/说明 | 对策 |
|------|----------|------|
| NE 扩展内存上限 | PacketTunnelProvider 约 50MB 量级，超限即被系统杀 | Go runtime 内存预算 + pion ICE/NATS 裁剪；M1 最早验证项 |
| 后台 TCP 长连被断 | NATS 连接在后台被挂起/断开 | 重连退避 + HTTPS 轮询降级；netmap 同步不依赖长连 |
| Network Extension 能力审批 | 开发者后台申请 packet-tunnel-provider 能力，需数天 | 立即提交申请 |
| Token/配置跨进程 | App 与 NE 扩展通过 App Group 共享 | App Group container 存 join token 与配置 |
| WireGuard fork 的 gomobile 构建 | 构建脚本需适配 Lattice fork | 参考 WireGuardKitGo 官方构建脚本 |

---

## 八、里程碑

| 阶段 | 内容 | 产出 |
|------|------|------|
| **M0 核心解耦**（~1周） | `internal/agent` 拆"可移植核心"（信令/netmap/WG/IPAM/心跳）与 OS 层（TUN/iptables/IPC）；enforcer 可选化（none 模式） | 可移植核心包 + 回归测试 |
| **M1 macOS**（2~3周） | gomobile 构建（iOS+macOS 双目标）+ Swift NE 适配器 + 菜单栏 App + Token 入网 + Developer ID 公证 | **真机 ping 通 overlay IP** |
| **M2 iOS**（2~3周） | SwiftUI App + PacketTunnelProvider + App Group + TestFlight | iOS 同样闭环 |
| **M3 执行增强** | netstack 级策略过滤（端口级）+ 拦截计数上报 | 与 Linux 执行粒度对齐 |

**前置申请（立即可提）**：开发者后台 Network Extension 能力（packet-tunnel-provider）+ App Group；Developer ID 证书 + 公证。

---

## 九、桌面路线的关系

- **不冲突且互补**：M0 的核心解耦对两端都有利——桌面 GUI 壳（Wails，Linux/Windows 优先）同样 import 可移植核心
- Apple M1 之后，Wails 托盘（Linux/Windows）可并行推进
- 移动端之外的"全平台"（桌面三端）在此路线完成后即达成

---

## 十、测试策略

- **Go 侧**：NEPacketTun 适配器用 mock packet flow 单测；重连退避状态机单测
- **集成**：macOS 本机 + Docker latticed（现成 demo 栈）→ 入网 → ping overlay → 策略生效验证
- **iOS**：模拟器跑 UI 逻辑；真机 TestFlight 跑隧道
- **回归**：Linux 连通性 E2E（8 项）保持全绿（OS 层解耦不得破坏 Linux 路径）
