# lattice-copilot v1 设计 — 家庭生态统一自然语言入口

- 日期：2026-09-22
- 状态：已评审（brainstorming 会话收敛，两段设计均经用户确认）
- 目标仓库：新建 `lattice-copilot`（建议归 `alatticeio` 组织；Go，复用 cast 所用的 modelcontextprotocol/go-sdk）
- 关联决策：2026-09-22 生态变现会话（家庭线在家免费 + relay 付费卡口；本设计是家庭线的"脊柱"工程）

## 1. 背景与定位

lattice 生态现有：lattice（WG 组网 + agent 沙箱，open-core）、lattice-cast（一句话投屏：agent + 渲染器 + LLM brain）、lattice-shim（用户态网络栈库）、reflux（闭源影音库）。家庭线的变现策略是"在家免费、远程靠 relay 付费、以家庭套票打包"。

本设计的 Copilot 是家庭线的统一自然语言入口：用户对它说话，它编排 cast（投屏）、Home Assistant（灯光/窗帘/空调等设备）以及未来新增的家庭 App（相册、下载等）。定位是**脊柱**——每个新系统接入，都是往脊柱上插一个工具集，而不是给某个 App 叠功能。

## 2. 方案取舍

| 方案 | 结论 |
|---|---|
| A. 长在 lattice-cast brain 里 | 否。落地最快，但 cast 变成上帝应用，后续每个家庭 App 都要挤进 cast 或重复造脑 |
| B. 内嵌 + 预留抽取缝 | 否。快但边界易腐坏，抽取时机难拿捏 |
| **C. 独立服务（选定）** | 新建守护进程，纯 MCP 编排器。cast 与 HA 都只是它的工具集，cast 几乎零改动 |

选 C 的关键前提（已核实成立）：cast agent 已是 MCP server，HA 官方内置 MCP Server 集成——**两边都是现成工具源，Copilot 只需做 MCP 客户端编排**，独立化的边际成本很低。

## 3. 架构

```
手机/电视 Web Chat (:7801, 内嵌单页)
        │ HTTP + bearer token
  ┌─────▼──────────┐   MCP   ┌────────────────┐
  │ lattice-copilot│───────► │ cast agent :7800│  list/search/play/…
  │                │   MCP   └────────────────┘
  │  toolset mgr   │────────►┌────────────────┐
  │  orchestrator  │         │ HA MCP Server  │  HassTurnOn/…
  │  session store │         └────────────────┘
  └─────┬──────────┘
        │ OpenAI 兼容 API（自备 key / Ollama / 未来 relay 配额）
      LLM
```

三个组件：

1. **toolset manager**：管理多个 MCP 连接（连接、探活、工具目录合并、断线退避重连）。首批两个 toolset：cast、HA。新增系统 = 新增一条连接配置，核心代码不改。
2. **orchestrator**：LLM 对话循环 + 工具编排。把所有 toolset 的工具目录合并喂给模型，模型自主规划调用顺序，Copilot 负责执行、超时（单工具 10s）、中间结果回灌、进度流式输出到聊天 UI。
3. **session store**：SQLite 会话历史，支撑多轮指代（"再大声点"）。

**跨系统场景数据流**（"看电影模式"）：
用户一句 → 模型拿到 cast + HA 合并工具目录 → 规划：`search_media` → `cast_play`；`HassTurnOff(客厅灯)`；`HassCloseCover(窗帘)` → 顺序执行，每步进度流式显示 → 逐项汇总成败。

## 4. 工具集接口

### 4.1 cast（零改动接入）
`internal/cast/mcpserver/server.go` 已用官方 Go SDK 注册 8 个工具：`list_cast_devices`、`search_media`、`cast_play`、`cast_stop`、`cast_pause`、`cast_seek`、`cast_volume`、`cast_status`。Copilot 直接作为 MCP 客户端连接。唯一改动见 §5 鉴权（可选 bearer 中间件）。

