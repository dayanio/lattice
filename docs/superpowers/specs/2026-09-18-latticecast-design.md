# LatticeCast——一句话智能投屏 设计

> 日期: 2026-09-18
> 性质: 特性设计（feature spec）——v1 为实现规范，v2/v3 为方向性规划，各自实施前可再出细化 mini-spec
> 关联: `2026-09-17-latticedns-design.md`、`2026-09-11-personal-mode-and-ai-trust-layer-design.md`、
>       `2026-05-09-ai-agent-isolation-design.md`、`docs/ai/mcp-server.md`
> 状态: 待评审

---

## 一、目标与定位

**一句话**：在 Lattice 生态上实现"说一句话，agent 把内容投到家里指定电视播放"的全自动智能投屏——"把这首歌投到卧室电视"→ 电视开始播放，无需打开任何 App 手动操作。

**用户旅程（v1 验收线）**：

1. 家里常开节点（NAS/Mac/树莓派）`docker run` 起 standalone 控制面，`lattice up` 入网（已有能力）；
2. 节点上跑起 `lattice-cast`，自动扫描发现家里的电视（DLNA / Chromecast），注册命名 `bedroom-tv.lattice`；
3. 用户在外面（或家里），对着任意 MCP 客户端说一句"把 NAS 里那部星际穿越投到卧室电视"；
4. LLM 调用 `list_cast_devices` → `search_media` → `cast_play`，经 mesh 下发指令，卧室电视开始播放；
5. 全程策略管控（default-deny 白名单放行）+ 每次调用落 tool_spans 审计。

**对 Lattice 的战略意义**：本特性是 LatticeDNS（设备命名）+ 个人模式（回家组网）+ MCP（自然语言→工具）+ AgentIdentity/Policy（零信任管控）的集大成演示——每项已有资产在链路中都有一席之地。

## 二、需求澄清结论（2026-09-18 会话）

| 问题 | 结论 |
|---|---|
| "打开 xx"的内容源 | 混合都要：NAS 媒体 + 在线视频 + 流媒体 App 内容 → 分层分期，v1 只做前两类 |
| 电视/播放设备 | 型号不确定 → **探测优先**：自动扫描发现设备支持什么协议，有什么用什么，不绑定品牌 |
| "说一句话"的入口 | 终态为语音硬件入口 → 渐进式落地，v1 先用文字/已有客户端验证全链路 |
| DLNA 实现 | **不自研协议栈，用现成开源库**，外面包统一 `CastAdapter` 接口隔离厂商兼容坑，便于打补丁/替换 |

**方案选型**：方案 A——`cast-agent` 常驻家里 + LLM 大脑外挂（可插拔：云端大模型默认，本地 ollama 隐私档）。否决：方案 B（全本地零云为独立架构，小模型意图理解不足）；方案 C（接入 Home Assistant/小爱生态，定制受制于人且偏离 lattice 自研叙事）。

## 三、总体架构

```
说话入口（分期：MCP 聊天客户端 → iOS 按住说话 → 常驻麦克风+唤醒词 → 语音硬件）
   │ 语音转文字（ASR）
   ▼
LLM 大脑（可插拔：GLM / Claude / 家里 ollama）
   │ MCP 工具调用（经 lattice mesh，AgentIdentity + JWT 鉴权）
   ▼
cast-agent（Go，家里常开节点，经家里 lattice agent 入网，普通 mesh peer）
   ├─ Discovery：SSDP 扫 DLNA MediaRenderer + mDNS 扫 Chromecast，按房间归位
   ├─ CastAdapter 统一接口 → 现成库实现（go-dlna / go-chromecast；AirPlay 二期）
   ├─ MediaResolver：媒体引用 → 电视可达的播放 URL
   ├─ MCP Server：cast_* 工具集
   └─ LatticeDNS 注册：bedroom-tv.lattice（网关别名记录）
   ▼
电视（主动拉流播放）
```

代码落位（现有 monorepo）：`cmd/lattice-cast/` + `internal/cast/{discovery,dlna,chromecast,resolve,mcp}`。

