# 快速上手

10 分钟内在单机拉起控制面，并让一台设备加入 mesh。

## 0. 准备

- 已按[安装指南](/guide/installation)装好 `lattice`（客户端）并能构建 `latticed`（控制面）
- 本机试用只需一台机器；要让局域网其它设备加入，先想好它们访问本机用的地址（下文用 `192.168.1.8` 举例，换成你的局域网 IP）

## 1. 启动控制面（standalone 模式）

```bash
make build SERVICE=latticed        # 首次会先打包前端
./bin/latticed --standalone --config-dir .lattice-demo
```

看到 `All systems go! Latticed is ready.` 即启动完成：

- **Dashboard / API**：`http://localhost:8080`
- 内嵌 NATS 信令 `:4222`、LRP 中继 `:6266`（standalone 模式下都在本进程内）
- 数据库 `lattice.db` 落在**启动时的工作目录**——固定用同一个目录启动，换目录等于换库

**要让其它设备加入**，用环境变量把"下发给设备的地址"指向它们可达的地址（这是最常见的踩坑点，默认回环地址只有本机能用）：

```bash
LATTICE_LISTEN=0.0.0.0:8080 \
LATTICE_SIGNALING_URL=nats://192.168.1.8:4222 \
LATTICE_RELAY_ADVERTISE_URL=192.168.1.8:6266 \
./bin/latticed --standalone --config-dir .lattice-demo
```

打开 Dashboard，用初始管理员 `admin / 123456` 登录，**立即修改密码**。

## 2. 签发入网令牌

Dashboard → **令牌** 页面 → 生成令牌。令牌行菜单里可以直接生成**入网二维码**（手机 App 扫码用）。

也可以用 API 创建工作区和令牌，见[全流程部署](/deploy/end-to-end)的"首次登录"一节。

## 3. 设备加入

**命令行（macOS / Linux）：**

```bash
lattice init --server http://192.168.1.8:8080 --token <入网令牌>
sudo lattice up          # 需要 root 创建 WireGuard 网卡
```

`lattice init` 交互式回答服务器地址和令牌也行；NATS 信令地址会通过 `/api/v1/discovery` 自动发现，不用手填。

**手机 / Mac App：** 打开 App → 扫描令牌页的入网二维码（内容为 `lattice://join?server=<面板地址>&token=<令牌>`），首次连接允许系统弹出的 VPN 授权。Apple 客户端从源码构建，见[全流程部署 · macOS / iOS](/deploy/end-to-end)。

## 4. 验证

```bash
sudo lattice status
```

设备应显示已分配的覆盖网络地址（`10.96.0.x`）和对端列表；Dashboard 的设备页也能看到。两台设备互相 ping：

```bash
ping -c 3 10.96.0.x
```

`status` 里 `Path: direct` 表示直连（ICE 打洞成功），`relayed` 表示走了中继。

## 5. 下一步

- **公网云主机部署 + Mac/iPhone 三端接入**：[全流程部署](/deploy/end-to-end)
- **家庭/个人模式**（回家可用、出门也可控）：[个人模式部署](/deploy-personal-mode)
- **客户端更多用法**（daemon、容器节点、workspace/token/policy 管理）：[Agent Setup](/guide/agent)
- **Kubernetes 部署**：[Helm](/deploy/helm) · [Kustomize](/deploy/k8s-operator)
