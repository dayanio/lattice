# 全流程部署指南（控制面 + Linux 节点 + macOS + iOS）

本文带你从一台空的云主机开始，部署控制面，再依次接入一个 Linux/容器节点、一台 Mac 和一部 iPhone，并验证三端互通。每一步都写明了会出什么问题、怎么判断。

如果你只想部署控制面，见 [All-in-One 部署](/deploy/all-in-one)；Kubernetes 见 [Helm](/deploy/helm) 与 [K8s Operator](/deploy/k8s-operator)。

---

## 1. 架构与端口

```
                 ┌────────────────────── 云主机 ──────────────────────┐
                 │  latticed --standalone                              │
  管理/设备注册 ─►│    API + Dashboard   18090/tcp                     │
  握手信令 ─────►│    NATS（内嵌）       4222/tcp                     │
  中继数据 ─────►│    中继 (LRP)         6266/tcp                     │
                 │  coturn (STUN)        3478/udp                     │
                 └─────────────────────────────────────────────────────┘
        Linux/容器节点          macOS 节点（命令行或 App）          iPhone
              └────── WireGuard，直连（ICE）优先，失败走中继 ──────┘
                          节点之间的 WireGuard/ICE：51820/udp
```

| 端口 | 协议 | 谁在用 | 关掉的后果 |
|---|---|---|---|
| 18090 | tcp | 控制面 API、Dashboard、设备注册、服务发现 | 无法加入网络 |
| 4222 | tcp | NATS：节点之间的握手信令（SYN/OFFER/ANSWER 等）和网络图推送 | 节点无法协商连接 |
| 6266 | tcp | 中继：直连失败时承载 WireGuard 数据 | 直连不通的节点之间完全不通；日志里看不到明显报错 |
| 3478 | udp | STUN：ICE 探测公网地址 | 只能靠中继，直连率下降 |
| 51820 | udp | 节点之间的 WireGuard 与 ICE | 直连失败，走中继 |

要点：

- **握手信令走 NATS，数据走直连或中继**。所以 4222 不通时节点连不上；6266 不通时，只有能直连的节点才能通。
- 中继地址由控制面通过 `relay-advertise-url` 下发给各节点，必须是节点能访问到的地址。
- 云厂商的安全组要放行以上端口。**6266/tcp 最容易被漏掉**。

---

## 2. 准备

- 一台 Linux 云主机（本文以 x86_64、Debian/Ubuntu 为例，已装 Docker 用于运行 STUN 和测试容器）。
- 本机（用来构建）：Go；构建 Apple 端需要 Xcode 和 `gomobile`（脚本会自动安装）；iPhone 需开启开发者模式。
- 能用 SSH 密钥登录云主机的 root（或有 sudo 的用户）。

> 本仓库要求的 Go 版本比部分本机默认版本新。如果构建提示 Go 版本不够，设置 `GOTOOLCHAIN=auto`（Apple 端脚本已经这样设置）或指定具体版本。

---

## 3. 构建二进制

交叉编译 Linux 版的控制面与节点：

```bash
mkdir -p /tmp/cloud-deploy
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/cloud-deploy/latticed ./cmd/latticed
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/cloud-deploy/lattice  ./cmd/lattice
```

云主机是 ARM 时把 `GOARCH` 换成 `arm64`。也可以按 [安装](/guide/installation) 使用发布包。

---

## 4. 部署控制面

### 4.1 一键脚本（首次部署）

仓库里的 `hack/deploy-cloud.sh` 会完成下面 4.2 到 4.5 的全部步骤，并启动一个测试容器：

```bash
CLOUD_HOST=<云主机IP> bash hack/deploy-cloud.sh
```

它是幂等的：已经生成的凭据不会被覆盖。但它每次都会**重启控制面**，所以日常只更新节点时请用第 11 节的做法。

想理解每一步在做什么，或者不能用脚本时，按下面手工做。

### 4.2 上传与凭据

