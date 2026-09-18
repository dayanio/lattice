# LatticeCast——一句话智能投屏 设计

> 日期: 2026-09-18（同日两版）
> 性质: 特性设计（feature spec）——v1 为实现规范，v2/v3 为方向性规划，各自实施前可再出细化 mini-spec
> 关联: `2026-09-17-latticedns-design.md`、`2026-09-11-personal-mode-and-ai-trust-layer-design.md`、
>       `2026-05-09-ai-agent-isolation-design.md`、`docs/ai/mcp-server.md`
> 状态: 待评审

## 修订记录

- **v1（2026-09-18）**: 初稿。主路径为 DLNA/Chromecast 适配（现成库），自研渲染端排 v2。
- **v2（2026-09-18）**: 评审决策反转主次——**自研 LatticeCast 渲染端（Android TV/盒子 APK + 自有协议）提前至 v1 主路径**，DLNA/Chromecast 降级为 v2 兜底。依据：Chromecast 接收端为闭源认证制、AirPlay 接收端闭源且协议逆向合规存疑、DLNA 是唯一开放接收端但厂商实现参差；**自建接收端是唯一"零适配统一一切"的路径**——两端都是自家软件，无需任何厂商认证（Google/Apple 能统一协议正因握着接收端，我们能干是因为两端都是自己的）。发送端结论不变：go-chromecast 等 MIT 库无许可障碍，仍用于 v2 兜底。
- **v3（2026-09-18）**: 增补定位说明——本特性为设备控制骨架（DeviceAdapter 模式）的**首例**，链路骨架可复用于其他设备品类；v1 咬死投屏一条线，不预抽通用框架。

---

## 一、目标与定位

**一句话**：在 Lattice 生态上实现"说一句话，agent 把内容投到家里指定电视播放"的全自动智能投屏——"把这首歌投到卧室电视"→ 电视开始播放，无需打开任何 App 手动操作。

**用户旅程（v1 验收线）**：

1. 家里常开节点（NAS/Mac/树莓派）`docker run` 起 standalone 控制面，`lattice up` 入网（已有能力）；
2. 电视/盒子上侧载安装 **LatticeCast 渲染端 APK**，开机自启，mDNS 自报家门；
3. 节点上跑起 `lattice-cast`，自动发现渲染端，确认房间命名（如"卧室电视"）；
4. 用户在外面（或家里），对着任意 MCP 客户端说一句"把 NAS 里那部星际穿越投到卧室电视"；
5. LLM 调用 `list_cast_devices` → `search_media` → `cast_play`，经 mesh 下发到 cast-agent，再经家庭局域网自有协议下发渲染端，电视开始播放；
6. 全程策略管控（default-deny 白名单放行）+ 审计（v1 本地审计日志）。

**对 Lattice 的战略意义**：本特性是 LatticeDNS（设备命名）+ 个人模式（回家组网）+ MCP（自然语言→工具）+ AgentIdentity/Policy（零信任管控）的集大成演示——每项已有资产在链路中都有一席之地；自研渲染端同时为未来 TV 端语音 UI、内容能力预留了自有阵地。

**定位：设备控制骨架的首例**。本特性的链路抽象后是所有远程设备控制的通用骨架：

```
意图（LLM）→ 身份（AgentIdentity）→ 网络（mesh + Policy）→ 协议（DeviceAdapter）→ 执行（设备）→ 回报（状态 + 审计）
```

投屏是 DeviceAdapter 模式的第一个实例；灯光、空调（红外网关）、摄像头画面、扫地机等品类可复用同一骨架，lattice 与消费 IoT 平台的差异在于 P2P 直连不过厂商云、策略管控加全量审计——"哪个 agent 被允许碰哪个设备"正是 AgentIdentity/Policy 的主场。两条边界约束：高危设备（门锁等）进入前必须先补"执行前人在环确认"的策略能力；**v1 咬死投屏一条线做完，不预抽通用框架**——待第二个品类（候选：摄像头画面查看）立项时，再基于两例共性沉淀 DeviceAdapter 公共层。

## 二、需求澄清结论与决策修订（2026-09-18）

| 问题 | 结论 |
|---|---|
| "打开 xx"的内容源 | 混合都要：NAS 媒体 + 在线视频 + 流媒体 App 内容 → 分层分期，v1 只做前两类 |
| 电视/播放设备 | 型号不确定 → **探测优先**：渲染端 mDNS 自报家门；设备有什么能力就用什么，不绑定品牌 |
| "说一句话"的入口 | 终态为语音硬件入口 → 渐进式落地，v1 先用文字/已有客户端验证全链路 |
| 投屏协议路径 | **自研渲染端为主（v1）**：Android TV/盒子装我们的 APK，自有协议零适配；DLNA/Chromecast（现成库包 CastAdapter）v2 兜底，覆盖装不了软件的电视 |

