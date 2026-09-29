# Cast 网关 Phase 0 — iOS 可行性 spike 记录

**日期**：2026-09-28
**状态**：实测完成（第 ① 项不通过；第 ④ 项三方版待门户操作，其余按实情记录）
**对应设计**：`2026-09-28-cast-gateway-design.md`（§七 Phase 0）
**工作区**：`lattice-apple` 分支 `spike/cast-gateway-phase0`（= master + `8ea77fc`，即文档 §九 的改动被原样带上）；`reflux` 仓 `dev` 分支追加探针
**设备**：iPhone 15 Pro Max（winston，iOS 26.x），overlay IP `10.96.0.4`；Mac（`10.96.0.6`）经 overlay 访问；控制面/管理面在公网（101.36.119.12）

## Spike 代码形态（全部带「Phase 0 结束后删除」标记）

- `apple/LatticeTunnel/CastGatewaySpike.swift`：隧道扩展内的最小 Swifter 服务（`GET /ping`，绑 `:7822`，随 `startTunnel`/`stopTunnel` 起停，绑定看门狗每 10s 补位），附带测量钩子：
  - `/ping` 响应体带 pid / uptime / `phys_footprint` / `os_proc_available_memory()` 估算的内存上限；
  - `/ping?notify=1` 发本地通知（扩展起服务时也会主动发一条，不依赖 HTTP 可达）；
  - `/ping?appgroup=1` 在 App Group `group.io.lattice.shared` 的 `cast/cast-sink.json` 追加事件；
  - 每 30s 一条内存日志；每收到一次 /ping 计数（`pings`）。
- `apple/Shared/CastSpikeAppGroup.swift`：扩展与主 App 共用的 App Group 读写探针。
- `apple/Lattice/CastSpikeSupport.swift` + `LatticeApp.swift`：主 App 回前台跑探针（App Group 读写、回环 127.0.0.1 与 overlay 自连探测、Bonjour 本地网络授权取证）；通知点击转发 `reflux://cast`。
- `apple/Shared/TunnelManager.swift` 增 `queryCastSpikeState()`：经 provider 通道拉取扩展状态快照（扩展日志无法远程采集，借主 App 的 `devicectl launch --console` 通道带出——这是本次 spike 的主要观测手段）。
- `reflux`：`RefluxApple/CastSpikeProbe.swift` + `RefluxAppleApp.swift`——回前台探 `127.0.0.1:7822/ping`（`connectionProxyDictionary = [:]` 绕系统代理）+ App Group 检测；`.onOpenURL` 记录 `reflux://` 到达。
- Lattice 设置页未做任何改动（无开关、无入口、不依赖配对状态）：spike 服务是无 UI 的、随隧道起停的。设置里的「投屏接收」入口是此前 `8ea77fc`（文档 §九）删除的，本 spike 未恢复、也未新增任何开关。

## 实测结果

### ① 可达性：Mac 经 overlay 访问 `10.96.0.4:7822/ping` — **不通过**

**现象**（2026-09-28 18:43–19:33 反复实测）：

- 扩展进程存活、监听建立：状态快照 `{"listening":true,"bind_attempts":1,"bind_error":""}`；`tunnel-boot` 事件（绑定成功后才写）出现在 cast-sink.json。
- 隧道数据面正常时：Mac `ping 10.96.0.4` 通（RTT 7–11ms）。
- **TCP :7822 的入站 SYN 被静默丢弃（Mac 侧与手机本机一致）**：
  - Mac → `10.96.0.4:7822`：握手始终完不成（curl 5s 超时）；对照 `10.96.0.4:9999`（无监听端口）9ms 即被 RST；
  - 手机本机 Lattice App → `127.0.0.1:7822`（扩展已绑定、`listening:true` 时）：`NSURLErrorDomain -1001` 4s 超时（接口 lo0），扩展 `pings:0`——回环同样到达不了监听者；
  - 即「无监听端口正常 RST、有监听端口一律静默丢弃」，与来源（overlay/回环）无关。
- **锁屏 10 分 41 秒监测**（18:55:24–19:06:05，每 15s 一次，33 次全部超时）：期间扩展进程未退出；但**隧道的 overlay 转发在锁屏约 10 分钟后也死亡**（窗口结束时 `ping 10.96.0.4` 100% 丢包，进程仍在），重连后恢复。这本身是比 7822 更严重的问题：锁屏状态下连隧道数据面都不可靠。

**根因分析**（证据充分但未 100% 锤死）：

