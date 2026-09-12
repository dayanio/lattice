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
# Standalone connectivity E2E: boots `latticed --standalone` in a container
# plus two REAL lattice agent containers (NET_ADMIN + /dev/net/tun), then
# verifies over the actual WireGuard data plane:
#   1. peer enrollment and overlay IP allocation (10.96.0.2 / .3)
#   2. tunnel connectivity (ping between overlay IPs)
#   3. policy lifecycle: allow -> ping ok; delete -> default-deny drops
# No Kubernetes anywhere. Needs Docker + free TCP 18080.

set -o pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d /tmp/lattice-conn-e2e.XXXXXX)"
NET="lattice-conn-net"
SVC_PORT=18080
HTTP="http://127.0.0.1:${SVC_PORT}"
PASS=0
FAIL=0

log()  { echo "[$(date +%H:%M:%S)] $*"; }
ok()   { log "✅ $*"; PASS=$((PASS+1)); }
bad()  { log "❌ $*"; FAIL=$((FAIL+1)); }
die()  { log "💥 $*"; exit 1; }

RUNID="$(date +%s | tail -c 6)"
SVC="conn-latticed-${RUNID}"
NODE_A="conn-node-a-${RUNID}"
NODE_B="conn-node-b-${RUNID}"

cleanup() {
  if [ "${KEEP:-0}" = "1" ]; then log "KEEP=1 — 环境保留 ($WORK, 网络 $NET)"; return; fi
  log "Cleaning up..."
  docker rm -f "$SVC" "${NODE_A}-a" "${NODE_A}-b" >/dev/null 2>&1
  docker network rm "$NET" >/dev/null 2>&1
  log "Artifacts kept in $WORK"
}
trap cleanup EXIT

api() {
  local method=$1 path=$2 token=$3 body=${4:-}
  local auth=(-H "Authorization: Bearer ${token}")
  local ws=("-H" "X-Workspace-Id: ${WSID:-}")
  [ -z "${WSID:-}" ] && ws=()
  if [ -n "$body" ]; then
    curl -s --max-time 15 -X "$method" "${HTTP}${path}" "${auth[@]}" "${ws[@]}" \
      -H "Content-Type: application/json" -d "$body"
  else
    curl -s --max-time 15 -X "$method" "${HTTP}${path}" "${auth[@]}" "${ws[@]}"
  fi
}

jsonget() { python3 -c "import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1]))" "$2" <<< "$1" 2>/dev/null; }