**配置**（YAML，单一来源）：NAS 媒体库路径列表、设备→房间映射、协议偏好顺序（多协议设备的降级次序）、MCP 监听地址。房间映射在设备首次被发现时自动生成待确认模板（设备友好名/UDN → 房间），人工确认一次后固化——"归位"永远以配置为准，不靠猜测。

### 组件职责边界

| 组件 | 职责 | 不负责 |
|---|---|---|
| Discovery | 设备发现、能力探测、房间归位、在线状态 | 协议指令下发 |
| CastAdapter | 单设备协议指令（play/pause/stop/volume/status） | 设备选择、媒体解析 |
| MediaResolver | 媒体引用 → 电视可达 URL；NAS 媒体库检索 | 投屏指令 |
| MCP Server | 工具鉴权、参数校验、审计落点 | 业务逻辑 |
| LLM 大脑 | 意图理解、设备匹配、多轮消歧 | 直接触碰电视 |

## 四、MCP 工具集（v1）

```jsonc
list_cast_devices() -> [{ name, room, lattice_name, protocols[], online, now_playing }]
search_media(query) -> [{ media_id, title, kind, source }]        // NAS 媒体库检索
cast_play(device, media_id?, url?, title?) -> { status, adapter }  // 二选一传参
cast_stop(device) -> { status }
cast_volume(device, level) -> { status }   // level: 0-100
cast_status(device) -> { now_playing, position_ms, state }
```

- `cast_play` 的 `media_id` 来自 `search_media`，`url` 为直链（MP4/HLS）或 YouTube 链接（内部经 yt-dlp 提流）。
- MCP over HTTP（streamable），复用现有 agent JWT 鉴权中间件模式；每次调用写 tool_spans。

## 五、关键数据流

以"把这首歌投到卧室电视"为例：

1. LLM 收到话术，调 `list_cast_devices()` 获取设备清单（名字/房间/协议/在线状态）；
2. 房间语义匹配："卧室电视" → `bedroom-tv.lattice`；命中多台 → 反问用户，**不猜**；
3. 调 `search_media("歌名")` 命中 NAS 文件，或直接使用用户给的链接；
4. MediaResolver 产出电视可达 URL（见第六节约束）；
5. 调 `cast_play` → CastAdapter 按设备协议下发（DLNA: SetAVTransportURI + Play；Chromecast: Load）；
6. 结果回话："已经在卧室电视上播放了"。每次工具调用落 tool_spans。

## 六、硬约束：谁来拉流

DLNA/Chromecast 的模型是**电视自己去拉媒体 URL**。因此 v1 约定：

- **可用内容**：家庭内网可达（NAS 文件）或公网可达（MP4/HLS 直链、YouTube 提流后的地址）；
- **不可用内容**：用户外部设备上的文件（如公司笔记本上的视频）——电视够不着 mesh，v1 明确报错并说明原因，**v2 增加"先中转到 NAS 暂存目录再投"的逃生通道**。

v1 内容范围：NAS 媒体库（本地目录扫描，文件名检索）+ MP4/HLS 直链 + YouTube（yt-dlp 提流）。
v2 起再啃：B 站等国内平台解析器（每平台一个、易失效、维护成本高，逐个加）；流媒体 App 操控仅 Android TV（adb）路径可行，排 v3。

## 七、错误处理

| 故障 | 行为 |
|---|---|
| 目标设备离线 | 明确回话"卧室电视不在线"，不静默失败 |
| DLNA SOAP 报错（厂商兼容坑高发区） | 设备若支持多协议，按配置的协议偏好顺序自动降级重试一次（如 DLNA 失败换 Chromecast） |
| 媒体 URL 电视不可达 | 回话说明原因（含第六节约束场景） |
| yt-dlp 提流失败/平台变更 | 回话"这个链接暂不支持投屏" |
| 意图歧义（多台设备/多个同名媒体） | 必须反问用户选择，不猜 |
| cast-agent 自身离线 | MCP 连接失败，LLM 回话"家里投屏服务不在线" |

## 八、安全与 Lattice 集成点

