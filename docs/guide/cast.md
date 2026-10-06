# Cross-network Cast（跨网络投屏）

Cast 是构建在 Lattice overlay 网络之上的投屏能力：手机 / Mac 上的 Reflux 把视频"投"到加入同一 Lattice 网络的电视上，**不限同一局域网**——只要两端都在 Lattice 网络里（直连或经中继），投屏即可用。

## 与传统投屏的区别

| | DLNA | Google Cast / AirPlay | Lattice Cast |
|---|---|---|---|
| 网络范围 | 同一局域网 | 同一局域网 | 任意 Lattice 网络（跨地域、跨 NAT） |
| 发现机制 | mDNS/SSDP 广播 | 生态内发现 | 控制面注册表（设备即节点）+ 局域网 mDNS |
| 接收端门槛 | 电视预装 | 硬件或认证（Chromecast / MFi） | 软件：装 App 即是（tvOS / Mac / Go renderer） |
| 媒体传输 | renderer 拉流，格式协商脆弱 | sender 给 URL，receiver 拉流 | 同 URL 拉流 + overlay 直连，不通时自动走中继 |
| 安全 | 依赖局域网信任（裸奔） | 生态内托管 | WireGuard 加密 + renderer Bearer token |

## 适用场景

一句话定位：**Google Cast / AirPlay 做"同一 WiFi 的客厅投屏"，Lattice Cast 做"私有内容 × 自有设备"的跨网播放通道**。

核心场景都是跨网的——这是上面三家协议结构上做不到的：

- **外网手机 → 家里电视**：人在公司或出差，家里电视开着 Reflux，手机把 NAS / 115 网盘的影片投过去，家人在家直接看。命令走控制面（NATS），媒体走 overlay 直连（ICE 打洞，不通走 FERRY 中继）——内容与流量都不经过第三方。
- **两处住宅互投**：老家和城里的电视都在同一 Lattice 网络，任意一端的媒体库可被另一端投放。
- **Mac/PC 作为接收端**：卧室 Mac 装上 Reflux 即是富格式接收端（PlayerKit 渲染），手机把网盘剧集投给电脑看；LatticeMac 内置 AVPlayer 接收端在其离场时自动兜底。

局域网内 Lattice Cast 与 AirPlay / DLNA 并存且可用（接收端是软件，无需任何硬件门槛），但**同一 WiFi 下用户没有理由优先选它**——遥控器上贴着的 AirPlay 标是现成的体验预期。这段能力是跨网通道顺路覆盖的一段，不是卖点。

## 什么时候不用 cast

- **内容在云端流媒体**：电视 App 直接播更简单，还省一次拉流。cast 的价值在**私有内容**——NAS、网盘（115）、本地文件。
- **接收端不归你管**：酒店、朋友家的电视装不了你的 App，跨网 cast 无从落地。
- **客厅多人同时投放**：协议 v1 尚无 sender 会话仲裁（互踢/队列/音量同步是 Google Cast 打磨了十年的部分），多人场景会暴露。

## 架构总览

```
 发送端（iPhone / Mac）                控制面（latticed）              接收端（Apple TV）
┌─────────────────────┐          ┌────────────────────┐          ┌─────────────────────┐
│ Reflux App          │          │  NATS signaling    │          │ 隧道扩展进程（NE）     │
│  ├ 媒体服务 :47823 ◄┼──overlay─┤                    ├──NATS───►│  └ Lattice 引擎      │
│  │  (Range/206)     │  WireGuard│  设备注册表         │  cmd 订阅 │     ├ 收命令          │
│  └ 发布 cast 命令 ──┼───NATS──►│  投递 cast 命令     │          │     ├ overlay 拉流 ──┼──► AVPlayer
│                     │          │                    │◄──overlay─│     └ 状态上报        │
└─────────────────────┘          └────────────────────┘  status   └─────────────────────┘
```

三条通道分工：

1. **命令通道（NATS）**：发送端把播放命令发到接收端的订阅主题，轻量、必达
2. **媒体通道（WireGuard overlay）**：电视拿到媒体 URL 后直接向发送端拉流，大流量不经过控制面
3. **状态通道（overlay HTTP）**：播放进度 / 状态周期性回传发送端

## 投屏生命周期

