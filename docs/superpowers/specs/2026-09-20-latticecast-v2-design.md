# LatticeCast v2 设计——语音入口、常驻接收、转码兜底与生态扩展

> 日期: 2026-09-20
> 性质: 特性设计（feature spec）——按特性分组，每组实施前可再出细化 mini-spec；组内顺序即建议实施顺序
> 前置: v1 已交付（见 `2026-09-18-latticecast-design.md` v6 与 lattice-cast PR #1）——cast-agent（发现/解析/MCP/审计/内置大脑/网页聊天）、三平台渲染端骨架、Reflux source、PlayerKit 倍速修复
> 关联: `2026-09-17-latticedns-design.md`、`2026-09-11-personal-mode-and-ai-trust-layer-design.md`、reflux 仓（Jellyfin API / 转码）、PlayerKit 仓
> 状态: 待评审

## 修订记录

- **v1（2026-09-20）**: 初稿。整合 v1 期间遗留的方向性条目与实测发现（常驻接收生命周期、语音入口嵌 reflux App、转码兜底、LatticeDNS 别名、tool_spans 上报、媒体中转、树莓派渲染端、播放质量 follow-ups）。

## 一、v2 的定位与目标

**一句话**：把 LatticeCast 从"能用的投屏功能"做成"常驻的家庭投屏基础设施"——入口从打字升级为语音、接收器从"打开 App 才在"升级为"开机即在"、内容从"播得动才行"升级为"转码兜底全都播"，并把投屏历史与设备命名接回 lattice 主仓生态。

**v1 遗留的实测输入**（v2 设计的直接依据）：
- 接收器生命周期错配：reflux 播放器关闭 = 接收端死亡（用户实测提出，v1 以"请勿强退"缓解，v2 结构性解决）；
- PlayerKit 对 DTS/AC3 直通源的倍速 bug 已修，但暴露"渲染端解不动的格式仍无法兜底"（转码需求）；
- 中文片名搜索依赖 TMDB 元数据（本地英文文件名搜不到）——本地源挂入 reflux 统一管元数据；
- MP4 轻微卡顿（buffering 疑似）、passthrough 真机矩阵未测——播放质量 follow-ups。

## 二、特性分组与优先级

### 组 A：语音入口嵌 reflux App（v2 旗舰）

**用户旅程**：手机上打开 reflux App → 按住说话"把 115 里的蚁人投到客厅" → App 内语音转文字 → 走 `/chat` 同一通道 → 客厅播放，App 内同步可看进度。

**设计**：

```
reflux App（iOS/macOS，LatticeCastKit 宿主）
   │ 按住说话（按住录音、松手识别）
   ▼
ASR：iOS/macOS 用系统语音识别（SFSpeechRecognizer，中文支持好、免费、
     音频不出设备——隐私档天然成立）；识别失败降级为键盘输入
   ▼
POST /chat/api/message（与网页聊天同一接口、同一 Bearer、同一 brain）
   ▼
SSE 事件回流：delta/tool/final 全部在 App 内可视化（工具执行行、最终回复）
```

- **不做独立 ASR 服务**：系统识别器覆盖 v2 需求；ollama 隐私档用户如需本地 ASR 可选配 whisper.cpp（开放问题，见第九节）。
- **多语言**：跟随系统识别语言，默认 zh-CN。
- **macOS 侧**：同 App 内快捷键长按触发，与菜单栏常驻形态（组 B）共用状态。
- **非目标**：TTS 语音回复（v2 只回文字/卡片）；免唤醒词的全天候监听（v3）。

### 组 B：接收器生命周期分层（T23，常驻接收）

**原则（继承自 v1 spec"渲染端不入 mesh"同款决策风格）**：接收 = 系统级常驻；呈现 = 用户级交互。投片到达时经既定通道把播放任务交给播放器。

| 平台 | v2 形态 | 系统约束（如实） |
|---|---|---|
| macOS | reflux App 增加**菜单栏常驻形态**：关闭主窗口 = 最小化到菜单栏，菜单图标显示接收状态/房间名/正在播放；**登录自启**（SMAppService）。投片到达 → 自动激活主窗口弹出播放界面（v1 已实现该呈现路径） | 彻底守护进程形态留 v3 评估 |
| Android（TV/手机） | **前台服务 + 开机自启**（T13/14 设计即如此，落地时保持） | 常驻通知栏图标是系统要求，如实呈现 |
| iOS/iPad | **打开 App 才能接收**（系统不允许第三方永久后台监听） | 配对页如实标注；投到手机前先开 App |
| Apple TV | 同 iOS（前台才收）；专用电视可"开机自启进 App" | 同上 |