全部复用现有机制，不新增信任模型：

- **入网**：cast-agent 所在节点经家里 lattice agent 入网（已有）；外部 LLM 客户端经 mesh 访问 MCP 时走 AgentIdentity + 单次 Enrollment Token + agent JWT（已有）；
- **策略**：LatticePolicy default-deny，仅放行两类流量——
  - ingress：`role=voice-assistant` 的 agent 身份 → 网关节点 MCP 端口（TCP）；
  - egress：网关节点 → 电视投屏端口（DLNA 8200/49152+、Chromecast 8008/8009、SSDP 1900/UDP、mDNS 5353/UDP）；
- **命名**：LatticeDNS 注册别名记录（`bedroom-tv.lattice` → 网关 overlay IP）。电视本身不是 mesh peer，属于网关后的"虚拟设备"——需要 LatticeDNS 增加**别名记录类型**（小扩展，随本特性交付）；
- **审计**：每次 MCP 工具调用落 tool_spans（traceID/agentID/tool/status/durationMs，已有），投屏历史可查询。

这是消费级投屏方案（小爱、米家）不具备的企业级信任层，是本特性对 lattice 叙事的核心差异化。

## 九、测试策略

- **CI 端到端**：Go 实现一个**假 DLNA 渲染器**测试替身（SSDP 应答 + AVTransport SOAP 端点），跑完整"发现→投屏→状态"链路断言；
- **golden 报文**：SOAP 请求/响应 XML 用 golden 文件测试，防协议回归；
- **适配器一致性套件**：对 CastAdapter 接口定义一致性用例，任何新适配器（含未来自研 LatticeCast 渲染端）必须通过；
- **Resolver 单测**：NAS 路径映射、直链透传、YouTube 提流（mock yt-dlp 输出）；
- **真机清单**：真实电视手动兼容列表，v1 验收以用户自家电视跑通为线，其他厂商逐步积累。

## 十、分期规划

### v1（本次实现范围）
- `lattice-cast`：Discovery（SSDP+mDNS）、CastAdapter（DLNA + Chromecast，现成库）、MediaResolver（NAS 目录扫描 + 直链 + YouTube）、MCP 四件套 + search_media；
- LatticeDNS 别名记录类型；
- 策略模板（voice-assistant 角色的放行规则示例）；
- 文字入口：任意现有 MCP 客户端（Claude Desktop / Cursor 等）经 mesh 使用。
- **验收**：在外的手机上说一句"把 NAS 里的 xx 投到卧室电视"，电视播出来。

### v2（方向性）
- iOS App 按住说话（复用 `apple/` 客户端）+ ASR（云端 API 或网关 whisper.cpp）；
- 媒体中转逃生通道（外部文件 → NAS 暂存 → 投屏）；
- AirPlay 适配器；LatticeCast 自研渲染端（HDMI 盒子/树莓派，绕开电视厂商 DLNA 实现）。

### v3（方向性）
- 常驻麦克风 + 唤醒词（网关上跑 whisper + openWakeWord/Porcupine）；
- Android TV adb 操控流媒体 App；
- 专用语音硬件形态。

## 十一、非目标（全部阶段）

- 屏幕镜像（mirroring）——只做推流播放（casting）；
- iPhone 上闭源流媒体 App 的远程操控；
- 媒体转码（电视解不动的格式 v1 直接报错）；
- 多用户/多家庭隔离（单家庭单 workspace）；
- 自研 DLNA 协议栈（决策：用现成库，见第二节）。

## 十二、开放问题

1. v1 默认接入哪个云端 LLM（GLM / Claude / 可配置）？建议做成 profile 配置，默认云端、可切 ollama；
2. v2 ASR 供应商选型（云端 API vs 网关本地 whisper）——延迟、成本、隐私权衡，v2 前定；
3. yt-dlp 作为外部依赖的分发方式（动态调用系统二进制 vs 静态内嵌），v1 实现时定；
4. SSDP/mDNS 组播流量在 LatticePolicy 下的放行表达方式（现有策略引擎按 peer+端口建模，组播需确认表达力），v1 实现时验证。
