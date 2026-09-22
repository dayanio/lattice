# All-in-One 部署

`latticed` 将 NATS + SQLite + API + Dashboard 打包为单个进程，无需任何外部依赖。**非 Kubernetes 环境一律加 `--standalone` 运行**：数据面（节点注册、网络图下发、入网审批）由内嵌数据库直接服务，并在进程内启动 LRP 中继（监听 `:6266`）。

---

## 二进制运行（推荐）

```bash
# 构建或下载见安装指南；首次运行会自动生成配置文件
./bin/latticed --standalone --config-dir /etc/lattice/config
```

- Dashboard + REST API 默认监听 `:8080`（`listen` / `LATTICE_LISTEN` 可改）
- 内嵌 NATS 信令 `:4222`
- standalone 模式内嵌 LRP 中继 `:6266`
- 初始管理员 `admin / 123456`（`app.initAdmins` 可改），**首次登录后立即修改**
- SQLite 落在**启动时工作目录**下的 `lattice.db`——固定工作目录与 `--config-dir` 启动，换目录等于换库

**要让本机以外的设备加入**，必须把"下发给设备的地址"设为设备可达的地址（最常见故障源，详见[个人/家庭模式](/deploy-personal-mode)的黄金法则）：

```bash
LATTICE_LISTEN=0.0.0.0:8080 \
LATTICE_SIGNALING_URL=nats://<本机局域网或公网IP>:4222 \
LATTICE_RELAY_ADVERTISE_URL=<IP>:6266 \
./bin/latticed --standalone --config-dir /etc/lattice/config
```

- `signaling-url` 留空时兜底 `nats://127.0.0.1:4222`，外部设备永远连不上
- `relay-advertise-url` 留空时兜底 `127.0.0.1:6266`，打洞失败的节点之间会不通
- 中继令牌用 `LATTICE_LRP_AUTH_TOKEN` 设置后，节点注册中继必须携带

### systemd 服务

```ini
[Unit]
Description=Lattice All-in-One Control Plane
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/latticed --standalone --config-dir /etc/lattice/config
Environment=LATTICE_LISTEN=0.0.0.0:8080
Environment=LATTICE_SIGNALING_URL=nats://<服务器IP>:4222
Environment=LATTICE_RELAY_ADVERTISE_URL=<服务器IP>:6266
WorkingDirectory=/var/lib/lattice
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now latticed
```

完整的公网部署（含 coturn STUN、凭据管理、升级回滚）见[全流程部署](/deploy/end-to-end)，也可以直接用仓库里的 `hack/deploy-cloud.sh` 一键脚本。

---

## Docker

```bash
docker run -d \
  --name latticed \
  --restart unless-stopped \
  -p 8080:8080 \
  -p 4222:4222 \
  -p 6266:6266 \
  -e LATTICE_DATABASE_DSN=/data/lattice.db \
  -v lattice-data:/data \
  ghcr.io/alatticeio/latticed:latest --standalone
```

- 镜像随 Release 发布（linux/amd64）；任意版本可自行构建：`docker build --build-arg TARGETSERVICE=latticed -t latticed:local -f Dockerfile .`
- SQLite 默认写在进程工作目录，容器里必须用 `LATTICE_DATABASE_DSN` 指到挂载卷，否则容器重启数据丢失
- 面板：`http://<host>:8080`，初始账号 `admin / 123456`

### Docker Compose

```yaml
services:
  latticed:
    image: ghcr.io/alatticeio/latticed:latest
    restart: unless-stopped
    command: ["--standalone"]
    ports:
      - "8080:8080"
      - "4222:4222"
      - "6266:6266"
    volumes:
      - lattice-data:/data
    environment:
      - LATTICE_DATABASE_DSN=/data/lattice.db
      - LATTICE_SIGNALING_URL=nats://<服务器IP>:4222
      - LATTICE_RELAY_ADVERTISE_URL=<服务器IP>:6266

volumes:
  lattice-data:
```

---

## 配置文件

配置查找顺序（onion 模型，后者覆盖前者）：`--config-dir/lattice.yaml` → `lattice.{env}.yaml` → `LATTICE_*` 环境变量 → CLI flags。`--config-dir` 缺省为 `~/.lattice`，也可用 `LATTICE_CONFIG_DIR` 指定。配置文件不存在时会自动生成一份默认配置。