```bash
ssh root@<云主机IP> 'mkdir -p /opt/lattice/bin /etc/lattice /etc/lattice/config /var/lib/lattice'
# 先上传到临时名再改名：直接覆盖正在运行的可执行文件会报 "text file busy"
scp /tmp/cloud-deploy/latticed root@<云主机IP>:/opt/lattice/bin/.latticed.new
scp /tmp/cloud-deploy/lattice  root@<云主机IP>:/opt/lattice/bin/.lattice.new
ssh root@<云主机IP> 'mv -f /opt/lattice/bin/.latticed.new /opt/lattice/bin/latticed && mv -f /opt/lattice/bin/.lattice.new /opt/lattice/bin/lattice && chmod 755 /opt/lattice/bin/*'
```

在云主机上生成管理员密码和中继令牌（仅 root 可读）：

```bash
umask 077
{ echo "ADMIN_PASSWORD=$(openssl rand -base64 18)"; echo "RELAY_TOKEN=$(openssl rand -base64 18)"; } > /etc/lattice/credentials
```

之后的入网令牌、工作区 ID 也会追加写在这个文件里。**不要把这个文件的内容提交到仓库或贴到聊天里。**

### 4.3 systemd 服务

`/etc/systemd/system/lattice-controller.service`（把尖括号内容换成你的值，`RELAY_TOKEN` 取自上面的凭据文件）：

```ini
[Unit]
Description=Lattice all-in-one control plane (standalone)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/lattice/bin/latticed --standalone --config-dir /etc/lattice/config
Environment=LATTICE_LISTEN=0.0.0.0:18090
# 下发给节点的 NATS 地址必须是公网地址，否则默认的 127.0.0.1 只有云主机自己能用
Environment=LATTICE_SIGNALING_URL=nats://<云主机IP>:4222
Environment=LATTICE_RELAY_ADVERTISE_URL=<云主机IP>:6266
Environment=LATTICE_STUN_URL=<云主机IP>:3478
Environment=LATTICE_LRP_AUTH_TOKEN=<RELAY_TOKEN>
Environment=LATTICE_RELAY_URL=<云主机IP>:6266
Restart=always
RestartSec=3
WorkingDirectory=/var/lib/lattice

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable lattice-controller && systemctl restart lattice-controller
curl -s -o /dev/null -w '%{http_code}\n' http://<云主机IP>:18090/api/v1/discovery   # 期望 200
```

`--standalone` 表示不依赖 Kubernetes，数据放在内嵌数据库里，并且**在进程内启动中继（监听 6266）**。中继令牌通过 `LATTICE_LRP_AUTH_TOKEN` 设置：节点注册中继时必须带上它，下发给节点的中继地址会自动带上。

### 4.4 STUN

```bash
docker run -d --name lattice-stun --network host --restart unless-stopped \
  coturn/coturn --log-file=stdout --external-ip='$(detect-external-ip)'
```

`--network host` 让 3478/udp 直接监听在主机上。没有 STUN 时节点仍能靠中继通信，但直连率会下降。

### 4.5 首次登录、工作区与入网令牌

Dashboard 在 `http://<云主机IP>:18090`。管理员账号是 `admin`，初始密码由配置里的 `app.initAdmins` 决定（All-in-One 文档示例是 `changeme`，部署脚本会先尝试生成的密码再尝试 `123456`，具体以你的配置为准，**待确认**）。登录后立即改成强密码。

然后创建工作区和入网令牌，可以在 Dashboard 的"令牌"页面操作（它还能生成二维码，见第 7 节），也可以用 API：

```bash
API=http://<云主机IP>:18090
TOKEN=$(curl -s -X POST $API/api/v1/users/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<管理员密码>"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")

WS=$(curl -s -X POST $API/api/v1/workspaces/add -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"demo","namespace":"demo-ns","displayName":"Demo","maxNodeCount":50}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")

curl -s -X POST $API/api/v1/token/generate -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WS" \
  -H 'Content-Type: application/json' -d '{"name":"my-device","expiry":""}'
```

