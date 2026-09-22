# Lattice Toolset Protocol (LTP) — 生态集大成架构设计

- 日期：2026-09-22
- 状态：草案，待评审
- 定位：lattice 生态"万物插槽 + 会说话的编排者"总体架构；定义协议层（LTP），并规定插件从实现到集成的完整路径
- 关联文档：[lattice-copilot v1 设计](2026-09-22-lattice-copilot-design.md)（编排层详细设计）、lattice-cast `docs/protocol.md`（渲染协议，未来归入 LTP renderer 剖面）、2026-09-22 生态变现会话（relay 付费卡口策略）

## 1. 愿景与一句话架构

**用户价值主张**：家里的内容、屏幕、设备、应用，一句话全可控，数据不出户。

**架构一句话**：一个基于 MCP 的开放工具集协议（LTP）把各领域能力标准化为可插拔的 toolset，一个 LLM 编排者（Copilot）把工具调用自然语言化，各域由最权威的实现去干（cast 管播放、HA 管设备、社区插件管内容源与应用意图）。

**不是什么**：不是把 lattice-cast 做成控制一切的上帝应用。cast 是"播放域"的权威实现；"控制一切"的是协议 + 编排层。设备驱动层不重建——借 HA 之手。

## 2. 总体架构

```
                          用户入口
        Web Chat(:7801) / 未来:电视端·语音·Home控制台
                              │  HTTP + bearer
        ┌─────────────────────▼──────────────────────┐
        │        编排层  lattice-copilot              │
        │  toolset 目录聚合 · LLM 编排 · 宏兜底 · 会话  │
        └──┬──────────────┬──────────────┬───────────┘
           │ LTP          │ LTP          │ LTP
     ┌─────▼─────┐  ┌─────▼─────┐  ┌────▼──────────┐
     │ cast agent │  │ HA MCP    │  │ 社区插件(容器)  │
     │ 播放+渲染域 │  │ 设备域桥接 │  │ 内容源·应用意图 │
     └─────┬─────┘  └───────────┘  └───────────────┘
           │
     ┌─────▼──────┐
     │ 渲染器群     │  Android TV / tvOS / 树莓派 / Web / 未来社区渲染器
     └────────────┘
```

关键性质：

1. **协议层与编排层解耦**：Copilot 挂了，cast 仍可独立投屏；插件不依赖 Copilot 存在。
2. **双消费者**：toolset 既被 Copilot（对话编排）消费，也被 cast agent 的 resolver（确定性流程，如"投屏"不走 LLM）直接挂载。两类消费者共享同一套 toolset 运行时。
3. **运行时可嵌入**：toolset manager/registry/manifest 解析做成 Go 库（沿 lattice-shim 的先例：核心能力抽成零依赖库），cast agent 与 copilot 各自嵌入，不引入进程间强耦合。

## 3. 协议层：LTP v0.1

### 3.1 基座：MCP，不发明新协议

一个 toolset = 一个 MCP server。两种运行形态：

- **dev 形态**：stdio 子进程，本地开发、调试、内置插件用。
- **分发形态**：HTTP(SSE) + 容器镜像。镜像里声明网络与目录权限，用户显式安装。

### 3.2 Manifest（协议核心，v0.1 刻意最小化）

```yaml
ltp: 0.1
name: source-115            # 命名空间: source- / render- / device- / intent- / infra-
version: 1.2.0
profile: media-source       # 能力剖面，见 3.3
capabilities: [search, stream, save, delete, download-status]
runtime:                    # dev=stdio 命令；分发=oci 镜像
  image: ghcr.io/community/source-115:1.2.0
  network: [wan]            # 声明所需出网
  volumes: []               # 声明所需挂载
config_schema:              # JSON Schema 子集，UI 据此自动渲染设置页
  type: object
  properties:
    cookie: { type: string, format: password }
config: ~/.lattice/toolsets/source-115/config.yaml
auth: bearer                # toolset 侧接受的可选鉴权
```

原则：**manifest 声明的就是它要的一切**——能力、权限、配置、运行时。用户安装时看到的是这份声明，不是黑盒。

### 3.3 能力剖面（capability profiles）

协议核只管 manifest/生命周期/鉴权/健康；领域能力由剖面定义：

