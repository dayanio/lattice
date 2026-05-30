# 115 媒体服务器设计文档

**日期**：2026-05-30  
**状态**：草稿  
**作者**：francis

---

## 概述

一个跑在家里机器上的 Go 服务，对 Infuse 等媒体播放器伪装成 Jellyfin 服务器，后端实际读取 115 网盘文件。通过 Lattice overlay 网络支持外出远程访问，无需端口映射。

**目标用户**：个人使用，影音发烧友  
**核心价值**：用 Infuse 的海报墙体验，播放 115 网盘里的视频，流量不经过本机服务器

---

## 架构

```
Apple TV (Infuse Pro)
       │  Jellyfin API
       │  (局域网 或 Lattice mesh 远程)
       ▼
┌──────────────────────────────────┐
│  115media（Go 服务，家里的机器）   │
│                                  │
│  ┌──────────────────────────┐    │
│  │  Jellyfin API 层 (Gin)   │    │  ← Infuse 只认这一层
│  ├──────────────────────────┤    │
│  │  媒体库 (SQLite + GORM)   │    │  ← 元数据、海报、进度缓存
│  ├──────────────────────────┤    │
│  │  115 客户端               │    │  ← 封装 SheltonZhu/115driver
│  └──────────────────────────┘    │
│                                  │
│  ┌──────────────────────────┐    │
│  │  Web 管理界面 (Vue 3)     │    │  ← 扫码登录、触发扫描
│  └──────────────────────────┘    │
└──────────────────────────────────┘
       │  302 重定向
       ▼
  115 CDN  ←──── Infuse 直接拉流（零本机带宽占用）
```

**Lattice 的角色**：家里的机器运行 Lattice agent，Apple TV 也在同一个 Lattice mesh 中，外出时 Infuse 通过 Lattice 分配的 IP 访问服务，无需配置端口映射或公网 IP。

---

## 核心模块

### 1. 115 客户端

**依赖**：`github.com/SheltonZhu/115driver`（AList 使用的同一个库，已处理指纹、签名、User-Agent）

**职责**：
- 二维码登录：`QRCodeStart` → 轮询 `QRCodeStatus` → `QRCodeLoginWithApp`
- 文件目录遍历
- 获取下载链接（返回时效性 CDN URL）
- Credential 持久化到 SQLite

**限频策略**：
- 请求队列，最大并发 2
- 失败后指数退避（1s → 2s → 4s，最多 3 次）
- 目录列表缓存 TTL 1 小时，减少对 115 API 的调用频次

**Credential 存储**：SQLite `auth_sessions` 表，服务启动时加载到内存，收到 115 返回 401 时触发重新扫码流程。

### 2. 媒体库

**存储**：SQLite，GORM 管理

**表结构**（简化）：

| 表 | 内容 |
|----|------|
| `media_items` | 文件 ID、115 路径、标题、年份、类型（电影/剧集）、TMDB ID |
| `metadata` | 海报 URL、简介、评分、导演、演员 |
| `watch_progress` | 用户 ID、媒体 ID、已播放秒数、是否看完 |
| `poster_cache` | 海报图片本地缓存路径 |

**扫描流程**：
1. 遍历用户配置的 115 目录
2. 按文件名解析影片信息（使用 `anitogo` 或正则提取标题 + 年份）
3. 调 TMDB API 匹配元数据（中文优先）
4. 下载海报到本地 `data/cache/posters/`
5. 增量扫描：对比文件 ID，只处理新增文件

**TMDB**：使用免费 API，中文元数据，支持电影和剧集两种类型。

### 3. Jellyfin API 层

只实现 Infuse 实际需要的最小接口子集：

| 方法 | 路径 | 作用 |
|------|------|------|
| POST | `/Users/AuthenticateByName` | 登录，返回 Token |
| GET | `/Users/{userId}/Items` | 媒体列表（支持分页、过滤） |
| GET | `/Items/{itemId}` | 单个媒体详情 |
| GET | `/Items/{itemId}/Images/Primary` | 海报图片 |
| GET | `/Items/{itemId}/Images/Backdrop` | 背景图片 |
| GET | `/Videos/{itemId}/stream` | 播放（302 → 115 CDN URL） |
| GET | `/Items/{itemId}/PlaybackInfo` | 播放前查询（Infuse 必须） |
| POST | `/Sessions/Playing/Progress` | 上报播放进度 |
| POST | `/Sessions/Playing/Stopped` | 上报播放结束 |

播放链接处理：调 `115driver` 的下载接口拿到时效 CDN URL（约 1 小时有效），直接 302 返回给 Infuse，视频字节不经过本机。

### 4. Web 管理界面

Vue 3 + Tailwind，轻量，仅用于配置和监控：

- **登录页**：显示二维码，扫码后自动跳转
- **首页**：媒体库统计（电影数、剧集数、最近扫描时间）
- **目录配置**：添加/删除要扫描的 115 目录路径
- **扫描进度**：实时显示当前扫描状态（SSE 推送）
- **重新登录**：手动触发二维码刷新

---

## 部署

### Docker

```bash
docker run -d \
  --name 115media \
  -p 8096:8096 \
  -v /your/data/path:/app/data \
  115media:latest
```

所有持久化数据写入 `/app/data/`：
```
/app/data/
├── db.sqlite        # 媒体库、认证信息、观看进度
└── cache/
    └── posters/     # 本地海报缓存
```

### Infuse 接入

1. Infuse → 添加服务器 → Jellyfin
2. 地址：`http://<家里机器IP>:8096`（局域网）或 `http://<Lattice IP>:8096`（远程）
3. 用户名/密码：115media 管理界面设置的账号

---

## 关键设计决策

| 决策 | 选择 | 原因 |
|------|------|------|
| 115 对接 | `SheltonZhu/115driver` | 已解决指纹/签名问题，AList 验证可用 |
| 认证方式 | 二维码扫码 | 不需要输入账号密码，体验好，不触发风控 |
| 播放流量 | 302 重定向到 CDN | 零本机带宽，速度最快 |
| 元数据来源 | TMDB API | 免费，中文支持，覆盖广 |
| 数据库 | SQLite | 个人使用，单机，零依赖 |
| 技术栈 | Go + Gin + GORM + Vue 3 | 与 Lattice 一致，可复用模式 |

---

## 未来扩展方向

**C 方向（Lattice showcase）**：
- 把 115media 打包为 Lattice 的"一键媒体节点"
- 作为 Lattice 官方 use case 展示私有网络的价值
- 在 Lattice 控制台里直接部署和管理 115media 节点

**D 方向（平台化）**：
- 支持更多云盘后端（阿里云盘、夸克、百度网盘）
- 多用户共享媒体库（家庭成员各自的观看进度）
- 转码支持（针对 Apple TV 不支持的格式）
- 对标 Plex/Jellyfin 的完整中国云盘用户解决方案