返回里的 `token` 就是入网令牌，后面所有节点都用它加入。

> 开启"入网审批"（`RequirePeerApproval`，见 ADR-0003）后，新设备注册成功也会先处于 `pending`，拿不到网络图，需要管理员在 Dashboard 批准后才会连上。

---

## 5. Linux / Docker 节点

### 5.1 主机上直接运行

```bash
sudo lattice init --server http://<云主机IP>:18090 --token <入网令牌>   # 写入 root 的 ~/.lattice/lattice.yaml
sudo lattice up                                                          # 前台运行
sudo lattice up --daemon                                                 # 后台运行，日志在 /var/log/lattice
```

`init` 和 `up` 要用同一个用户执行，否则读到的不是同一个配置目录（`~/.lattice` 指向各自的家目录）。也可以两条命令都加 `--config-dir <目录>` 显式指定。

常用参数：`--name` 设置显示名，`--level debug` 调高日志，`--enable-sys-log` 打开 WireGuard 与 ICE 的详细日志，`--wg-port` 指定 UDP 端口（默认 51820）。

### 5.2 用容器运行

```bash
docker run -d --name lattice-node --hostname node-1 --privileged \
  --add-host host.docker.internal:host-gateway \
  -v lattice-node-data:/root/.lattice \
  <你的镜像> sh -c "lattice init --server http://host.docker.internal:18090 --token <入网令牌> && exec lattice up --level debug --relay-url host.docker.internal:6266"
```

要点：

- `--privileged` 是创建 WireGuard 网卡和策略规则所需要的。
- 用数据卷保存 `/root/.lattice`，容器重建后节点身份不变。
- **容器访问不到它所在主机的公网 IP 上的 6266（云厂商会拦回环访问）**，所以节点与控制面在同一台主机时，用 `--relay-url host.docker.internal:6266` 覆盖中继地址，而不是依赖控制面下发的公网地址。控制面在另一台机器上时不需要这个参数。

---

## 6. macOS

macOS 有两种用法，二选一，**不要同时运行**：命令行和 App 会占用同一个 utun 网卡与 `10.96.0.x` 路由。

### 6.1 命令行

```bash
lattice init --config-dir ~/.lattice-mac --server http://<云主机IP>:18090 --token <入网令牌>
sudo lattice up --config-dir ~/.lattice-mac --level debug 2>&1 | tee /tmp/lattice-mac.log
```

两条命令都显式带上 `--config-dir`，避免 `sudo` 后读到 root 的另一个配置目录。`up` 需要 root 才能创建 utun。用 `tee` 把日志同时存到文件，排障时很有用。`lattice status` 也需要 root（否则会提示无法访问 `/var/run/wireguard/*.sock`）。

### 6.2 macOS App

```bash
cd apple
Scripts/build_framework.sh macos      # 生成 apple/Frameworks/MacOS/LatticeCore.xcframework（不入库）
xcodebuild -project LatticeApple.xcodeproj -scheme LatticeMac -configuration Debug \
  -destination 'generic/platform=macOS' build
```

也可以直接在 Xcode 里打开 `LatticeApple.xcodeproj`，选 `LatticeMac` 运行。签名团队在 `apple/project.yml` 的 `DEVELOPMENT_TEAM`，换成你自己的；隧道扩展需要 Network Extension 权限，用开发者账号自动签名即可。

第一次打开 App：点"加入网络"，填服务器地址（`http://<云主机IP>:18090`）、入网令牌和设备名，然后在系统弹出的"添加 VPN 配置"对话框里点允许。

- 设备名可以带空格，服务器会把它规范化（如 `MacBook Pro` 变成 `MacBook-Pro`）。提交 `79faa0ac` 之前的版本对带空格的名字会一直停在"连接中"。
- 隧道日志：`~/Library/Containers/io.lattice.mac.tunnel/Data/Library/Caches/lattice-ne.log`（引擎日志）和同目录下的 `lattice-tunnel.log`（启动与错误事件）。不需要 sudo 就能读。
- 管理功能（节点列表的改名、删除等）需要另外登录 Dashboard 账号，见 [Apple 端入网与登录改版设计](/design/apple-join-and-login)。

