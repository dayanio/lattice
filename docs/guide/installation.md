# 安装

Lattice 有两个二进制：

| 二进制 | 角色 | 说明 |
|--------|------|------|
| `lattice` | 边缘节点客户端 | 跑在每台要入网的 Linux / macOS / Windows 机器上 |
| `latticed` | All-in-One 控制面 | 内嵌 NATS + SQLite + API + Dashboard，**发布包仅提供 Linux 版**（macOS 可源码构建） |

---

## 一键安装脚本

自动探测操作系统和 CPU 架构，从 GitHub Releases 拉取对应的发布包：

```bash
curl -fsSL https://raw.githubusercontent.com/winstonfly/lattice/master/docs/public/install.sh | bash
```

**安装 latticed（All-in-One 控制面，仅 Linux 发布包）：**

```bash
curl -fsSL https://raw.githubusercontent.com/winstonfly/lattice/master/docs/public/install.sh | BINARY=latticed bash
```

**非交互式安装并直接完成入网配置**（等价于装完自动执行 `lattice init --server ... --token ...`）：

```bash
curl -fsSL https://raw.githubusercontent.com/winstonfly/lattice/master/docs/public/install.sh | bash -s -- \
  --server http://<控制面地址>:18090 --token <入网令牌>
```

脚本同时支持环境变量和长选项两种传参方式：

| 参数 | 说明 |
|------|------|
| `BINARY` / `--binary` | `lattice`（默认）或 `latticed` |
| `TAG` / `--tag` | 指定版本（如 `v0.1.6-alpha`），缺省取最新 Release |
| `INSTALL_DIR` / `--install-dir` | 安装目录，默认 `/usr/local/bin` |
| `SERVER` / `--server` | 控制面地址，与 `--token` 同时给出时非交互式写入配置 |
| `TOKEN` / `--token` | 入网令牌 |

安装完成后验证：

```bash
lattice --version
```

> **注意**：GitHub Releases 的发布节奏落后于主线代码。要使用最新功能（入网审批、入网二维码、LatticeDNS、子网路由、对外发布等），建议直接[从源码构建](#从源码构建)。

---

## 从源码构建

需要 Go 1.26+（本机 Go 版本较旧时加 `GOTOOLCHAIN=auto`）：

```bash
git clone https://github.com/dayanio/lattice.git
cd lattice

# 边缘节点客户端（全平台）
make build SERVICE=lattice          # 输出 bin/lattice

# All-in-One 控制面（会先打包前端，Dashboard 静态资源内嵌进二进制）
make build SERVICE=latticed         # 输出 bin/latticed
```

不用 make 也可以直接 `go build`：

```bash
go build -o bin/lattice  ./cmd/lattice
go build -o bin/latticed ./cmd/latticed
```

交叉编译 Linux 版（在 Mac 上构建、部署到云主机时用，见[全流程部署](../deploy/end-to-end)）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/latticed ./cmd/latticed
```

---

## 手动安装

### macOS（Homebrew）

```bash
brew tap dayanio/tap
brew install lattice
```

> Homebrew 安装的是命令行客户端。macOS 图形客户端（App + Network Extension 隧道）不在 brew 里，从源码构建，见[全流程部署](../deploy/end-to-end)的 macOS 一节。

### 二进制下载

从 [GitHub Releases](https://github.com/winstonfly/lattice/releases) 下载对应平台的包（资产命名 `lattice_<版本>_<系统>_<架构>.tar.gz`），解压后移动到 PATH：

```bash
# Linux amd64
curl -fsSL https://github.com/winstonfly/lattice/releases/latest/download/lattice_linux_amd64.tar.gz | tar xz
sudo mv lattice /usr/local/bin/

# macOS Apple Silicon
curl -fsSL https://github.com/winstonfly/lattice/releases/latest/download/lattice_darwin_arm64.tar.gz | tar xz
sudo mv lattice /usr/local/bin/
```

---

## 部署控制面（latticed）

单机 / 云主机部署（推荐，`--standalone` 模式，无需 Kubernetes）：

```bash
./bin/latticed --standalone --config-dir /etc/lattice/config
```

- Dashboard + API 默认监听 `:8080`（`LATTICE_LISTEN` 可改）
- 内嵌 NATS 信令 `:4222`、LRP 中继 `:6266`
- 数据写在启动目录的 `lattice.db`，建议固定 `--config-dir` 与工作目录启动

详细说明见 [All-in-One 部署](../deploy/all-in-one)，公网云主机 + 三端接入的完整流程见[全流程部署](../deploy/end-to-end)。

### Docker

镜像随 Release 发布（linux/amd64）：

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

SQLite 默认写在进程工作目录下的 `lattice.db`，容器里务必像上面那样用 `LATTICE_DATABASE_DSN` 指到挂载卷，否则重建容器数据丢失。

自行构建任意版本：

```bash
docker build --build-arg TARGETSERVICE=latticed -t latticed:local -f Dockerfile .
```

### Kubernetes（Helm）

```bash
helm install lattice oci://ghcr.io/alatticeio/charts/lattice \
  --namespace lattice-system --create-namespace
```

详细部署说明见 [Helm 部署](../deploy/helm) 和 [Kubernetes 部署](../deploy/k8s-operator)。