### 4.2 Home Assistant（官方集成）
启用 HA 内置 "Model Context Protocol Server" 集成（Settings → Devices & Services → Add Integration），创建 long-lived access token，把 `ha_url + token` 填入 Copilot 配置。工具面为 `HassTurnOn/HassTurnOff` 等官方暴露集。不依赖任何第三方 HA 组件。

### 4.3 后续工具集（非本期）
photos、下载助手、reflux——各期按需插入。

## 5. 鉴权（三段，顺手补掉 cast 无鉴权旧账）

- **HA**：官方 long-lived access token，存 Copilot 配置。
- **Copilot → cast**：给 cast MCP 加**可选** bearer token 中间件，默认开启、首启自动生成、写进 cast 配置；copilot 侧对应填入。
- **用户 → Copilot**：同一 token 机制；web chat 首次访问输入一次，存 localStorage。

## 6. 行为设计

### 6.1 场景编排
v1 不做场景 DSL，靠模型工具链自主编排。若实测不稳，v1.1 增加"命名场景宏"（存 JSON、确定性回放、不经 LLM）作为兜底。

### 6.2 状态问答
读类工具（`list_cast_devices`、`cast_status`、HA 状态查询）由模型按需调用后作答："家里灯都关了吗""客厅在放什么"。

### 6.3 错误处理
- 每个 toolset 维持探活，聊天页顶部状态（cast ✅ / HA ❌）；单个 toolset 离线不阻塞其余问答。
- 工具报错回灌给 LLM 由其决定重试或降级（"投屏失败，但灯已关"）；不静默吞部分失败——多步汇总必须逐项列成败。
- 会话历史落 SQLite，toolset 断线指数退避重连；LLM 报错（限流/未配 key）在聊天中明确提示，会话不丢。

## 7. LLM provider 与付费钩子

配置 `llm.base_url / model / api_key`（OpenAI 兼容）。三种来源同一配置面：用户自备 key、本地 Ollama、relay 托管网关。**relay 模式本期只预留 provider 类型不实现**——它是家庭套票中"LLM token 配额"付费能力的落点（relay 是生态唯一付费卡口，见关联决策）。

## 8. 配置与部署

- 单 Go 二进制 + `go:embed` 内嵌 UI；NAS / 树莓派 / macOS 直跑；附 docker-compose 片段（对齐 reflux-deploy 风格）。
- YAML 配置（对齐 cast `config.example.yaml` 风格）：`cast_url+token`、`ha_url+token`、`llm.*`、监听地址（默认 :7801）。
- 首启：自动生成访问 token 打到日志；UI 内置 setup 清单（连 HA → 连 cast → 配 LLM），未配完显示引导态。

## 9. 测试策略

- **单元**：orchestrator 以 mock LLM + mock toolset 跑确定性工具调用脚本；配置解析、token 中间件单测。
- **契约**：对真实 cast MCP server 跑（沿用 cast 仓库 e2e 测试模式）。
- **HA**：录制/回放 fixture 做黄金样本；真实联调藏 env flag 后（CI 不依赖 HA 实例）。
- **E2E**：脚本化对话 → 断言工具调用序列（"看电影"必须触发 `search_media`+`cast_play`+`HassTurnOff`）。
- 质量门：`go test ./...` 全绿。

## 10. 里程碑

- **M1**：只接 cast——聊天投屏端到端（与现有 cast brain 能力打平）；鉴权就位其中两段（用户→Copilot、Copilot→cast），HA token 段随 M2 接入时生效。
- **M2**：接 HA——场景编排、状态问答、健康态展示。
- **M3**：setup 向导 UI、docker-compose、文档、（若需要）命名场景宏。

## 11. 非目标（v1）

语音入口、automation YAML 生成（二期）、场景 DSL、多家庭、移动推送、远程访问（v2 relay）、reflux/photos/下载工具集。

## 12. 未来扩展

relay LLM token 配额（付费）、命名场景宏、photos/下载/reflux 工具集即插即用、电视端复用 HTTP API、语音、HACS 反向集成（把 cast/reflux 暴露成 HA 实体，分发向，二期评估）。
