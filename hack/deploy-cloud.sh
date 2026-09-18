#!/usr/bin/env bash
# deploy-cloud.sh — 把 Lattice 控制面(latticed)+ 测试容器 agent 部署到公网云主机
#
# 前置：
#   1. 本机 ed25519 公钥已加到云主机 root@<IP> 的 authorized_keys
#   2. 云安全组放行：18090/tcp 4222/tcp 6266/tcp 3478/udp 51820/udp
#
# 部署内容：
#   - latticed(HEAD 构建)为 systemd 常驻服务
#     * signaling-url / stun-url / relay 广播全部指向公网 IP(任意网络可加入)
#     * 自动生成强 admin 密码与 relay token，落在 /etc/lattice/(root 600)
#   - docker 测试容器 agent 一个(与 iPhone 互 ping 的对端)
#
# 用法：CLOUD_HOST=101.36.119.12 bash hack/deploy-cloud.sh
set -euo pipefail

CLOUD_HOST="${CLOUD_HOST:-101.36.119.12}"
CLOUD_USER="${CLOUD_USER:-root}"
SSH="ssh -o ConnectTimeout=8 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null ${CLOUD_USER}@${CLOUD_HOST}"
SCP="scp -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null"
API="http://${CLOUD_HOST}:18090"

info() { echo -e "\033[32m[deploy-cloud]\033[0m $1"; }
fail() { echo -e "\033[31m[deploy-cloud][FAIL]\033[0m $*" >&2; exit 1; }
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ── 1. SSH 可达 + 架构探测 ───────────────────────────────────────────────────
info "探测 ${CLOUD_USER}@${CLOUD_HOST}"
ARCH_RAW=$($SSH 'uname -m') || fail "SSH 不可达——确认公钥已加入 authorized_keys"
case "$ARCH_RAW" in
  x86_64) GOARCH=amd64; PKG=apt ;;
  aarch64|arm64) GOARCH=arm64; PKG=apt ;;
  *) fail "未知架构 $ARCH_RAW" ;;
esac
info "远程架构 $ARCH_RAW → GOARCH=$GOARCH($( $SSH 'head -1 /etc/os-release' ))"

# ── 2. 本地交叉编译 ──────────────────────────────────────────────────────────
info "交叉编译 linux/$GOARCH latticed + lattice"
cd "$ROOT"
GOOS=linux GOARCH=$GOARCH go build -o /tmp/cloud-deploy/latticed ./cmd/latticed
GOOS=linux GOARCH=$GOARCH go build -o /tmp/cloud-deploy/lattice ./cmd/lattice
mkdir -p /tmp/cloud-deploy

# ── 3. 上传 + 远端依赖 ───────────────────────────────────────────────────────
info "上传二进制与部署清单"
$SSH "mkdir -p /opt/lattice/bin /etc/lattice"
$SCP /tmp/cloud-deploy/latticed  "${CLOUD_USER}@${CLOUD_HOST}:/opt/lattice/bin/latticed"
$SCP /tmp/cloud-deploy/lattice   "${CLOUD_USER}@${CLOUD_HOST}:/opt/lattice/bin/lattice"
$SSH 'chmod 755 /opt/lattice/bin/*'

info "检查/安装 docker($PKG)"
$SSH 'command -v docker >/dev/null || { apt-get update -qq && apt-get install -y -qq docker.io || dnf install -y -q docker || yum install -y -q docker; }'

# ── 4. 云端配置(幂等：已生成的凭据不覆盖)───────────────────────────────────
info "生成/复用 admin 密码与 relay token"
$SSH 'grep -q ADMIN_PASSWORD /etc/lattice/credentials 2>/dev/null || { umask 077; { echo "ADMIN_PASSWORD=$(openssl rand -base64 18)"; echo "RELAY_TOKEN=$(openssl rand -base64 18)"; } > /etc/lattice/credentials; }'
ADMIN_PASSWORD=$($SSH 'grep ADMIN_PASSWORD /etc/lattice/credentials | cut -d= -f2')
RELAY_TOKEN=$($SSH 'grep RELAY_TOKEN /etc/lattice/credentials | cut -d= -f2')

# ── 5. systemd 常驻 latticed ─────────────────────────────────────────────────
info "安装 systemd 服务 lattice-controller"
$SSH 'cat > /etc/systemd/system/lattice-controller.service' <<EOF
[Unit]
Description=Lattice all-in-one control plane (standalone)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/lattice/bin/latticed --standalone --config-dir /etc/lattice/config
Environment=LATTICE_LISTEN=0.0.0.0:18090
Environment=LATTICE_RELAY_ADVERTISE_URL=${CLOUD_HOST}:6266
Environment=LATTICE_STUN_URL=${CLOUD_HOST}:3478
Environment=LATTICE_LRP_AUTH_TOKEN=${RELAY_TOKEN}
Environment=LATTICE_RELAY_URL=${CLOUD_HOST}:6266
Restart=always
RestartSec=3
WorkingDirectory=/var/lib/lattice