---

## 7. iOS

```bash
cd apple
Scripts/build_framework.sh ios         # 生成 apple/Frameworks/iOS/LatticeCore.xcframework
Scripts/build_install.sh               # 构建、安装到已连接的 iPhone 并启动
```

前提：iPhone 用数据线连接、已解锁并信任这台电脑、已开启开发者模式，签名团队同上。脚本按设备名包含 `iPhone` 查找设备，可以传参数指定：`Scripts/build_install.sh <设备名片段>`。安装后脚本会采样启动日志 15 秒，然后结束进程，所以需要在手机上重新打开 App。

加入网络的两种方式：

- **扫码**：在 Dashboard 的令牌页面点某个令牌的"二维码"，用 App 扫描。二维码内容是 `lattice://join?server=<Dashboard 地址>&token=<令牌>`。
- **手动输入**：服务器地址、入网令牌、节点名称。

首次连接同样会弹出系统的 VPN 授权。隧道日志的拉取方式：

```bash
xcrun devicectl device copy from --device <设备ID> --domain-type appDataContainer \
  --domain-identifier io.lattice.ios.tunnel --source Library/Caches/lattice-ne.log --destination ./ne.log
```

设备 ID 用 `xcrun devicectl list devices` 查看。

---

## 8. 验证三端互通

在任一节点上：

```bash
sudo lattice status                              # 容器里：docker exec <容器名> lattice status
```

每个对端会显示 `Transport`（`ice-ready` 直连、`lrp-ready` 走中继、`probing` 协商中）、`Path`（`direct` 或 `relayed`）、握手时间和状态。**以 `Path` 和 WireGuard 的 Endpoint 为准**：Endpoint 以 `fd6c:7270::` 开头表示当前走中继。

再用覆盖网络地址互 ping（地址在 `status` 或 Dashboard 里）：

```bash
ping -c 3 10.96.0.x
```

健康的表现：三个节点两两 0% 丢包；同一局域网内的 Mac 与手机是 `direct`；容器与手机通常是 `relayed`（容器在 Docker 桥接网络后面，ICE 一般打不通）。

---

## 9. 网络切换测试

用来验证手机在 wifi 与蜂窝之间切换时能自动恢复。

1. 三端互通后，在 Mac 上启动一个持续 ping 的脚本，把断开和恢复的时间点记到文件里（每秒 `ping -c 1 -W 1000 <手机覆盖网络地址>`，状态变化时写一行带 UTC 时间的记录）。在容器所在主机上对容器做同样的事。
2. 在手机上把 wifi 切到蜂窝（或反过来），记下切换时间。
3. 看两个数字：**断开到恢复的时长**和**是否出现反复重启**。日志里搜 `SYN on active LRP session` 的次数，正常应为个位数以内、不循环。

参考值（2026-09-19 实测，Mac 命令行到手机 13 s、容器到手机 29 s、Mac App 到手机 23 s）：正常在 30 s 内恢复。曾出现过一次 108 s 的偶发情况，原因是手机在蜂窝上的 NATS 连接反复失效，详见 [ADR-0006](/adr/0006-signaling-over-relay-fallback)。

---

## 10. 常见问题排查