**方案选型**：方案 A——`cast-agent` 常驻家里 + LLM 大脑外挂（可插拔：云端大模型默认，本地 ollama 隐私档）。否决：方案 B（全本地零云为独立架构，小模型意图理解不足）；方案 C（接入 Home Assistant/小爱生态，定制受制于人且偏离 lattice 自研叙事）。渲染端主次反转的决策过程见顶部修订记录 v2 条目。

## 三、总体架构

```
说话入口（分期：MCP 聊天客户端 → iOS 按住说话 → 常驻麦克风+唤醒词 → 语音硬件）
   │ 语音转文字（ASR）
   ▼
LLM 大脑（可插拔：GLM / Claude / 家里 ollama）
   │ MCP 工具调用（经 lattice mesh，AgentIdentity + JWT 鉴权）
   ▼
cast-agent（Go，家里常开节点，经家里 lattice agent 入网，普通 mesh peer）
   ├─ Discovery：mDNS 发现 LatticeCast 渲染端（_latticecast._tcp），房间归位
   ├─ CastAdapter：LatticeCast 适配器（v1）｜DLNA / Chromecast 适配器（v2 兜底，现成库）
   ├─ MediaResolver：媒体引用 → 渲染端可达的播放 URL
   └─ MCP Server：cast_* 工具集
   ▼ 家庭局域网，自有协议（HTTP/JSON + Bearer Token）
LatticeCast 渲染端（Android TV / 盒子 APK：ExoPlayer 拉流播放 + 进度回报 + 极简 UI）
```

**关键简化**：渲染端**不入 mesh、不跑 WireGuard**——它只在家里局域网收 cast-agent 指令，远程访问的活儿全由 cast-agent（mesh peer）承担，APK 因此可以做得极轻。

**仓库策略（双仓库）**：全部实现放**新仓库** `lattice-cast`（`github.com/alatticeio/lattice-cast`）：

```
cmd/lattice-cast/            # cast-agent 入口
internal/cast/
  discovery/                 # mDNS 发现、房间归位
  adapter/latticecast/       # v1 自有协议适配器
  adapter/dlna|chromecast/   # v2 兜底（占位）
  resolve/                   # 媒体解析
  mcp/                       # MCP 工具集
android/                     # 渲染端 APK（Kotlin + ExoPlayer；借鉴主仓库 apple/ 单仓库多端先例）
docs/protocol.md             # LatticeCast 协议唯一权威定义
```

理由：依赖画像不同（yt-dlp、ExoPlayer 等消费级依赖，不进核心 Go 代码树）、发布节奏独立（渲染端真机兼容列表持续滚动）、不稀释 lattice 核心的"网络编排 + agent 沙箱"叙事。cast-agent 对 lattice 的依赖仅限控制面 API 与 agent JWT 校验模式。**lattice 主仓库 v1 零代码改动**（仅策略模板文档，见第九节）。

**配置**（YAML，单一来源）：NAS 媒体库路径列表、设备→房间映射、多协议设备时的适配器偏好顺序（v2 起生效）、MCP 监听地址、渲染端 Bearer Token。房间映射在渲染端首次被发现时自动生成待确认模板（mDNS name → 房间），人工确认一次后固化——"归位"永远以配置为准，不靠猜测。

### 组件职责边界

| 组件 | 职责 | 不负责 |
|---|---|---|
| Discovery | 渲染端发现（mDNS）、房间归位、在线状态 | 协议指令下发 |
| CastAdapter | 单设备协议指令（play/pause/stop/seek/status） | 设备选择、媒体解析 |
| MediaResolver | 媒体引用 → 渲染端可达 URL；NAS 媒体库检索 | 投屏指令 |
| MCP Server | 工具鉴权、参数校验、审计落点 | 业务逻辑 |
| LatticeCast 渲染端（APK） | 拉流播放、进度回报、极简 UI（播放中标题/封面） | 意图理解、媒体解析 |

## 四、LatticeCast 协议（v1）

```
传输:   HTTP/JSON over 家庭局域网
鉴权:   Bearer Token（cast-agent 配置持有，APK 首次运行录入）
发现:   渲染端 mDNS 发布 _latticecast._tcp，TXT 携带 name / room / protocol version

POST /play    { url, title?, position_ms? } -> { ok, state }
POST /pause   -> { ok, state }
POST /stop    -> { ok, state }
POST /seek    { position_ms }               -> { ok, state }
POST /volume  { level }                     -> { ok, state }   // level: 0-100
GET  /status  -> { state: playing|paused|idle|error, position_ms, duration_ms, title?, error? }
```