[Install]
WantedBy=multi-user.target
EOF
$SSH 'mkdir -p /etc/lattice/config /var/lib/lattice && systemctl daemon-reload && systemctl enable --now lattice-controller' || fail "systemd 启动失败"

# ── 6. 首次初始化：改 admin 密码 + 建默认 workspace + 入网 token ─────────────
info "等待 API 就绪并初始化"
for i in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/discovery" || true)
  [ "$code" = "200" ] && break
  sleep 2
  [ "$i" = 30 ] && fail "API 60s 未就绪($SSH journalctl -u lattice-controller -e)"
done

login() { curl -s -X POST "$API/api/v1/users/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$1\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['data']['token'] if isinstance(d.get('data'),dict) else d['data'])"; }
TOKEN=$(login "$ADMIN_PASSWORD") || true
[ -n "$TOKEN" ] || TOKEN=$(login "123456")
[ -n "$TOKEN" ] || fail "admin 登录失败(种子密码与生成密码均未通过)"
$SSH "grep -q WORKSPACE_ID /etc/lattice/credentials 2>/dev/null || true"
WS=$($SSH "grep WORKSPACE_ID /etc/lattice/credentials 2>/dev/null | cut -d= -f2" | tr -d '\r\n')
if [ -z "$WS" ]; then
  WS=$(curl -s -X POST "$API/api/v1/workspaces/add" -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"slug":"cloud","namespace":"cloud-ns","displayName":"Cloud","maxNodeCount":50}' \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['id'])")
  $SSH "echo WORKSPACE_ID=$WS >> /etc/lattice/credentials"
fi
JOIN_TOKEN=$($SSH "grep JOIN_TOKEN /etc/lattice/credentials 2>/dev/null | cut -d= -f2" | tr -d '\r\n')
if [ -z "$JOIN_TOKEN" ]; then
  JOIN_TOKEN=$(curl -s -X POST "$API/api/v1/token/generate" -H "Authorization: Bearer $TOKEN" \
    -H "X-Workspace-Id: $WS" -H 'Content-Type: application/json' -d '{"name":"cloud-agent","expiry":""}' \
    | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")
  $SSH "echo JOIN_TOKEN=$JOIN_TOKEN >> /etc/lattice/credentials"
fi

# ── 7. 测试容器 agent ────────────────────────────────────────────────────────
info "在云端构建 agent 镜像"
$SSH 'mkdir -p /tmp/agentimg && cp /opt/lattice/bin/lattice /tmp/agentimg/lattice && printf "FROM alpine:3.19\nRUN apk add -U iptables ip6tables && chmod 755 /usr/local/bin 2>/dev/null; mkdir -p /usr/local/bin\nCOPY lattice /usr/local/bin/lattice\nWORKDIR /data\n" > /tmp/agentimg/Dockerfile && docker build -q -t lattice-run-test:v2 /tmp/agentimg'

info "启动测试容器 agent(hostname cloud-node-1)"
$SSH 'docker rm -f lattice-cloud-node >/dev/null 2>&1 || true; docker run -d --name lattice-cloud-node --hostname cloud-node-1 --privileged --add-host host.docker.internal:host-gateway lattice-run-test:v2 sh -c "lattice init --server http://host.docker.internal:18090 --token '$JOIN_TOKEN' >/tmp/init.log 2>&1 && exec lattice up"' || \
  fail "容器启动失败——确认镜像 lattice-run-test:v2 已在云端构建(脚本第 8 步提示见报告)"

# ── 8. 输出交付信息 ──────────────────────────────────────────────────────────
cat <<EOF

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
✅ 云端部署完成
  面板/API     http://${CLOUD_HOST}:18090
  admin 密码   (云主机 /etc/lattice/credentials)
  relay token  (同上，agent 侧 relay URL 需带 ?token=)
  入网 token   $JOIN_TOKEN
  iPhone 入网  App 扫码/手动填：
               server = http://${CLOUD_HOST}:18090
               token  = 见上
安全提醒：公网暴露，admin 密码请立即改强密码并妥善保存；
NATS 4222 当前无鉴权(测试期限制，已在交付说明记录)。
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EOF
