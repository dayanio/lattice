#!/bin/bash
# test_apple_logic.sh — 编译并运行 Apple 端纯逻辑的检查：邀请链接解析、名称规范化、失败原因归类、
# 节点列表合并、隧道节点 JSON 解码、面板页面路由。
# 这些代码只依赖 Foundation，所以不需要 Xcode 测试 target。
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
# 多文件编译时，顶层语句必须放在名为 main.swift 的文件里。
cp Tests/AppleLogicTests.swift "$TMP/main.swift"
swiftc -o "$TMP/apple_logic_tests" Shared/JoinPayload.swift Shared/TunnelCore.swift LatticeMac/PanelRoute.swift "$TMP/main.swift"
"$TMP/apple_logic_tests"
