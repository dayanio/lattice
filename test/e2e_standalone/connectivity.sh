#!/usr/bin/env bash
# Copyright 2026 The Lattice Authors, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Standalone connectivity E2E: boots `latticed --standalone` on the host and
# two REAL lattice agents inside Docker containers (NET_ADMIN + /dev/net/tun),
# then verifies — over the actual WireGuard data plane:
#   1. peer enrollment and overlay IP allocation (10.96.0.2 / .3)
#   2. tunnel connectivity (ping between overlay IPs)
#   3. policy distribution + enforcement (allow → ping ok; allow removed →
#      default-deny tail drops the traffic)
# No Kubernetes anywhere.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d /tmp/lattice-conn-e2e.XXXXXX)"
NET="lattice-e2e-net"
SVC_PORT=18080
NATS_PORT=4222
HTTP="http://127.0.0.1:${SVC_PORT}"
PASS=0
FAIL=0
LATTICED_PID=""

log()  { echo "[$(date +%H:%M:%S)] $*"; }
ok()   { log "✅ $*"; PASS=$((PASS+1)); }
bad()  { log "❌ $*"; FAIL=$((FAIL+1)); }
die()  { log "💥 $*"; exit 1; }

cleanup() {
  if [ "${KEEP:-0}" = "1" ]; then log "KEEP=1 — 环境保留 ($WORK)"; return; fi
  log "Cleaning up..."
  docker rm -f lattice-node-a lattice-node-b lattice-svc >/dev/null 2>&1
  docker network ls -q --filter "name=${NET}" | xargs -r docker network rm >/dev/null 2>&1

  log "Artifacts kept in $WORK"
}
trap cleanup EXIT

step_wait_http() {
  for _ in $(seq 1 60); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:${SVC_PORT}/api/v1/discovery" || true)
    [ "$code" = "200" ] && return 0
    sleep 1
  done
  return 1
}

api() { # method path token [body] -> body
  local method=$1 path=$2 token=$3 body=${4:-}
  if [ -n "$body" ]; then
    curl -s --max-time 15 -X "$method" "http://127.0.0.1:${SVC_PORT}${path}" \
      -H "Authorization: Bearer ${token}" -H "Content-Type: application/json" \
      -H "X-Workspace-Id: ${WSID:-}" -d "$body"
  else
    curl -s --max-time 15 -X "$method" "http://127.0.0.1:${SVC_PORT}${path}" \
      -H "Authorization: Bearer ${token}" -H "X-Workspace-Id: ${WSID:-}"
  fi
}

api_delete() { # path token
  docker exec "$SVC" wget -qO- -T 15 --method=DELETE \
    --header "Authorization: Bearer ${2}" --header "X-Workspace-Id: ${WSID}" \
    "http://127.0.0.1:${SVC_PORT}${1}"
}

jsonget() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$2" <<< "$1" 2>/dev/null; }