| 剖面 | 归属 | 状态 |
|---|---|---|
| `media-source` 内容源 | LTP 本期定义（见 §5） | v0.1 |
| `renderer` 渲染端 | 已存在于 cast `docs/protocol.md`，后续重表述为 LTP 剖面 | v0.2 归并 |
| `device-bridge` 设备 | 首个实现 = HA MCP 桥接（不新建设备协议） | v0.2 |
| `intent` 应用意图 | 爱优腾 deep link 等"拿指令不拿流"扩展 | v0.2，依赖协议 v2 |
| `infra` 基础设施 | qBittorrent/下载器/NAS 服务等 | v0.2 |

版本策略：协议核只做**加法演进**（新字段可忽略、新剖面可并存）；剖面独立版本化，消费者声明兼容区间。

### 3.4 生命周期与注册

- **注册表 v0.1 = 一个 git 仓库 + JSON 索引**（名称、manifest 摘要、镜像地址、维护者、声明标签）。不做市场站点。
- 生命周期：`install`（拉镜像→渲染 config_schema 表单→健康检查）→ `enable`（进消费者目录）→ `upgrade/remove`。
- **内置插件走狗粮路线**：cast 现有四个解析源（NAS 库、mediaserver、reflux、ytdlp）重构为内置 toolset，用实现验证协议充分性。

### 3.5 信任与权限模型（分期）

- **v0.1（协议内建）**：manifest 声明 + 用户显式安装 + 容器隔离（网络/卷白名单）+ toolset 间无互相发现。
- **v0.2（lattice 加持，护城河）**：每个 toolset 发放 lattice AgentIdentity，经策略引擎按声明能力做调用级放行，全程审计——"能控制一切的东西必须是零信任的"，直接复用企业线资产。签名与供应链校验同批引入。
- **红线**：协议中立，插件自担。任何灰色集成（逆向 API 类）只存在于社区仓库，核心产品（lattice/cast/copilot/reflux）永不内置、注册表索引可发现但标注"第三方·自担风险"。

## 4. 编排层与消费者

- **Copilot**（详见 copilot spec）：聚合所有已启用 toolset 的工具目录 → LLM 编排；权限敏感操作首次使用时向用户确认；LLM 不稳的场景用命名宏确定性回放。
- **cast agent**：media-source 剖面的 toolset 直接挂入 resolver，投屏等确定性流程不经过 LLM。
- **健康与降级**：toolset 探活独立，单个离线不影响其他域；UI 显示各 toolset 状态。

## 5. media-source 剖面 v0.1（第一个剖面）

**公共数据类型**（JSON）：

```
MediaItem  { id, title, year?, type: movie|series|episode|audio, poster?, source }
Playable   { kind: url|deep_link, url?, expires_at?, container?, note? }
SaveTask   { task_id, status: queued|running|done|failed, progress?, error? }
```

**工具面**（按 capabilities 选实现，search/stream 必选）：

| 工具 | 能力位 | 语义 |
|---|---|---|
| `search(query, type?) → [MediaItem]` | search | 按标题检索；id 必须全局可回放 |
| `resolve(id) → Playable` | stream | 取可播地址；临时 url 必须带 expires_at |
| `save(id, dest?) → SaveTask` | save | 转存/下载到用户库 |
| `delete(id) → ack` | delete | 删除（须二次确认由消费者实现） |
| `download_status(task_id) → SaveTask` | download-status | 任务进度 |

约定：id 格式 `<plugin>:<native_id>`（如 `115:302194`），与 reflux 现有 `reflux:` 前缀规则一致；工具错误必须结构化（`code, message, retriable`），供 LLM 降级判断。

## 6. 插件开发者指南：实现什么、怎么集成

### 6.1 实现什么：两条作者路径

**原则：基础的东西全部协议化（SDK 吸收），作者只对接目标系统的 API。** MCP 是线缆协议，不是作者要学的框架。

**路径 A：SDK 快车道（面向 95% 社区作者）**——实现一个接口，其余全部由 SDK 托管：

```go
// Go：一个 115 插件的全部业务代码
type S115 struct{ cfg Config }

func (p *S115) Search(ctx, query string, o ltp.SearchOpts) ([]ltp.MediaItem, error) { ...调 115 API... }
func (p *S115) Resolve(ctx, id string) (ltp.Playable, error)                        { ... }
func (p *S115) Save(ctx, id string, dest string) (ltp.SaveTask, error)              { ... }

func main() {
    ltp.ServeMediaSource(ltp.Manifest{ Name: "source-115", Capabilities: [...] }, &S115{})
}
```

```python
# Python：等价形态（115/逆向社区主力语言，与 Go 同批提供）
from ltp import MediaSource, serve

class S115(MediaSource):
    capabilities = ["search", "stream", "save"]
    def search(self, query, type=None): ...   # 调 115 API
    def resolve(self, id): ...
    def save(self, id, dest=None): ...

serve(S115)
```