设计原则：

- **命令式而非会话式**——无连接状态，cast-agent 重启即恢复控制；
- **进度由渲染端维护**，cast-agent 轮询 `GET /status`（v1 不做推送；这是 DLNA 的 GENA 事件订阅省掉的整块脏活，自家协议顺手就有）；
- `docs/protocol.md` 是唯一权威定义，Go 与 Kotlin 各自实现，共用同一套契约测试用例（JSON 夹具）保证两端不漂移。

## 五、MCP 工具集（v1）

```jsonc
list_cast_devices() -> [{ name, room, protocols[], online, now_playing }]
search_media(query) -> [{ media_id, title, kind, source }]        // NAS 媒体库检索
cast_play(device, media_id?, url?, title?) -> { status, adapter }  // 二选一传参
cast_stop(device) -> { status }
cast_volume(device, level) -> { status }   // level: 0-100
cast_status(device) -> { now_playing, position_ms, state }        // 协议自带，免费获得
```

- `cast_play` 的 `media_id` 来自 `search_media`，`url` 为直链（MP4/HLS）或 YouTube 链接（内部经 yt-dlp 提流）；
- MCP over HTTP（streamable），复用现有 agent JWT 鉴权中间件模式；
- v1 审计：cast-agent 本地结构化审计日志（JSONL：时间/agentID/工具/参数摘要/结果）；v2 接入控制面 tool_spans。

## 六、关键数据流

以"把这首歌投到卧室电视"为例：

1. LLM 收到话术，调 `list_cast_devices()` 获取设备清单（名字/房间/协议/在线状态）；
2. 房间语义匹配："卧室电视" → cast-agent 设备表中的对应渲染端；命中多台 → 反问用户，**不猜**；
3. 调 `search_media("歌名")` 命中 NAS 文件，或直接使用用户给的链接；
4. MediaResolver 产出渲染端可达 URL（见第七节约束）；
5. 调 `cast_play` → LatticeCast 适配器向渲染端 `POST /play`；
6. 结果回话："已经在卧室电视上播放了"；用户追问"播到哪了"→ `cast_status` 轮询作答；
7. 每次工具调用落本地审计日志。

## 七、硬约束：谁来拉流

与 DLNA/Cast 相同，**渲染端自己去拉媒体 URL**（ExoPlayer 主动拉流）。因此 v1 约定：

- **可用内容**：家庭内网可达（NAS 文件）或公网可达（MP4/HLS 直链、YouTube 提流后的地址）；
- **不可用内容**：用户外部设备上的文件（如公司笔记本上的视频）——渲染端够不着 mesh，v1 明确报错并说明原因，**v2 增加"先中转到 NAS 暂存目录再投"的逃生通道**。

v1 内容范围：NAS 媒体库（本地目录扫描，文件名检索）+ MP4/HLS 直链 + YouTube（yt-dlp 提流）。
v2 起再啃：B 站等国内平台解析器（每平台一个、易失效，逐个加）；流媒体 App 操控仅 Android TV（adb）路径可行，排 v3。

## 八、错误处理

| 故障 | 行为 |
|---|---|
| 渲染端离线（mDNS 消失 / status 超时） | 明确回话"卧室电视不在线"，不静默失败 |
| 渲染端播放失败（解码不支持、URL 404） | `/play` 返回 `state=error` + 原因，透传回话（自有协议下这是结构化错误，不再有 DLNA 的 SOAP 兼容坑） |
| 媒体 URL 渲染端不可达 | 回话说明原因（含第七节约束场景） |
| yt-dlp 提流失败/平台变更 | 回话"这个链接暂不支持投屏" |
| 意图歧义（多台设备/多个同名媒体） | 必须反问用户选择，不猜 |
| cast-agent 自身离线 | MCP 连接失败，LLM 回话"家里投屏服务不在线" |
| APK 侧载受阻（个别电视系统限制） | 逃生通道：v2 的 DLNA 兜底适配器可提前拉入 v1（CastAdapter 接口已就绪，插入即可用） |

## 九、安全与 Lattice 集成点

全部复用现有机制，不新增信任模型：

