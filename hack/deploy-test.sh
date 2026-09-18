#!/usr/bin/env bash
# deploy-test.sh — 本机测试环境幂等重部署（服务端 + 容器 agent 群）
#
# 流程（与 2026-09-17 手工部署验证一致，已脚本化）：
#   1. 探测本机 LAN IP（iPhone/容器可达性依赖它）
#   2. 校正 cfg/lattice.yaml 的 signaling-url 指向当前 IP
#   3. 从 HEAD 编译 latticed → 停旧 → 换二进制 → 起新 → 健康检查
#   4. 编译 linux/arm64 引擎 → 构建 agent 容器镜像
#   5. 重建三个容器 agent（保留各自的 ~/.lattice 卷以保住设备密钥）
#   6. 调用 hack/verify-mesh.sh 做连通性断言
#
# 可覆盖环境变量：RUN_DIR ADMIN_USER ADMIN_PASS WORKSPACE_ID
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${RUN_DIR:-/tmp/lattice-run-test}"
CFG_DIR="$RUN_DIR/cfg"
CFG="$CFG_DIR/lattice.yaml"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-123456}"
API="http://127.0.0.1:18090"

info() { echo -e "\033[32m[deploy-test]\033[0m $*"; }
fail() { echo -e "\033[31m[deploy-test][FAIL]\033[0m $*" >&2; exit 1; }

cd "$ROOT"

# ── 1. LAN IP ────────────────────────────────────────────────────────────────
LAN_IP="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)"
[ -n "$LAN_IP" ] || fail "无法探测 LAN IP（en0/en1 均无地址）"
info "LAN IP = $LAN_IP"

# ── 2. signaling-url 对齐当前 IP ─────────────────────────────────────────────
mkdir -p "$CFG_DIR"
if [ ! -f "$CFG" ]; then
  fail "缺少 $CFG —— 首次部署请先手动初始化测试配置（含 admin 账号）"
fi
python3 - "$CFG" "$LAN_IP" <<'EOF'
import re, sys
p, ip = sys.argv[1], sys.argv[2]
s = open(p).read()
s = re.sub(r'signaling-url: \S+', f'signaling-url: nats://{ip}:4222', s)
open(p, 'w').write(s)
EOF
grep -q "nats://$LAN_IP:4222" "$CFG" || fail "signaling-url 修正失败"

# ── 3. 服务端：编译 → 停旧 → 换二进制 → 起新 → 健康检查 ─────────────────────
info "编译 latticed（HEAD）"
go build -o "$RUN_DIR/latticed.new" ./cmd/latticed

info "停止旧服务端"
pkill -f "latticed --standalone" 2>/dev/null || true
sleep 3
pkill -9 -f "latticed --standalone" 2>/dev/null || true
sleep 1

mv "$RUN_DIR/latticed.new" "$RUN_DIR/latticed"
info "启动新服务端"
cd "$RUN_DIR"
nohup ./latticed --standalone --config-dir "$CFG_DIR" > latticed.log 2>&1 &
cd "$ROOT"

for i in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/discovery" || true)
  [ "$code" = "200" ] && break
  sleep 1
  [ "$i" = 30 ] && fail "服务端 30s 未就绪（看 $RUN_DIR/latticed.log）"
done
discovered=$(curl -s "$API/api/v1/discovery" | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['nats_url'])")
[[ "$discovered" == *"@$LAN_IP:"* ]] || [[ "$discovered" == *"//$LAN_IP:"* ]] || fail "discovery 广播的 nats_url($discovered) 未指向 $LAN_IP"
info "服务端就绪，discovery → $discovered"

# ── 4. 容器镜像 ──────────────────────────────────────────────────────────────
info "编译 linux/arm64 引擎并构建镜像 lattice-run-test:v2"
GOOS=linux GOARCH=arm64 go build -o "$RUN_DIR/lattice-linux" ./cmd/lattice
cat > "$RUN_DIR/Dockerfile.v2" <<'EOF'
FROM lattice-run-test
COPY lattice-linux /usr/local/bin/lattice
RUN chmod 755 /usr/local/bin/lattice
EOF
docker build -q -f "$RUN_DIR/Dockerfile.v2" -t lattice-run-test:v2 "$RUN_DIR" >/dev/null

# ── 5. 重建容器 agent 群 ─────────────────────────────────────────────────────
# 说明：容器内 ~/.lattice 保存设备私钥。volume 挂载的容器重建后密钥不变，
# 能以原身份 resume；未挂卷的容器会以新密钥全新入网（可能落到 pending）。
start_agent() { # name hostname token volume(可空)
  local name="$1" host="$2" token="$3" vol="${4:-}"
  docker rm -f "$name" >/dev/null 2>&1 || true
  local volargs=()
  [ -n "$vol" ] && volargs=(-v "$vol:/root/.lattice")
  docker run -d --name "$name" --hostname "$host" --privileged \
    --add-host host.docker.internal:host-gateway \
    "${volargs[@]}" lattice-run-test:v2 \
    sh -c "lattice init --server http://host.docker.internal:18090 --token '$token' >/tmp/init.log 2>&1 && exec lattice up" >/dev/null
}

info "重建容器 agent 群"
mkdir -p /tmp/lattice-dbg/node1 /tmp/lattice-dbg/phone /tmp/lattice-dbg/approval
start_agent lattice-container-node container-node-1 oZSaynbji2ru7Yh6 /tmp/lattice-dbg/node1
start_agent lattice-phone-test-node 8be28bc449b4 pCKLk5HbQG5WdUDO /tmp/lattice-dbg/phone
# approval 容器首次使用时需先删掉服务端同名行（API）或直接接受其进入 pending
start_agent lattice-approval-test approval-node-1 e2e-approval-token /tmp/lattice-dbg/approval

for c in lattice-container-node lattice-phone-test-node lattice-approval-test; do
  ok=0
  for i in $(seq 1 15); do
    docker logs "$c" --since 3m 2>&1 | grep -qE "lattice started|awaiting administrator" && { ok=1; break; }
    sleep 2
  done
  [ "$ok" = 1 ] || fail "$c 未在 30s 内进入运行态（docker logs $c）"
done
info "三个容器 agent 均已启动"

# ── 6. 连通性断言 ────────────────────────────────────────────────────────────
info "运行 mesh 连通性断言"
bash "$ROOT/hack/verify-mesh.sh"

info "部署完成 ✅"
