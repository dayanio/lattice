#!/usr/bin/env bash
# verify-mesh.sh — 测试环境连通性断言（部署后/验收时运行）
#
# 断言：
#   1. discovery 可达且广播的 NATS 地址指向本机当前 LAN IP
#   2. run-test workspace 内 container-node-1 与 phone-test agent 已注册
#   3. 两个容器 agent 经 mesh 数据面互 ping（0 丢包）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
API="http://127.0.0.1:18090"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-123456}"
WORKSPACE_ID="${WORKSPACE_ID:-f4be5040-4087-4d88-9d94-1b712f75c0f5}"

pass() { echo -e "\033[32m[PASS]\033[0m $*"; }
fail() { echo -e "\033[31m[FAIL]\033[0m $*" >&2; exit 1; }

# ── 1. discovery ─────────────────────────────────────────────────────────────
LAN_IP="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)"
[ -n "$LAN_IP" ] || fail "无法探测 LAN IP"
code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/v1/discovery")
[ "$code" = "200" ] || fail "discovery HTTP $code"
discovered=$(curl -s "$API/api/v1/discovery" | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['nats_url'])")
[[ "$discovered" == *"$LAN_IP:"* ]] || fail "discovery nats_url($discovered) 未指向本机 $LAN_IP"
pass "discovery 可达且广播 $discovered"

# ── 2. peer 注册 ─────────────────────────────────────────────────────────────
TOKEN=$(curl -s -X POST "$API/api/v1/users/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['data']['token'] if isinstance(d.get('data'),dict) else d['data'])")
[ -n "$TOKEN" ] || fail "admin 登录失败"

PEERS_JSON=$(curl -s "$API/api/v1/peers/list?page=1&pageSize=50" \
  -H "Authorization: Bearer $TOKEN" -H "X-Workspace-Id: $WORKSPACE_ID")
python3 - "$PEERS_JSON" <<'EOF'
import sys, json
d = json.loads(sys.argv[1])['data']
names = {p.get('name') for p in (d.get('list') or [])}
missing = {'container-node-1', '8be28bc449b4'} - names
if missing:
    print(f"FAIL: 未注册的容器 agent: {missing}", file=sys.stderr)
    sys.exit(1)
EOF
pass "两个容器 agent 均已在 run-test 注册"

# ── 3. mesh 数据面互 ping ────────────────────────────────────────────────────
NODE1_IP=""; NODE1_NAME=""; PHONE_IP=""; PHONE_NAME=""
while read -r name addr; do
  case "$name" in
    container-node-1) NODE1_NAME="$name"; NODE1_IP="$addr" ;;
    8be28bc449b4)     PHONE_NAME="$name"; PHONE_IP="$addr" ;;
  esac
done <<PEERS
$(echo "$PEERS_JSON" | python3 -c "
import sys, json
d = json.load(sys.stdin)['data']
for p in (d.get('list') or []):
    if p.get('name') in ('container-node-1', '8be28bc449b4') and p.get('address'):
        print(p['name'], p['address'])
")
PEERS
[ -n "$NODE1_IP" ] && [ -n "$PHONE_IP" ] || fail "应有两个容器 agent 的地址（node1=$NODE1_IP phone=$PHONE_IP）"

docker exec lattice-container-node ping -c 2 -W 2 "$PHONE_IP" >/dev/null \
  || fail "mesh ping 失败：container-node-1 → $PHONE_NAME($PHONE_IP)"
pass "mesh ping container-node-1 → $PHONE_NAME($PHONE_IP) 通"
docker exec lattice-phone-test-node ping -c 2 -W 2 "$NODE1_IP" >/dev/null \
  || fail "mesh ping 失败：$PHONE_NAME($PHONE_IP) → container-node-1($NODE1_IP)"
pass "mesh ping $PHONE_NAME($PHONE_IP) → container-node-1($NODE1_IP) 通"

echo -e "\033[32m[verify-mesh] 全部断言通过 ✅\033[0m"
