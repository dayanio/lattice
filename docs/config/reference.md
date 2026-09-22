# Configuration Reference

配置采用 onion 模型（后者覆盖前者）：内置默认值 → `lattice.yaml` → `lattice.{env}.yaml` → `LATTICE_*` 环境变量 → CLI flags。配置目录由 `--config-dir`（CLI）或 `LATTICE_CONFIG_DIR`（环境变量）决定，缺省 `~/.lattice`；目录不存在时自动创建并写入默认配置。

## Agent Config（`~/.lattice/lattice.yaml`）

由 `lattice init` 创建，`lattice up` 读取：

```yaml
server-url: "http://lattice.example.com:18090"
token: "<入网令牌>"
name: "office-mac"          # 显示名（可选）
```

| Key | 必填 | 说明 |
|-----|------|------|
| `server-url` | 是 | 控制面 HTTP 地址 |
| `token` | 是 | 入网令牌（Dashboard 令牌页签发，可扫码） |
| `name` | 否 | 设备显示名；缺省取主机名 |
| `relay-url` | 否 | 覆盖控制面下发的 TCP 中继地址（容器节点连 `host.docker.internal:6266` 时用） |
| `relay-quic-url` | 否 | QUIC 中继地址，空 = 禁用 |
| `relay-auth-token` | 否 | 中继接入令牌（服务端设置 `LATTICE_LRP_AUTH_TOKEN` 时必填） |
| `signaling-url` | 否 | 覆盖自动发现的 NATS 地址（一般不用填） |
| `stun-url` | 否 | 覆盖 STUN；空 = 控制面下发或内置列表 |
| `wg-port` | 否 | WireGuard/ICE UDP 端口，默认 `51820` |
| `interface-name` | 否 | WireGuard 网卡名（默认 `wf0`） |
| `enforcer-mode` | 否 | 策略执行：`auto` / `iptables` / `ebpf`（pro），默认 `auto` |
| `enable-dns` | 否 | 引擎内置 `*.lattice` DNS 解析（LatticeDNS） |
| `ingress-addr` | 否 | 对外发布（publish gateway）HTTP ingress 监听地址，空 = 禁用 |
| `metrics-addr` | 否 | 指标上报地址（配合 `--enable-metric`） |
| `netmap-poll-interval` | 否 | 网络图拉取周期，默认 `30s`，`0` 关闭 |
| `level` | 否 | 日志级别，默认 `info` |

非敏感项大多可直接用 CLI flag 传入（如 `lattice up --name xxx --level debug`），加 `--save` 会持久化到配置文件。

## Server Config（latticed）

配置文件为 `<config-dir>/lattice.yaml`，所有键都有内置默认值。

### 核心连接项

| Key | 默认值 | 说明 |
|-----|--------|------|
| `listen` | `:8080` | Dashboard + REST API 监听地址 |
| `standalone` | `false` | 非 K8s 部署**必须开**（CLI `--standalone`） |
| `signaling-url` | `""` | 下发给设备的 NATS 地址；空 = `nats://localhost:4222`（仅本机可用）⚠ |
| `relay-advertise-url` | `""` | 下发给设备的中继地址；standalone 空值兜底 `127.0.0.1:6266` ⚠ |
| `stun-url` | `""` | STUN；空 = 内置多服务器列表，自建填 `host:3478` |
| `relay-url` | `:6266` | 中继监听地址（standalone 进程内启动） |
| `port` | `3478` | 内置 STUN 服务端口（pro 版） |
| `resync-interval` | 内置值 | standalone reconcile 周期（如 `30s`） |

⚠ 这两个"下发地址"是自部署最常见的故障源：必须是**设备能访问到**的地址（局域网 IP 或公网 IP），不能是回环。详见[个人/家庭模式](/deploy-personal-mode)。

### App / Database

```yaml
app:
  name: "Lattice"
  initAdmins:                # 初始管理员（仅首次建库时生效）
    - username: "admin"
      password: "123456"

database:
  driver: "sqlite"           # sqlite（默认）/ mysql / mariadb
  dsn: ""                    # 空 = 工作目录下 lattice.db；MySQL 填标准 DSN
```

### 其它常用

| Key | 默认值 | 说明 |
|-----|--------|------|
| `metrics-addr` | `:8443` | Prometheus metrics 地址（`enable-metric` 开启时） |
| `monitor.address` | `""` | VictoriaMetrics 远端写入地址（pro） |
| `dex.providerUrl` | `""` | Dex OIDC，空 = 禁用（pro） |

### 环境变量

所有配置键都可用 `LATTICE_` 前缀环境变量覆盖（大写，`.` / `-` → `_`）：`LATTICE_LISTEN`、`LATTICE_SIGNALING_URL`、`LATTICE_RELAY_ADVERTISE_URL`、`LATTICE_DATABASE_DSN` 等。

与配置文件无关、直接读环境变量的两个密钥：

| 环境变量 | 说明 |
|----------|------|
| `LATTICE_JWT_SECRET` | Dashboard 登录态签名密钥；未设置时用内置开发密钥，仅限本机试用 |
| `LATTICE_TOKEN_TTL` | 管理端 JWT 有效期（秒），默认 7 天 |
| `LATTICE_LRP_AUTH_TOKEN` | 中继接入令牌（服务端与节点两侧需一致） |
