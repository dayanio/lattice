#!/bin/bash
# test_join_logic.sh — 编译并运行入网逻辑（邀请链接解析、名称规范化、失败原因归类）的检查。
# 这些代码只依赖 Foundation，所以不需要 Xcode 测试 target。
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
# 多文件编译时，顶层语句必须放在名为 main.swift 的文件里。
cp Tests/JoinLogicTests.swift "$TMP/main.swift"
swiftc -o "$TMP/join_logic_tests" Shared/JoinPayload.swift "$TMP/main.swift"
"$TMP/join_logic_tests"