1. 「关闭端口 RST、监听端口丢弃」指向 **iOS 本地网络（Local Network）隐私权限的默认拒绝**：未授权 App 的监听端口入站连接被静默丢弃，而内核对无监听端口的 RST 不受权限影响。
2. Lattice（及其扩展）从未触发过本地网络授权：设置里没有「本地网络」开关；主 App 用 Bonjour 浏览探测返回 `failed(-65555: NoAuth)`，**系统连授权弹窗都不弹**（NoAuth 直接失败）——带 NetworkExtension 的 App 在 iOS 26 上拿不到这个授权入口，原因未明。
3. 被排除的假设：端口字节序问题（若有，7822 也会 RST）；被冻结进程占端口（那种情况握手能完成，且当时无其他进程存活）；引擎特殊处理 7822（引擎源码无 7822 字样）。

**复现步骤**：装本分支构建 → 手机开隧道并锁屏 → Mac `curl --noproxy '*' -m 5 http://10.96.0.4:7822/ping`（超时）→ 对照 `nc -z -G 2 10.96.0.4 9999`（秒回 refused）→ 打开 Lattice 看「连接」状态（扩展 state 快照 `listening:true`）。

### ② 内存：Go 引擎 + Swifter 常驻峰值 vs 上限 — **通过（数据充分）**

扩展状态快照（`handleAppMessage` 通道，pid 1312，起服务 3–84s 时点多次采样）：

| 指标 | 数值 |
|---|---|
| phys_footprint（引擎+Swifter+隧道常驻） | **12.7–13.3 MB**（多轮会话峰值） |
| os_proc_available_memory() | ≈ 39.5–39.7 MB |
| 上限估算（footprint + available） | **52,428,800 B = 精确 50 MB** |
| 峰值占比 | **≈ 24.6%–25.6%**，远低于 80% 红线 |

注：这是「引擎已在转发 + Swifter 已监听」的稳态值；spike 期间未做投屏负载，Phase 1 的网关再加协议状态机也只增量几百 KB 级。内存不是风险项。

### ③ 跨进程 — **拆成三小项，结果不同**

1. **扩展发本地通知：API 层通过**。隧道扩展内 `UNUserNotificationCenter.current().add()` 返回成功（扩展状态 `notify_posted:1`，授权状态=authorized，主 App 已获授权）。横幅实际显示与点击行为需要人工确认（本机当时锁屏/桌面状态未记录到截图级证据）。
2. **扩展深链打开 Reflux：直接深链不可行**。`NEProvider.openURL` 在 iPhoneOS 26.2 SDK 已被移除（`NEProvider.h` 无此方法，编译期实测）。可行链路 = 通知点击 → 落到 Lattice 主 App（`UNUserNotificationCenterDelegate`，代码已就位）→ `UIApplication.open(reflux://cast)`；`reflux://` scheme 已在 Reflux 注册（`CFBundleURLSchemes: ["reflux"]`）。该链路的「点击」环节待人工确认。
3. **其他进程连扩展的 `127.0.0.1:<port>`：不通过**。扩展监听就绪（`listening:true`）时，Lattice 主 App 进程连 `127.0.0.1:7822` 4s 超时（`NSURLErrorDomain -1001`，接口 lo0），扩展端 `pings:0`——回环连接根本没有到达监听 socket（与 ① 同一行为：有监听的端口一律静默丢弃，不分来源）。Reflux 侧同款探针已部署，但其 NSLog 无法经 `devicectl --console` 采集（Lattice 的可以），故本项以 Lattice App 的同机制探测为准测得。

### ④ App Group：三方读写同一 cast-sink.json — **两方通过；三方被门户阻塞（未绕过）**

- **`io.reflux.apple` 的 App Group 未在 Apple Developer 后台启用**（其本地描述文件 2026-08-27 生成、无 `com.apple.security.application-groups`；对照 `io.lattice.ios` 的描述文件里有 `group.io.lattice.shared`）。按约定未改 Reflux 的 entitlements、未绕过；已部署到 Reflux 的探针在授权启用后无需改动即可自动完成三方验证（当前它记录的是「NO container（未授权，预期内）」）。
- **已证的两方跨进程读写**（`/private/var/mobile/Containers/Shared/AppGroup/869842F0-.../cast/cast-sink.json`，`devicectl device copy from --domain-type appGroupDataContainer` 可随时拉取核对）：
  - 18:41:56 lattice-app（pid 1119）两次写：events 0→1→2；
  - 18:46:03 tunnel（pid 1132）追加 tunnel-boot；
  - 18:47:36 / 18:48:40 lattice-app（pid 1126）读 4 条 → 写 5 → 6；
  - 19:17–19:31 lattice-app（pid 1275/1311）多轮读写至 26 条。
- 结论：扩展进程与主 App 进程对同一 App Group 文件的读-改-写全程一致，无丢事件。**机制本身在 iOS 上工作正常**。

## 给用户决策的结论

**2026-09-28 用户已拍板：不走 §八 的 A/B 回退，改走「NATS 推送信令」新方向（设计文档 §十三），
后定稿为双传输：主 = LatticeDNS 同款引擎内截获 overlay 命令包，兜底 = NATS 推送。** Phase 0
的四项结论在新方向下的意义：