- **入网**：cast-agent 所在节点经家里 lattice agent 入网（已有）；外部 LLM 客户端经 mesh 访问 MCP 时走 AgentIdentity + 单次 Enrollment Token + agent JWT（已有）；渲染端仅家庭局域网 + Bearer Token，**不入 mesh**（攻击面与配置复杂度同时收敛）；
- **策略**：LatticePolicy default-deny，v1 仅需放行一类流量——`role=voice-assistant` 的 agent 身份 → 网关节点 MCP 端口（TCP）。渲染端在家局域网内，不涉 mesh 策略（v1 主仓库**零代码改动**，交付策略模板文档）；
- **命名**：v1 设备命名由 cast-agent 设备表承担（房间名即身份）；LatticeDNS 别名记录（`bedroom-tv.lattice` → 网关 overlay IP）**移至 v2 可选**——那是叙事层面的锦上添花，不是 v1 链路的必需件；
- **审计**：v1 本地 JSONL 审计日志；v2 提供控制面写入 API 后接入 tool_spans（traceID/agentID/tool/status/durationMs），投屏历史可在 Dashboard 查询。

这是消费级投屏方案（小爱、米家）不具备的企业级信任层，是本特性对 lattice 叙事的核心差异化。

## 十、测试策略

- **协议契约测试**：`docs/protocol.md` 派生一套 JSON 契约用例，Go 适配器与 Kotlin 渲染端共用，防两端漂移；
- **CI 端到端**：Go 实现**假渲染端**（mock HTTP server——比假 DLNA 渲染器简单一个数量级），跑完整"发现→投屏→进度→停止"链路断言；
- **Resolver 单测**：NAS 路径映射、直链透传、YouTube 提流（mock yt-dlp 输出）；
- **APK 测试**：ExoPlayer 播放链路 instrumented 用例（可选）+ 真机手动清单；v1 验收以用户自家电视/盒子跑通为线；
- **v2 兜底适配器引入时**：补假 DLNA 渲染器测试替身 + golden SOAP 报文测试（届时才需要）。

## 十一、分期规划

### v1（本次实现范围）
- **`lattice-cast` 仓库**：LatticeCast 协议定义 + cast-agent（Discovery/CastAdapter-LatticeCast/MediaResolver/MCP 工具集/本地审计）+ `android/` 渲染端 APK（ExoPlayer、mDNS 自报、开机自启、极简播放 UI）；
- **lattice 主仓库**：零代码，仅 voice-assistant 策略模板文档；
- 文字入口：任意现有 MCP 客户端（Claude Desktop / Cursor 等）经 mesh 使用。
- **验收**：在外的手机上说一句"把 NAS 里的 xx 投到卧室电视"，电视播出来；追问"播到哪了"能答上进度。

### v2（方向性）
- DLNA / Chromecast 兜底适配器（现成库，覆盖装不了 APK 的电视）；AirPlay 合规评估；
- iOS App 按住说话（复用 `apple/` 客户端）+ ASR（云端 API 或网关 whisper.cpp）；
- 媒体中转逃生通道（外部文件 → NAS 暂存 → 投屏）；
- LatticeDNS 别名记录类型（主仓库小 PR）+ tool_spans 控制面上报（主仓库写入 API）；
- 树莓派/旧电脑渲染端（Go + mpv，与 APK 共享协议与契约测试）。

### v3（方向性）
- 常驻麦克风 + 唤醒词（网关上跑 whisper + openWakeWord/Porcupine）；
- Android TV adb 操控流媒体 App；
- 渲染端 TV 端语音 UI、专用语音硬件形态。

## 十二、非目标（全部阶段）

- 屏幕镜像（mirroring）——只做推流播放（casting）；
- iPhone 上闭源流媒体 App 的远程操控；
- 媒体转码（渲染端解不动的格式 v1 直接报错）；
- 多用户/多家庭隔离（单家庭单 workspace）；
- 自研 DLNA 协议栈（兜底仍用现成库，见修订记录）；
- 渲染端跑完整 lattice agent / 入 mesh（保持 APK 轻量）。

## 十三、开放问题

1. v1 默认接入哪个云端 LLM（GLM / Claude / 可配置）？建议做成 profile 配置，默认云端、可切 ollama；
2. yt-dlp 作为外部依赖的分发方式（动态调用系统二进制 vs 静态内嵌），v1 实现时定；
3. 渲染端首次配对的 Bearer Token 录入体验：手动输入 vs 二维码（TV 显示码/手机扫），v1 实现时定；
4. **APK 侧载在目标电视/盒子上的实际可行性**——实现的第一件事就验证它；若受阻，提前把 v2 的 DLNA 兜底适配器拉入 v1；
5. YouTube 提流的家庭网络可达性（视部署环境，不行则该功能点降级）。