log "[0/6] Building binaries..."
(cd "$REPO" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$WORK/latticed" ./cmd/latticed) || die "latticed build failed"
(cd "$REPO" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$WORK/lattice" ./cmd/lattice) || die "agent build failed"
mkdir -p "$WORK/ctx"
cp "$WORK/latticed" "$WORK/lattice" "$WORK/ctx/"
cat > "$WORK/ctx/Dockerfile" <<'DOCKER'
FROM alpine:3.19
RUN apk add -U iptables ip6tables ca-certificates
COPY latticed /usr/local/bin/latticed
COPY lattice /usr/local/bin/lattice
RUN chmod 755 /usr/local/bin/latticed /usr/local/bin/lattice && mkdir -p /data /cfg
WORKDIR /data
DOCKER
docker build -q -t lattice-conn "$WORK/ctx" >/dev/null || die "agent image build failed"
ok "binaries + image built"

log "[1/6] Starting latticed --standalone..."
docker network ls -q --filter "name=${NET}" | xargs -r docker network rm >/dev/null 2>&1
docker network create "$NET" >/dev/null || die "docker network create failed"
docker run -d --name "$SVC" --network "$NET" --hostname lattice-svc \
  -p ${SVC_PORT}:${SVC_PORT} \
  -e LATTICE_LISTEN="0.0.0.0:${SVC_PORT}" \
  -e LATTICE_SIGNALING_URL="nats://lattice-svc:4222" \
  -e LATTICE_RESYNC_INTERVAL=1s \
  lattice-conn sh -c "exec latticed --standalone --config-dir /cfg" >/dev/null || die "latticed container failed"

ready=0
for _ in $(seq 1 60); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "${HTTP}/api/v1/discovery" || true)
  [ "$code" = "200" ] && { ready=1; break; }
  sleep 1
done
[ "$ready" = "1" ] || { docker logs "$SVC" 2>&1 | tail -20; die "management API not ready"; }
ok "management plane up on :${SVC_PORT}"

log "[2/6] Login / workspace / enrollment token..."
body=$(api POST /api/v1/users/login "" '{"username":"admin","password":"123456"}')
TOKEN=$(jsonget "$body" "d['data']['token']")
[ -n "$TOKEN" ] || die "login failed: $body"

body=$(api POST /api/v1/workspaces/add "$TOKEN" '{"namespace":"e2e-net","displayName":"Conn E2E","slug":"conn-e2e","maxNodeCount":10}')
WSID=$(jsonget "$body" "d['data']['id']")
[ -n "$WSID" ] || die "workspace create failed: $body"

body=$(api POST /api/v1/token/generate "$TOKEN")
JOIN_TOKEN=$(jsonget "$body" "d['data']['token']")
[ -n "$JOIN_TOKEN" ] || die "join token failed: $body"
ok "workspace $WSID, join token issued"

log "[3/6] Launching agent containers..."
for N in a b; do
  docker run -d --name "${NODE_A}-${N}" --network "$NET" --hostname "node-${N}" \
    --cap-add NET_ADMIN --device /dev/net/tun \
    -e LATTICE_NETMAP_POLL_INTERVAL=2s \
    lattice-conn sh -c "lattice init --server http://lattice-svc:${SVC_PORT} --token '${JOIN_TOKEN}' >/dev/null && exec lattice up" >/dev/null \
    || die "agent container $N failed"
done
ok "agent containers launched"
NODE_A_C="$NODE_A-a"
NODE_B_C="$NODE_A-b"

log "[4/6] Waiting for overlay addresses..."
wait_ip() {
  local c=$1 ip=$2
  for _ in $(seq 1 60); do
    docker exec "$c" ip -4 addr show wf0 2>/dev/null | grep -q "$ip" && return 0
    sleep 2
  done
  return 1
}
wait_ip "$NODE_A_C" "10.96.0.2" && ok "node-a enrolled at 10.96.0.2" || { docker logs "$NODE_A_C" 2>&1 | tail -5; bad "node-a never got 10.96.0.2"; }
wait_ip "$NODE_B_C" "10.96.0.3" && ok "node-b enrolled at 10.96.0.3" || { docker logs "$NODE_B_C" 2>&1 | tail -5; bad "node-b never got 10.96.0.3"; }

log "[5/6] Connectivity over the tunnel..."
body=$(api POST "/api/v1/policies/create" "$TOKEN" '{"name":"allow-mesh","action":"Allow","policyTypes":["Ingress","Egress"],"network":"e2e-net","peerSelector":{},"ingress":[{"from":[{"ipBlock":{"cidr":"10.96.0.0/24"}}]}],"egress":[{"to":[{"ipBlock":{"cidr":"10.96.0.0/24"}}]}]}')
code=$(jsonget "$body" "d['code']"); [ "$code" = "200" ] || bad "allow policy apply: $body"

ping_ok=0
for _ in $(seq 1 30); do
  if docker exec "$NODE_A_C" ping -c 2 -W 2 10.96.0.3 >/dev/null 2>&1; then ping_ok=1; break; fi
  sleep 2
done
[ "$ping_ok" = "1" ] && ok "ping 10.96.0.2 -> 10.96.0.3 over the tunnel" || bad "ping a->b failed"

log "[6/6] Policy enforcement..."
api DELETE "/api/v1/policies/allow-mesh" "$TOKEN" > /dev/null
blocked=0
for _ in $(seq 1 30); do
  if ! docker exec "$NODE_A_C" ping -c 2 -W 2 10.96.0.3 >/dev/null 2>&1; then blocked=1; break; fi
  sleep 2
done
[ "$blocked" = "1" ] && ok "after allow-policy removal, default-deny drops the traffic" || bad "traffic still flows after policy removal"

echo
echo "=================================="
echo " Connectivity E2E: PASS=$PASS FAIL=$FAIL"
echo "=================================="
[ "$FAIL" = "0" ]
