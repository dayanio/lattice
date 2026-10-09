# zyno 智能投屏 v1 设计 — Infuse 级播放器 × 一句话投屏

- 日期：2026-10-09
- 状态：Approved（brainstorming 会话四轮收敛，用户 2026-10-09 确认）
- 涉及仓库：`lattice`（引擎 / shim / 控制面）、`lattice-apple`（发起端栈）、`zyno`（播放器 / renderer，原 reflux）
- 关联：[cast.md](../../guide/cast.md)（协议与链路现状）、[tvOS 投屏设计](2026-09-30-tvos-cast-design.md)、[gVisor 嵌入式网络设计](2026-09-23-gvisor-embedded-networking-design.md)、[引擎 c-archive 迁移](2026-10-05-apple-engine-carchive-migration-design.md)、[lattice-copilot 设计](2026-09-22-lattice-copilot-design.md)

## 一、定位与问题

zyno 要做到 Infuse 级播放体验，同时支持"一句话把影片投到客厅电视、主卧、Mac、手机"。本文落定 brainstorming 收敛的三个架构问题：

1. 智能投屏是否需要 lattice overlay？局域网能不能单独实现？
2. 引擎以什么形态进 zyno——独立 App 双装，还是内嵌 SDK？
3. 命令走 NATS 还是直发？NATS 是不是瓶颈？

**结论**：overlay 在局域网内的形态就是 ICE host candidates 直连，不为局域网另建 mDNS 路；引擎内嵌为默认；命令通道维持"直发主路 + NATS 兜底"双路，NATS 在家庭场景的时延与压力均不可感知。

## 二、决策记录

| # | 决策 | 理由（详见关联章节） |
|---|---|---|
| D1 | **接收端范围**：v1 全自有设备（装 zyno + 引擎的 Apple TV / Mac / iPhone）；DLNA/AirPlay 兼容 sender 列为二期，仅预留 sender 抽象 | 同一 WiFi 下 AirPlay 标是现成体验预期，局域网兼容不是卖点；差异化在跨网 + 私有内容 |
| D2 | **引擎宿主形态**：全平台内嵌 gVisor 引擎为默认（单 App、零权限、不占 VPN 槽位）；iOS/macOS 保留 NE 作"全机接入/后台常收"升级项；tvOS 维持内嵌现状（无 NE，唯一形态） | 装一个 zyno 即得播放器 + 组网；与用户已有 VPN 和平共处（gVisor 设计 §11.3，reflux 即范本） |
| D3 | **边界语义**：zyno 不写组网逻辑，只消费 lattice CI 产出的预编译 xcframework + 窄 C ABI（~200 行）；组网代码、密钥、协议演进全在 lattice 仓 | 把"进程边界"换成"仓库 + 二进制边界"，职责不腐坏；zyno 钉引擎版本，升级 = 换二进制 |
| D4 | **命令通道**：维持双路——overlay UDP 47822 直发主路（等 ACK）+ NATS `lattice.cast.<AppID>.cmd` 兜底；shim P1 `ListenUDP` 前置以解锁嵌入式接收端直发 | 直发低延迟且不依赖控制面可达；NATS 兜底覆盖硬 NAT 与冷启动收敛窗口，保证即插即用 |
| D5 | **一句话入口**：v1 = zyno 内置确定性入口（搜片 → 选设备 → 投放，零 LLM）+ Siri 快捷指令语音壳；copilot 统一编排为后续演进（zyno 库能力按 LTP toolset 接入） | 家庭高频意图就是"搜片 + 投设备"，确定性流程可覆盖，无需引入 LLM 依赖；copilot 是多系统编排的脊柱，不是 zyno v1 的前置 |

## 三、分层职责

"一句话投到客厅"拆成四层，前三层是 lattice 存量能力，第四层是 zyno 主场：

| 层 | 干什么 | 归属 | 现状 |
|---|---|---|---|
| 听得懂 | "把 XX 投到客厅" → 意图 + 片名 + 目标 | zyno（v1 确定性流）/ copilot（后续 LLM 编排） | v1 待建 |
| 找得到 | "客厅" → 具体设备 | lattice 控制面设备注册表（AppID） | ✅ 已上线 |
| 到得了 | 命令到电视、流从发送端过来 | lattice 引擎（overlay 直发 + NATS 兜底 + ICE 选路） | ✅ NATS 路已验证；直发主路嵌入式侧待补（§五） |
| 播得好 | 4K HDR 直解、音轨字幕、刮削、UI | **zyno** | zyno 主场，lattice 不碰 |

## 四、引擎宿主形态（按平台）

| 平台 | 形态 | 后台 / 保活 | 说明 |
|---|---|---|---|
| tvOS | 内嵌（c-archive 静态库，单进程，收令即播放） | App 停在"等待投屏"页，`isIdleTimerDisabled` 防锁屏 | 现状（tvOS 无 NE，内嵌是唯一形态）；产品限制已在 tvOS 设计中明确接受 |
| iOS | 内嵌为默认；NE 作"全机接入 / 后台常收"升级项 | 前台投屏（发送）无碍；接收场景用户在场看手机，冻结无感 | 不占 VPN 槽位、免 entitlement 弹窗是转化率关键 |
| macOS | 内嵌默认（现有嵌入式引擎页模式）；NE 可选 | App 存活即引擎存活 | 与 LatticeMac 嵌入式形态一致 |
| Android | 二期 | 前台服务保活 | LatticeCast Android 设计已有，随 zyno Android 排期 |

