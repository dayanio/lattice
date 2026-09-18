#!/bin/bash
# build_install.sh — 构建 iOS App 并安装到已连接的真机，启动并抓取启动日志。
# 用法：apple/Scripts/build_install.sh [设备名过滤，默认 iPhone]
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

FILTER="${1:-iPhone}"
SCHEME="Lattice"
DERIVED="${DERIVED:-build}"
BUNDLE_ID="${BUNDLE_ID:-io.lattice.Lattice}"

info() { echo -e "\033[32m[build_install]\033[0m $1"; }
fail() { echo -e "\033[31m[build_install][FAIL]\033[0m $*" >&2; exit 1; }

info "xcodebuild $SCHEME → generic/platform=iOS"
BUILD_LOG="$DERIVED/build_install.log"
mkdir -p "$DERIVED"
xcodebuild -project LatticeApple.xcodeproj -scheme "$SCHEME" \
  -configuration Debug -destination 'generic/platform=iOS' \
  -derivedDataPath "$DERIVED" build > "$BUILD_LOG" 2>&1 \
  || { tail -20 "$BUILD_LOG" >&2; fail "xcodebuild 失败（详见 $BUILD_LOG）"; }
grep -q "BUILD SUCCEEDED" "$BUILD_LOG" || fail "未见 BUILD SUCCEEDED"
APP="$DERIVED/Build/Products/Debug-iphoneos/Lattice.app"
[ -d "$APP" ] || fail "找不到构建产物 $APP"

info "查找已连接设备（过滤：$FILTER）"
DEV=$(xcrun devicectl list devices 2>/dev/null | grep -i "$FILTER" | grep -i available | head -1 | awk '{print $NF}')
[ -n "$DEV" ] || fail "未找到已连接的设备——请解锁并信任后重试"
info "目标设备 $DEV"

info "安装 $APP"
xcrun devicectl device install app --device "$DEV" "$APP" | tail -2

info "启动 $BUNDLE_ID"
xcrun devicectl device process launch --console --device "$DEV" "$BUNDLE_ID" &
LAUNCH_PID=$!
sleep 15
kill "$LAUNCH_PID" 2>/dev/null || true

info "完成。启动日志已在前台输出（15s 采样）。"
info "验收提示：设备列表节点应显示在线而非「连接中」；设置页应出现「设备身份」节。"
