#!/bin/bash
# ip-drift-guard.sh — 测试环境 LAN IP 漂移守护（LaunchAgent 每 5 分钟调用）
# 检测 LAN IP 变化 → 自动修正 signaling-url → 重部署测试环境（服务端+容器群+断言）。
# 日志：/tmp/lattice-run-test/ip-drift-guard.log
LOG="/tmp/lattice-run-test/ip-drift-guard.log"
CFG="/tmp/lattice-run-test/cfg/lattice.yaml"
DEPLOY="/Users/francis/workspc/lattice/hack/deploy-test.sh"
ts() { date '+%F %T'; }

[ -f "$CFG" ] || exit 0
[ -f "$DEPLOY" ] || exit 0

CUR="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null)"
[ -n "$CUR" ] || exit 0
CFGIP="$(grep -o 'nats://[0-9.]*:4222' "$CFG" 2>/dev/null | head -1 | sed 's|nats://||; s|:4222||')"
[ -n "$CFGIP" ] || exit 0

[ "$CUR" = "$CFGIP" ] && exit 0

echo "$(ts) [drift] LAN IP $CFGIP → $CUR，自动重部署测试环境" >> "$LOG"
sed -i '' "s|nats://[0-9.]*:4222|nats://$CUR:4222|" "$CFG"
if bash "$DEPLOY" >> "$LOG" 2>&1; then
  echo "$(ts) [drift] 重部署完成，断言通过" >> "$LOG"
else
  echo "$(ts) [drift] 重部署失败——查看上方日志" >> "$LOG"
fi