配对 UX：电视亮码、手机扫码入网（零新服务端依赖，沿用 tvOS 设计）；配对时落设备命名（"客厅电视" / "主卧"）→ workspace 设备注册表 AppID——这是"找得到"层的数据来源。

嵌入式形态的已知功能缺口与本次影响：无 47822 主路（P1 后补齐，§五）、无 LatticeDNS / split routing（cast 链路不依赖）、iOS 后台冻结（§七）。媒体出站 `engine.Dial` 与发送端入站 `Listen 47823`（显式清单审批）嵌入式均已支持，链路成立。

## 五、投屏链路 v1（协议零改动）

cast 协议沿用 cast.md 五端点语义，三通道不变，本设计只补"嵌入式接收端直发"一块砖：

```
命令通道（双路，发送端已有逻辑零改动）
  CastCommandSender
    ├─ 主路：overlay UDP 直发 接收端 overlay-ip:47822，等 ACK
    └─ 兜底：NATS lattice.cast.<AppID>.cmd

媒体通道：接收端 engine.Dial ──Range/206──► 发送端 CastMediaFileServer（overlay 47823）
状态通道：接收端每 5s POST /__cast/status（overlay HTTP，不经 NATS）
```

**嵌入式直发闭环（引擎侧唯一新代码）**：

1. shim P1：`Server.ListenUDP(port)`——net.PacketConn 语义，与 ListenTCP 同构，含包泵单测（gVisor 设计 §8.2，一天级）；
2. 引擎接线：47822 端点收到的数据报直接喂现有 `CastCommandHandler`——解析 / 去重 / 事件回调全部复用，零新逻辑；ACK 从同一 UDP 端点原路发回，发送端"等 ACK"逻辑不变；
3. 前提与边界：直发要求两端 WireGuard 会话新鲜——电视引擎常驻在线即满足；两端同时冷启动的收敛窗口内直发可能失败，由 NATS 兜底覆盖（这正是双路互补的意义）。

**NATS 量级结论（记录，消除"压力"疑虑）**：命令为人手操作频率，每天 ~百条、KB 级总量；时延 LAN 1–2ms、跨网一个公网 RTT（20–50ms）；相对起播链路总时长（前台拉起 + 建流缓冲，数百 ms 到秒级）不可感知。媒体大流量恒走 ICE 直连 / LRP 中继，从不经过 NATS。

## 六、一句话入口（v1 与演进）

**v1（零 LLM、零新服务）**：

- zyno 内置确定性流：搜片（本地库 / NAS / 115）→ 投屏面板选设备（列 workspace 设备，按房间命名）→ `cast_play`；
- Siri 快捷指令作语音壳：App Intents 触发 zyno intent，参数（片名 / 房间名）透传——"播放 XX 到客厅"；
- 确定性 resolver 不走 LLM：家庭高频意图模板可完整覆盖，无模型依赖、离线可用。

**演进（不在本期）**：lattice-copilot 统一编排（2026-09-22 已定独立服务路线），zyno 库能力（搜片 / 继续观看 / 播放队列）注册为 LTP toolset（reflux 插槽已预留）；跨系统场景（"看电影模式" = 投屏 + 灯光 + 窗帘）归 copilot，zyno 零改动。v1 的入口设计不引入对 copilot 的反向依赖——copilot 挂了 cast 照常可投。

## 七、错误处理

| 现象 | 处理 |
|---|---|
| 直发无 ACK | 发送端自动落 NATS（双传输逻辑已有，零改动） |
| 接收端失联 | 状态上报超时感知（已有） |
| 控制面不可达 | 已注册设备间直发不受影响；新配对 / 设备发现不可用，UI 明示 |
| iOS 宿主退后台 | 发送端为前台意图；接收端场景（人在看手机）冻结无感；常驻接收需求引导走 NE"全机接入"升级项 |

## 八、测试

- 沿用 [cast e2e 测试指南](2026-09-30-cast-e2e-testing-guide.md) 矩阵，新增：**嵌入式接收端直发路径**（NATS 断开对照，验证直发独立成路）；
- shim P1 `ListenUDP`：包泵单测 + 大报文 MTU 用例（gVisor 设计 §13 约定）；
- 收端矩阵：iPhone / Mac / tvOS 嵌入式收端 × 直发 / NATS 双路，含 8.8GB 4K120 HDR 回归。

## 九、里程碑（建议）

| 里程碑 | 内容 | 仓库 |
|---|---|---|
| M0 引擎前置 | shim P1 `ListenUDP` + 引擎 47822 接线 → 嵌入式直发闭环 | lattice |
| M1 收端打磨 | tvOS / Mac / iOS 内嵌收端，直发 / NATS 双路全矩阵验证 | lattice / zyno |
| M2 发端面板 | zyno 投屏面板（设备列表 / 进度 / 状态）+ Siri 快捷指令入口 | zyno |
| M3（后续） | copilot LTP toolset 接入（续看 / 跨系统编排） | lattice-copilot / zyno |

## 十、非目标

- DLNA / AirPlay 兼容 sender（二期；M2 的 sender 抽象预留插槽）；
- 多 sender 会话仲裁（互踢 / 队列 / 音量同步）；
- Android / 鸿蒙收端；
- zyno 对 copilot / LLM 的任何运行时依赖。