SDK 托管（作者零接触）：MCP server 循环与工具注册、manifest 生成、config.yaml 加载与 schema 校验、**设置页 schema 从结构体标签自动生成**（作者不实现任何控制台/表单）、结构化错误映射（作者只返回普通 error）、健康探针、save 任务进度跟踪、本地契约测试（`ltp plugin verify`）、容器打包（`ltp plugin build` 出镜像）。

**路径 B：裸 MCP（面向 cast-agent 级作者）**——自行实现 MCP server + 自备 manifest，直接进注册表。协议不因 SDK 存在而改变，SDK 是纯语法糖；能独立做出 cast agent 的人走这条路毫无额外成本。

两者共同的其余义务：capabilities 按 115 真实能力声明；配置密钥只读 `config.yaml`（用户自己填），不内置凭据。

### 6.2 怎么集成进来

- **开发期**：消费者配置里加一行 stdio 条目 → cast/copilot 启动时拉起 → 工具立即出现在 LLM 目录与 cast resolver 源列表。
- **分发期**：插件 PR 进注册表仓库 → CI 做契约测试（§6.3）→ 合入索引 → 用户在设置页点安装 → 渲染 config_schema 表单 → 填凭据 → 健康检查通过 → 自动出现在两个消费者的目录里。全程无需改动 cast/copilot 任何代码。

### 6.3 质量门槛

- **契约测试套件**（协议仓库提供）：跑真实例断言剖面工具的请求/响应形状、错误结构、expires_at 语义；注册表 CI 强制。
- 文档即门槛：README 必须含配置说明与权限声明。

### 6.4 一次安装的完整生命周期（实例）

插件是独立跑在用户 NAS 上的小程序（容器）；"安装"= 拉起它 + 在消费者清单登记连接方式。核心软件不改一字节（类比 VSCode 扩展）。

用户侧（装 115 插件）：设置页"添加插件"（列表来自注册表 index.json）→ 选中 → 后台 `docker pull` 镜像 → 按 manifest 的 config_schema 渲染设置表单（粘贴 115 cookie）→ 启动容器并做 MCP 握手 + 试探 search 的健康检查 → 写入本地清单 → 插件工具自动同时出现在 Copilot 工具目录与 cast resolver 源列表。卸载 = 停容器删目录。

安装的落点是消费者清单（UI 生成）：

```yaml
# ~/.lattice/copilot.yaml
toolsets:
  - name: cast            # 内置，播投屏
    url: http://nas:7800/mcp
  - name: ha              # 内置桥接，管设备
    url: http://ha:8123/mcp
    token: eyJhbGci...
  - name: source-115      # 社区插件（容器形态，点安装后生成）
    image: ghcr.io/xxx/source-115:1.2.0
    config: ~/.lattice/toolsets/source-115/config.yaml
  - name: source-webdav   # 开发者调试形态（stdio，清单加一行即"接入"）
    cmd: ["python3", "~/dev/webdav-plugin/main.py"]
```

开发者侧：模板仓库实现工具+manifest → 本地 stdio 调通 → Dockerfile 推镜像到自己的 ghcr → 注册表仓库提 PR 改 index.json → CI 契约测试通过即合并上架，全量用户可见。

容器即信任边界实体：manifest 声明所需网络/目录，安装时对用户可见，运行时被框死；dev 期 stdio 无需容器。

## 7. 落地顺序

1. 本协议 spec 评审冻结 v0.1 核 + media-source 剖面
2. `lattice-toolset` Go 库（manager/registry/manifest，嵌入消费者）
3. **插件 SDK（Go + Python 同批）与 `ltp` CLI（scaffold / build / verify）**——薄封装官方 MCP SDK + LTP 约定，不重造 MCP 轮子
4. cast 四源狗粮重构 + WebDAV 参考插件（两者都基于 SDK 写，同时狗粮协议与 SDK；WebDAV 保持模板级质量）
5. Copilot M2 接入（copilot spec 的 toolset manager 即 LTP 消费者）
6. 注册表仓库 + 契约测试 CI + 教程（"30 分钟写一个源插件"）
7. 社区发布：恩山/B站/V2EX，"一句话保存到 115"演示物料，首期悬赏 2–3 个社区插件

## 8. 非目标（v0.1）

插件市场站点、签名分发、跨设备插件同步、renderer/device/intent 剖面定稿（v0.2）、灰度发布机制。