```
配对（一次性）        电视打开 Reflux → 扫码加入 Lattice 网络 → 获得 overlay IP
                     引擎身份持久化，之后系统自动保活重连

投放                  发送端选择电视 → 起本地媒体服务 → 发布 play 命令（含媒体 URL）
                     电视引擎收到命令 → 回前台拉起播放器 → 向发送端拉流播放

播放中               每 5s 状态上报（进度 / 缓冲 / 错误）
                     发送端可随时投下一部（覆盖式）或停止

退出                  电视按返回键退出播放器；发送端 App 退出时媒体服务随生命周期关闭
```

## 命令通道细节

每台接收端设备在控制面注册后有稳定身份（AppID），订阅主题为：

```
lattice.cast.<appid>.cmd
```

命令是 JSON 载荷：

```json
{ "id": "<uuid>", "action": "play", "url": "http://<发送端overlay-ip>:47823/<hash>/<file>", "title": "movie.mkv", "ts": 1717500000 }
```

- NATS 订阅在引擎内维护，断线重连后自动恢复订阅，**离线期间的命令不补投**（投屏是即时意图）
- URL 的路径首段（`<hash>`）同时是状态回传的 Bearer 凭据——拿到 URL 即拿到本次投放会话的凭据，无需额外握手

## 媒体数据通道

发送端在投放时起一个本地媒体服务（监听 overlay IP 的 47823 端口），支持 HTTP Range 请求（206 Partial Content）。电视端 AVPlayer 以标准 Range GET 拉流，随机访问（seek）天然支持。

传输路径由 Lattice 的连接管理决定：优先 ICE 直连（P2P 打洞），失败时回落到 FERRY 中继。对播放器而言完全透明。

## 状态回传

电视端周期性 POST 到媒体 URL 同主机的 `/__cast/status`：

```
POST http://<发送端overlay-ip>:47823/__cast/status
Authorization: Bearer <hash>
{"state": "playing", "position": 123.4, ...}
```

发送端以此驱动投屏面板上的进度条与状态展示。接收端崩溃或断网时，发送端靠上报超时感知。

## tvOS 上的引擎常驻设计

传统做法引擎跑在 App 进程里——tvOS 会在 App 退后台后挂起进程，投屏就断了。Lattice 的 tvOS 方案把引擎下沉到 `NEPacketTunnelProvider` 扩展进程：

- **系统保活**：隧道 profile 启用后，扩展进程由系统维持，App 退后台 / 被杀不影响接收投屏
- **零流量捕获**：隧道路由表为空（`includedRoutes = []`），不接管电视的任何普通流量，纯粹作为一个受系统保活的执行上下文
- **App ⇄ 扩展 IPC**：App 通过 `sendProviderMessage` 轮询引擎状态、下发打开媒体句柄等指令；投屏到达时 App 被拉起前台播放
- **配置真源是 profile**：引擎身份与服务器配置存于持久化的 VPN profile，系统重启后扩展重拉即自愈，无需 App 在场

## 调试工具：castcmd

仓库自带命令行投播工具（`cmd/castcmd`），不依赖 App 即可验证整条链路：

```bash
go run ./cmd/castcmd \
  -nats nats://<控制面地址>:4222 \
  -appid <电视的AppID> \
  -media "http://<发送端overlay-ip>:47823/<hash>/movie.mkv" \
  -title "测试影片"
```

| 参数 | 说明 |
|------|------|
| `-nats` | 控制面 NATS 地址（命令走此通道） |
| `-appid` / `-peerid` / `-pubkey` | 目标设备三种定位方式，优先级依次递减 |
| `-media` | 媒体 URL（必需） |
| `-transport` | `nats`（默认）或 `overlay`（直发 UDP 数据报到引擎命令口 47822，等 ACK） |

`-transport overlay` 用于验证 ICE 直连数据面是否健康，与 NATS 通道互为对照。

## 故障排查速查

| 现象 | 检查 |
|------|------|
| 发送端找不到电视 | 电视是否在线（控制面设备列表）；两端是否同一网络 |
| 命令已发但电视无反应 | 用 castcmd 复现；确认电视引擎的 NATS 连接在（控制面侧）；电视 App 是否完成配对（VPN profile 已允许） |
| 命令到了但不播 | 媒体 URL 是否可达（电视 → 发送端 overlay 连通性）；发送端媒体服务是否还在 |
| 能播但频繁卡顿 | 大概率走了中继（ICE 直连未建成），检查两端 NAT 类型；见 [ICE Connection](/design/ice-connection) |
| 电视重启后失联 | 引擎应自动重连重注册；检查 VPN profile 是否仍启用（设置 → VPN） |