完整示例：

```yaml
listen: "0.0.0.0:8080"      # Dashboard + API 监听地址
standalone: true             # 也可只用 CLI flag --standalone
name: "Lattice"
env: "production"

signaling-url: "nats://<服务器IP>:4222"   # ⚠ 下发给设备的 NATS 地址，勿留回环
relay-advertise-url: "<服务器IP>:6266"    # ⚠ 下发给设备的中继地址
stun-url: ""                 # 留空 = 使用内置 STUN 服务器列表；自建填 "host:3478"

database:
  driver: "sqlite"           # sqlite（默认）/ mysql / mariadb
  dsn: "data/lattice.db"     # SQLite 时为文件路径（相对工作目录）
  # dsn: "root:pass@tcp(mysql:3306)/lattice?charset=utf8mb4&parseTime=True"

app:
  name: "Lattice"
  initAdmins:                # 初始管理员，首次登录后请改密
    - username: "admin"
      password: "123456"

metrics-addr: ":8443"        # Prometheus metrics（enable-metric 开启时）
```

### 环境变量覆盖

配置项均可通过 `LATTICE_` 前缀环境变量覆盖（大写，`.` 和 `-` 替换为 `_`）。常用：

| 环境变量 | 对应配置 | 说明 |
|----------|----------|------|
| `LATTICE_LISTEN` | `listen` | Dashboard/API 监听地址 |
| `LATTICE_STANDALONE` | `standalone` | 非 K8s 部署必开 |
| `LATTICE_SIGNALING_URL` | `signaling-url` | 下发给设备的 NATS 地址 |
| `LATTICE_RELAY_ADVERTISE_URL` | `relay-advertise-url` | 下发给设备的中继地址 |
| `LATTICE_STUN_URL` | `stun-url` | STUN 服务地址 |
| `LATTICE_DATABASE_DSN` | `database.dsn` | SQLite 文件路径或 MySQL DSN |
| `LATTICE_JWT_SECRET` | — | Dashboard 登录态签名密钥；未设置时使用内置开发密钥，仅限本机试用 |
| `LATTICE_LRP_AUTH_TOKEN` | — | 中继接入令牌 |

---

## 使用 MySQL/MariaDB

默认 SQLite，无需额外配置。切换 MySQL/MariaDB：

```yaml
database:
  driver: "mysql"
  dsn: "user:pass@tcp(mysql-host:3306)/lattice?charset=utf8mb4&parseTime=True"
```

或通过环境变量传入（推荐，避免密码写入配置文件）：

```bash
docker run -d \
  -e LATTICE_DATABASE_DRIVER=mysql \
  -e LATTICE_DATABASE_DSN="user:pass@tcp(mysql-host:3306)/lattice?charset=utf8mb4&parseTime=True" \
  ghcr.io/alatticeio/latticed:latest --standalone
```

---

## 常见问题

**本机能打开面板，其它设备加不进来**

十有八九是下发的信令地址是回环地址。设置 `LATTICE_SIGNALING_URL` 为设备可达的 `nats://<IP>:4222`，完整对照表见[个人/家庭模式](/deploy-personal-mode)的故障对照表。

**agent 无法连接信令**

agent 只需要 `--server-url` 指向 `http://<host>:8080`，会自动通过 `/api/v1/discovery` 获取 NATS 地址（该地址来自服务端的 `signaling-url`）。个别网络环境也可在 agent 侧用 `LATTICE_SIGNALING_URL` 直接覆盖。

**启动后看不到历史数据 / 数据"丢了"**

SQLite 写在启动时工作目录的 `lattice.db`。换目录启动等于换库（systemd 单元里务必配 `WorkingDirectory`，或显式设置 `LATTICE_DATABASE_DSN` 为绝对路径）。

**数据持久化（Docker）**

必须挂载 volume 并设置 `LATTICE_DATABASE_DSN` 指向卷内路径，见上文 Docker 示例。

**Kubernetes 模式与 standalone 模式的区别**

不带 `--standalone` 时，`latticed` 走 K8s controller 路径（节点/策略/网络图由 CRD + API Server 承载），需要集群环境；`--standalone` 全部由内嵌数据库承载，单机即可运行。K8s 部署见 [Helm](/deploy/helm) 与 [Kustomize](/deploy/k8s-operator)。