**与 reflux 播放器的关系**：接收器（常驻）与播放器（交互）**生命周期分离、品牌一体**。macOS 菜单栏形态是 reflux 播放器的"常驻分身"：关闭主窗口后仍在站岗，投片到达自动弹窗播放——这解决 v1 实测的"关 App = 变砖"。

### 组 C：转码兜底（解不动就转）

**机制**：cast-agent 侧的**适配降级**，协议零改动。

```
cast_play → 渲染端 /play → state=error（解码不支持）
   → cast-agent 捕获 error，若源来自 reflux：
     请求 reflux 转码流（/Videos/{id}/stream 的 transcode 参数，由 reflux 的
     实时转码能力承接）→ 重投 → 成功
   → 两次都失败：如实报错（含两次原因）
```

- 仅对 reflux source 启用（本地 NAS 文件的格式问题由 cast-agent 侧 ffmpeg 按需转——v2.1 评估）；
- 降级次数=1，避免循环；降级事实写审计（`downgraded_to_transcode: true`）；
- LLM 转述规则进 brain 系统提示："已自动转码，画质可能略降"。

### 组 D：LatticeDNS 设备命名（主仓小 PR）

- LatticeDNS 增加**别名记录类型**（v1 遗留项）：`bedroom-tv.lattice` → 网关 overlay IP（虚拟设备 → 网关）；
- cast-agent 启动时把配对设备注册进别名表（调控制面 API）；
- 价值：全 mesh 可用稳定名字寻址投屏目标；与子网路由（全屋入网）天然组合。

### 组 E：tool_spans 控制面上报（主仓小 PR）

- cast-agent 的本地 JSONL 审计**镜像上报**到 latticed 的 tool_spans 写入 API（主仓需提供该写入端点——v1 时主仓零代码，v2 补上）；
- Dashboard 可查投屏历史：谁、何时、投了什么、到哪台、成功与否；
- 断网降级：上报失败仅本地缓存重试（有界队列），不阻塞投屏。

### 组 F：媒体中转逃生通道

- 场景：人在外面，想把**随身设备上的文件**投到家里电视（渲染端够不着外部文件，v1 明确报错）；
- 机制：MCP 新工具 `cast_upload(device, filename, content)`——文件经 mesh 上传到网关暂存目录（限额如 2GB/24h 自动清理）→ 按 NAS 文件流程投屏 → 播放结束后可选清理；
- 安全：沿用 Bearer + Policy；暂存目录独立于媒体库，不参与 search_media 索引（避免污染检索）。

### 组 G：树莓派渲染端

- Go 渲染端（cmd/latticecast-renderer）的 Linux/arm64 构建目标 + Makefile 交叉编译 + 树莓派安装文档；
- 与 macOS 版同一份代码、同一套契约测试（T17 已保证）；
- 输出：音频走 ALSA/PulseAudio 择优（树莓派跑桌面环境 vs headless 两种说明）。

### 组 H：播放质量 follow-ups（PlayerKit 仓）

- MP4 流式轻微卡顿排查（buffering 策略/cacheSpeed）；
- passthrough 真机矩阵：HDMI/DP/多显示器音频路由下的播放+seek 泡水测试；
- 渲染端有界等待的参数裕量（10s 窗口 vs adapter 10s 超时的重合问题、eof 分支补 time-pos==0 守卫——投图片不误报）；
- 慢源 >10s 场景：考虑 adapter 超时给裕量或渲染端提前返回"loading"中间态。

### 组 I：小项打包

- Reflux source 透传上游错误体（让 LLM 能说"115 登录过期了"）；
- brain sessions 上限 + 淘汰（oldest eviction）；
- webchat 请求体 MaxBytesReader + 文本长度上限；
- search_media 融合去重（同片名 NAS/reflux 双条时合并展示或标注来源）；
- brain 工具描述与系统提示措辞对齐（url"真实网络直链"口径统一）。

## 三、架构增量总览

```
v1 已有：入口（MCP 客户端/网页聊天/内置大脑）→ cast-agent → 渲染端
v2 增量：
  入口侧：reflux App 语音（组A）——入口嵌自家 App，SFSpeechRecognizer 本地识别
  生命周期：接收器常驻分层（组B）——macOS 菜单栏/Android 前台服务/苹果系打开即收
  内容侧：转码兜底（组C）+ 媒体中转（组F）——"全都播得动"
  生态侧：LatticeDNS 命名（组D）+ tool_spans Dashboard（组E）+ 树莓派（组G）
  质量：PlayerKit follow-ups（组H）+ 小项（组I）
```