| 现象 | 原因 | 处理 |
|---|---|---|
| 节点显示 `lrp-ready` 但一直 `Handshake: never`，或直连不通的节点之间完全不通 | 云安全组没放行 6266/tcp，中继连不上（日志里没有明显报错） | 放行 6266/tcp；在云主机上抓包看 SYN 是否到达 |
| 在 Mac 上用 `nc -z 云主机 6266` 显示成功，但实际连不上 | Mac 上的 Clash 等代理的 TUN 模式会在本地假装应答 TCP 连接 | 不要用 `nc` 判断；在云主机上 `tcpdump`，或换用真实连接读取应答 |
| Mac 上 `nats connect: dial tcp …:4222: i/o timeout`，偶发 | Mac 的代理让到服务器的新连接变慢，偶尔卡住几秒，而 NATS 客户端默认只等 2 秒 | 给云主机 IP 添加直连（DIRECT）规则；重试通常就能成功 |
| App 一直"连接中"，隧道日志里是 `record not found` | 设备名带空格，旧版本客户端用原名请求（已在 `79faa0ac` 修复） | 更新到包含该修复的版本；或把设备名改成不含空格 |
| 容器里的节点连不上同主机的中继 | 容器无法访问所在主机的公网 IP:6266 | 用 `--relay-url host.docker.internal:6266`（见 5.2） |
| 新注册的设备连不上，Dashboard 里显示 `pending` | 工作区开启了入网审批 | 管理员在 Dashboard 批准该设备 |
| 服务器上出现已经离线的旧节点（例如换过设备名后残留的记录） | 旧记录仍在数据库里，其他节点会周期性探测它 | 在 Dashboard 里删除旧节点 |
| Mac 上 `lattice status` 提示 `failed to list WireGuard devices … permission denied` | 需要 root 才能访问 WireGuard 的 socket | 用 `sudo lattice status` |
| 用命令行 agent 时手机、Mac 互相 ping 不通，且同时开着 Mac App | 两者抢占同一个 utun 与路由 | 只保留一个 |
| Docker 在服务器上加了 `-d <容器IP> ! -i docker0 -j DROP` 规则 | 这是 Docker 自己的容器保护规则，不是测试残留 | 不需要处理 |

远程操作的两个小提示：

- 在 `ssh` 命令里用 `pkill -f "<模式>"` 时，如果同一条命令里别处也出现了这个模式的文字，会把 ssh 自己的 shell 也杀掉。把 `pkill` 单独放在一次 `ssh` 里执行。
- 云主机的 sshd 偶尔会在突发连接后断开（退出码 255），长命令请写成脚本放到后台运行，并允许重试。

---

## 11. 升级与回滚

**只更新节点容器（不重启控制面）**：

```bash
# 1. 构建并上传新的 lattice
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/cloud-deploy/lattice ./cmd/lattice
scp /tmp/cloud-deploy/lattice root@<云主机IP>:/tmp/agentimg/lattice.new
# 2. 在云主机上构建新镜像（/tmp/agentimg 里已有 Dockerfile，来自 deploy-cloud.sh）
ssh root@<云主机IP> 'cd /tmp/agentimg && mv -f lattice.new lattice && chmod 755 lattice && docker build -q -t lattice-run-test:<新标签> .'
# 3. 停掉旧容器并改名保留；启动新容器时复用同一个数据卷和启动命令
```

原则：新旧容器共用同一个数据卷，**同一时间只能运行一个**；旧容器只停止不删除，出问题就停掉新的、启动旧的。复用启动命令时注意里面带入网令牌，不要把它打印出来（可以用 `docker inspect` 读到 shell 变量后传给新容器）。

**更新控制面**：按 4.2 上传（先传临时名再 `mv`），然后 `systemctl restart lattice-controller`。回滚前先保留旧二进制为 `latticed.bak`。

**更新 Apple 端**：重新执行 `Scripts/build_framework.sh`，再构建并安装。Mac 上如果是用 Xcode 构建的，停掉后重新运行即可。

---

## 12. 安全提醒

- 控制面暴露在公网，第一次登录后立即修改管理员密码。
- NATS 4222 目前没有鉴权（部署脚本的交付说明里也标注了这一点），只适合测试和受控环境；生产环境请限制来源地址或放在受保护的网络里。
- 中继令牌、入网令牌、管理员密码都保存在 `/etc/lattice/credentials`，权限要保持仅 root 可读，不要写进仓库、日志或聊天。
- 入网令牌的次数、有效期以服务器实现为准，发给设备后按需要回收（**语义待确认**）。