# ── 0. Build ────────────────────────────────────────────────────────────────
log "[0/6] Building binaries..."
(cd "$REPO" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$WORK/latticed" ./cmd/latticed) || die "latticed build failed"
(cd "$REPO" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$WORK/lattice-linux-arm64" ./cmd/lattice) || die "agent build failed"
chmod 755 "$WORK/lattice-linux-arm64"
log "binaries built"

# ── 1. Docker network + management plane ───────────────────────────────────
docker network ls -q --filter "name=${NET}" | xargs -r docker network rm >/dev/null 2>&1
docker network create "$NET" >/dev/null || die "docker network create failed"
GW=$(docker network inspect "$NET" -f '{{(index .IPAM.Config 0).Gateway}}')
log "[1/6] Building control-plane image and starting latticed --standalone..."
mkdir -p "$WORK/ctx/cfg" "$WORK/ctx/data"
cp "$WORK/latticed" "$WORK/ctx/latticed"
cat > "$WORK/ctx/Dockerfile.svc" <<'DOCKER'
FROM alpine:3.19
RUN apk add -U iptables ip6tables ca-certificates
COPY latticed /usr/local/bin/latticed
RUN chmod 755 /usr/local/bin/latticed && mkdir -p /data /cfg
WORKDIR /data
DOCKER
docker build -q -t lattice-svc-e2e "$WORK/ctx" -f "$WORK/ctx/Dockerfile.svc" >/dev/null || die "svc image build failed"
docker run -d --name lattice-svc --network "$NET" --hostname lattice-svc \
  -p ${SVC_PORT}:${SVC_PORT} \
  -e LATTICE_LISTEN="0.0.0.0:${SVC_PORT}" \
  -e LATTICE_SIGNALING_URL="nats://lattice-svc:${NATS_PORT}" \
  -e LATTICE_RESYNC_INTERVAL=1s \
  lattice-svc-e2e sh -c "cd /data && exec latticed --standalone --config-dir /cfg" > /dev/null || die "latticed container failed"
SVC="lattice-svc"
step_wait_http() {
  for _ in $(seq 1 60); do
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "http://127.0.0.1:${SVC_PORT}/api/v1/discovery" || true)
    [ "$code" = "200" ] && return 0
    sleep 1
  done
  return 1
}
step_wait_http || { docker logs lattice-svc 2>&1 | tail -20; die "management API not ready"; }
ok "management plane up (container lattice-svc)"

# ── 2. Login + workspace + enrollment token ────────────────────────────────
log "[2/6] Login / workspace / enrollment token..."
TOKEN=""
for _ in $(seq 1 5); do
  body=$(api POST /api/v1/users/login "" '{"username":"admin","password":"123456"}')
  TOKEN=$(jsonget "$body" "d['data']['token']")
  [ -n "$TOKEN" ] && break
  sleep 1
done
[ -n "$TOKEN" ] || die "login failed: $body"

body=$(api POST /api/v1/workspaces/add "$TOKEN" '{"namespace":"e2e-net","displayName":"Conn E2E","slug":"conn-e2e-$$_$RANDOM","maxNodeCount":10}')
WSID=$(jsonget "$body" "d['data']['id']")
[ -n "$WSID" ] || die "workspace create failed: $body"

body=$(curl -s --max-time 15 -X POST "http://127.0.0.1:${SVC_PORT}/api/v1/token/generate" \
  -H "Authorization: Bearer ${TOKEN}" -H "X-Workspace-Id: ${WSID}")
JOIN_TOKEN=$(jsonget "$body" "d['data']['token']")
[ -n "$JOIN_TOKEN" ] || die "join token failed: $body"
ok "workspace $WSID, join token issued"

# ── 3. Launch two real agent instances ─────────────────────────────────────
log "[3/6] Building agent image and launching containers..."
mkdir -p "$WORK/ctx"
cp "$WORK/lattice-linux-arm64" "$WORK/ctx/lattice"
cat > "$WORK/ctx/Dockerfile" <<'DOCKER'
FROM alpine:3.19
RUN apk add -U iptables ip6tables ca-certificates
COPY lattice /usr/local/bin/lattice
RUN chmod 755 /usr/local/bin/lattice
DOCKER
docker build -q -t lattice-agent-e2e "$WORK/ctx" >/dev/null || die "agent image build failed"
for N in a b; do
  docker run -d --name "lattice-node-${N}" --network "$NET" --hostname "node-${N}" \
    --cap-add NET_ADMIN --device /dev/net/tun \
    -e LATTICE_NETMAP_POLL_INTERVAL=2s \
    lattice-agent-e2e sh -c "lattice init --server http://lattice-svc:${SVC_PORT} --token '${JOIN_TOKEN}' >/dev/null && \
      exec lattice up" > /dev/null || die "agent container $N failed to start"
done
ok "agent containers launched"

# ── 4. Wait for overlay IP allocation ──────────────────────────────────────
log "[4/6] Waiting for overlay addresses..."
wait_ip() { # container expected_ip
  local c=$1 ip=$2
  for _ in $(seq 1 60); do
    if docker exec "$c" ip -4 addr show wf0 2>/dev/null | grep -q "$ip"; then return 0; fi
    sleep 2
  done
  return 1
}
wait_ip lattice-node-a "10.96.0.2" && ok "node-a enrolled at 10.96.0.2" || { docker logs lattice-node-a 2>&1 | tail -5; bad "node-a never got 10.96.0.2"; }
wait_ip lattice-node-b "10.96.0.3" && ok "node-b enrolled at 10.96.0.3" || { docker logs lattice-node-b 2>&1 | tail -5; bad "node-b never got 10.96.0.3"; }

# ── 5. Connectivity: allow policy → ping OK ────────────────────────────────
log "[5/6] Connectivity over the tunnel..."
body=$(api POST "/api/v1/policies/create" "$TOKEN" '{"name":"allow-mesh","action":"Allow","policyTypes":["Ingress","Egress"],"network":"e2e-net","peerSelector":{},"ingress":[{"from":[{"ipBlock":{"cidr":"10.96.0.0/24"}}]}],"egress":[{"to":[{"ipBlock":{"cidr":"10.96.0.0/24"}}]}]}')
code=$(jsonget "$body" "d['code']"); [ "$code" = "200" ] || bad "allow policy apply: $body"

ping_ok=0
for _ in $(seq 1 30); do
  if docker exec lattice-node-a ping -c 2 -W 2 10.96.0.3 >/dev/null 2>&1; then ping_ok=1; break; fi
  sleep 2
done
[ "$ping_ok" = "1" ] && ok "ping 10.96.0.2 → 10.96.0.3 over the tunnel" || bad "ping a→b failed"

# ── 6. Policy enforcement: remove allow → default-deny drops traffic ───────
log "[6/6] Policy enforcement..."
api_delete "/api/v1/policies/allow-mesh" "$TOKEN" > /dev/null
blocked=0
for _ in $(seq 1 30); do
  if ! docker exec lattice-node-a ping -c 2 -W 2 10.96.0.3 >/dev/null 2>&1; then blocked=1; break; fi
  sleep 2
done
[ "$blocked" = "1" ] && ok "after allow-policy removal, default-deny drops the traffic" || bad "traffic still flows after policy removal"

echo
echo "══════════════════════════════════"
echo " Connectivity E2E: PASS=$PASS FAIL=$FAIL"
echo "══════════════════════════════════"
[ "$FAIL" = "0" ]