- **① 可达性不通过 → 不再是障碍**：推送传输下渲染端零监听端口，本地网络权限的入站过滤无从
  作用。兜底 NATS 通道实测锁屏 34 分钟 **19/19 送达**（含 1 条手动；发布→接收延迟 <1 秒），
  且期间 overlay 数据面断开（ping 100% 丢包）指令照达——指令通道与数据面解耦实测成立。主路
  （引擎内截获 overlay 命令包）待实现，是下一步。
- **② 内存通过**：~13MB / 50MB，非风险项。
- **③ 通知可发（实测 notify_posted）、深链经主 App 转发全链走通（实测：通知点击 → Reflux
  拉起）**；扩展直接 openURL 不可行已被 SDK 事实固定，接力链设计吸收了这一跳。
- **④ App Group 机制通过**（扩展+主 App 两方跨进程读写验证；三方待 Reflux 启用门户
  App Group `group.io.lattice.shared` 后自动补测，探针已就位，未绕过）。

**新暴露的产品级问题（与 cast 无关，另行处理）**：手机在蜂窝网络下 NATS 4222 不通时，引擎
起不来、隧道反复重启（i/o timeout 到 101.36.119.12:4222）——管理面可达性是全链前置条件。

## 附二：隧道数据面闪断的根因分析（2026-09-29 深夜，引擎日志实证）

当晚后续的投屏 E2E 反复失败，全部归因于**手机隧道数据面闪断**。用手机引擎日志
（`/tmp/lattice-ne.log`，4MB，覆盖 11:28-12:21 CST，每分钟 300-400 行 DEBUG）做的取证：

**观测到的事实**：

1. **引擎进程全程存活、NATS 全程在线**：锁屏 34 分钟测试（23:45-00:20 CST）期间，NATS
   发布 19/19 送达（<1s 延迟）——引擎没有被 iOS 杀死，控制面 TCP 存活；
2. **overlay 数据面在同一窗口死亡**：`ping 10.96.0.4` 100% 丢包，且进行中的拉流 TCP
   冻结（服务端阻塞式 send 无限挂起，直到对端彻底超时）；
3. **引擎日志对数据面死亡零感知**：死亡窗口内引擎日志密度不变（300-400 行/分）、无任何
   error/fail/timeout/reconnect——`liveness: ok` 探测每分钟 8 次全部报 OK；
4. **传输探测层在循环失败**（每分钟 ~20 次）：`Discover transport failed` +
   `SYN canceled`——lattice 自己的传输发现/探测协议没有完成建连；
5. **WG 层握手正常**：`Sending handshake response` 双向都在——底层 UDP 路径可达，
   WG 会话能建立；NATS（TCP 出站）也正常。

**结论（按可能性排序）**：

- **lattice 传输探测协议的 bug**：probe/ICE 状态机无法收敛（SYN 发出但对端 ACK 永不
  返回，或 ACK 返回但被状态机丢弃），导致对端路径（WG endpoint）无法建立/维持——
  而引擎的 `liveness` 探测探测的是错误的Health指标（探测包本身能发出/收到，不代表
  数据面通），**引擎对数据面死亡零感知、零自愈**。这是 `internal/server/transport`
  的真 bug，是隧道稳定性的主攻方向；
- 次要疑点：WG 的 endpoint（对端地址）依赖探测结果，探测失败 → WG session 无法
  重建 → 数据面持续死亡，直到人工重连触发重新入网。

**对 cast 功能的意义**：媒体拉流、UI 发起全链都依赖 overlay 数据面——隧道闪断期间
一切失败是必然。传输层修复后，本 spike 的全部能力（含 4K 拉流）预期直接可用；
修复前的过渡方案：保持手机亮屏可维持数据面（实测），但不可作为产品行为。

**下一步**：`internal/server/transport` 的 probe 状态机专项（SYN-ACK 为何不收敛、
liveness 探测为何与数据面脱节、网络变化时的重探测触发）——建议以本日志
（`/tmp/lattice-ne.log`，覆盖 11:28-12:21 CST 全程）为复现依据新开会话。

## 附：本次 spike 的遗留物与回滚

- lattice（`docs/cast-gateway-design`）：`feat(cast)` NATS cast 订阅 + `cmd/castcmd` 发布工具 + §十三 双传输设计。
- lattice-apple（`spike/cast-gateway-phase0`）：spike 代码（/ping 服务、cast 命令接收/通知/深链转发、状态通道）；project.yml 恢复 LatticeCastKit + Swifter（PlayerKit 未恢复，设置页未动）。
- reflux（`dev`）：探针 + 深链弹窗确证。
- 手机上现装的是 spike 构建；Phase 1' 落地后按正式版重装即清理。所有 spike 代码带删除标记。