## 四、错误处理增量

| 故障 | v2 行为 |
|---|---|
| 渲染端解码不支持（reflux 源） | 自动转码重投一次，失败才报错并说明"已尝试转码" |
| ASR 识别失败/权限拒绝 | 提示降级为键盘输入，不中断会话 |
| tool_spans 上报失败 | 本地有界队列重试，投屏不受影响 |
| 上传超限/超时 | 明确报错（大小上限、剩余配额） |
| 115 登录过期 | **透传上游错误体**（组 I 已含）→ LLM 直接说"115 登录过期，去 reflux 重新扫码" |

## 五、安全与隐私增量

- 语音：默认系统级 ASR（音频不出设备）；如选云端 ASR 需用户显式开启（v2 默认不提供）；
- 菜单栏常驻：接收状态可见（房间/正在播放），退出常驻 = 停止接收（明确开关）；
- 上传暂存：独立目录、限额、自动清理、不入检索索引；
- tool_spans 上报沿用 Policy 与审计体系，不新增信任模型；
- 继承 v1 全部纪律（api_key 脱敏、receiver 诚实回报、prompt 反幻觉规则）。

## 六、测试策略增量

- 语音链路：ASR mock（注入文本）→ /chat → 渲染端，端到端断言；真机听写质量手动清单；
- 菜单栏常驻：关窗→投片→自动弹窗的 UI 自动化（macOS XCUITest 可选）+ 手动验收；
- 转码兜底：fake reflux 的 transcode 分支（首次 error → 转码重投成功）；真机"解不动格式"清单；
- 组 D/E：主仓 API 的契约测试（cast-agent 侧 mock 控制面）；
- 树莓派：交叉编译产物在真机的 smoke 清单；
- 回归：v1 全部测试保持绿（13 Go 包 + Swift 契约 + 真机清单）。

## 七、分期建议

- **v2.0**：组 B（macOS 常驻）+ 组 A（语音）——旗舰体验，解决两大 v1 痛点；
- **v2.1**：组 C（转码兜底）+ 组 I（小项打包）——"全都播得动"+ 清尾巴；
- **v2.2**：组 D（LatticeDNS 命名）+ 组 E（tool_spans）——生态回归（主仓两个小 PR）；
- **v2.3**：组 F（中转）+ 组 G（树莓派）+ 组 H（播放质量收尾）。
- 组间无硬依赖，可按实际需要调序；A/B 优先因为直接回应 v1 实测痛点。

## 八、非目标（v2 明确不做）

- TTS 语音回复（文字/卡片足够，v3 再议）；
- 免唤醒词全天候监听（v3 常驻麦克风）；
- 免 App 的 iOS/Apple TV 后台接收（系统不允许，非我们可控）；
- AirPlay 协议适配（接收端闭源 + 发送端逆向合规风险，评估后维持不做）；
- 多用户/多家庭；
- B 站等解析器（继续逐个评估，未列入 v2 主线）。

## 九、开放问题

1. macOS 常驻形态选型：菜单栏 App（交互丰富）vs 纯 LaunchAgent + 按需弹窗（更轻）——v2.0 实现时定，倾向菜单栏；
2. Android 前台服务的通知措辞与交互（随 T13/14 出）；
3. 转码触发是否前移（play 前探测渲染端能力，避免"先失败再转"的一次失败体验）——依赖渲染端能力上报协议，v2.1 评估；
4. whisper.cpp 本地 ASR 是否作为 ollama 隐私档的配套选项——v2.0 用户反馈后定；
5. 树莓派 headless（无桌面）场景的输出方案（ALSA 直出 vs 强制桌面）——v2.3 实现时定。

## 十、成功标准（v2 验收线）

1. 关掉 reflux 主窗口，电视/Mac 仍可被投屏（菜单栏显示接收中）；
2. 手机 reflux App 按住说话"把 115 里的 xx 投到客厅"→ 播放，全程无键盘；
3. 投一个"电视解不动"的格式 → 自动转码播出；
4. `bedroom-tv.lattice` 在任意 mesh peer 上可解析可投；
5. Dashboard 能查到今天每次投屏的完整历史；
6. 树莓派接电视成为第四块屏。
