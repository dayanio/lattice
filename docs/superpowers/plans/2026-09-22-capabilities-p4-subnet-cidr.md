# 客户端能力补全 P4（广播子网路由 CIDR 编辑器）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把网络设置页的"广播子网路由"从 no-op 开关变成真能增删 CIDR 并调 `advertised-routes` API 的页内编辑器。

**Architecture:** 纯逻辑（CIDR 校验/规范化 + 本机子网探测）放 Foundation-only 的 `SubnetRoutes.swift` 走 TDD；UI 沿用退出节点选择的页内切换模式（`body` 按 `showingSubnetEditor` 分支）。声明链路（API → netmap `AllowedIPs` → 对端路由）服务端已通，本机转发能力二期另做，UI 明示。

**Tech Stack:** Swift/SwiftUI、Network framework（`IPv4Address`）、`apple/Scripts/test_apple_logic.sh`。

**Spec:** `docs/superpowers/specs/2026-09-22-mac-client-capabilities-design.md` §五

## Global Constraints

同 P1+P2 计划的 Global Constraints（单 commit、`-s`、无 Co-Authored-By、lint、中文文案、BUILD 命令不带 CODE_SIGNING_ALLOWED=NO）。本任务不动 Go，无需重 bind。

## File Structure

| 文件 | 职责 | 操作 |
|---|---|---|
| `apple/Shared/SubnetRoutes.swift` | CIDR 校验/规范化、本机子网建议 | 新增 |
| `apple/Tests/AppleLogicTests.swift` | SubnetRoutes 检查 | 修改 |
| `apple/Scripts/test_apple_logic.sh` | 编译列表加 SubnetRoutes.swift | 修改 |
| `apple/LatticeMac/NetworkPages.swift` | 行改 chevron、页内编辑器、删 no-op toggle | 修改 |

---

### Task 1: 纯逻辑 SubnetRoutes（TDD）

- [ ] **Step 1: 测试脚本编译列表加文件**（`test_apple_logic.sh`）

```bash
swiftc -o "$TMP/apple_logic_tests" Shared/JoinPayload.swift Shared/TunnelCore.swift Shared/SubnetRoutes.swift LatticeMac/PanelRoute.swift "$TMP/main.swift"
```

- [ ] **Step 2: 写失败测试**（`AppleLogicTests.swift`，PanelPage 块前插入）

```swift
// MARK: SubnetRoutes

do {
    eq(SubnetRoute.normalized("192.168.1.5/24"), "192.168.1.0/24", "host bits are masked")
    eq(SubnetRoute.normalized(" 10.0.0.0/8 "), "10.0.0.0/8", "trimmed")
    eq(SubnetRoute.normalized("172.16.0.0/12"), "172.16.0.0/12", "rfc1918 kept")
    eq(SubnetRoute.normalized("fd00::/8"), "fd00::/8", "ipv6 kept")
    check(SubnetRoute.normalized("192.168.1.5") == nil, "a bare address is not a route")
    check(SubnetRoute.normalized("192.168.1.5/33") == nil, "prefix > 32 rejected")
    check(SubnetRoute.normalized("abc/24") == nil, "garbage rejected")
    check(SubnetRoute.normalized("0.0.0.0/0") == nil, "default route is the exit node's business")
    check(SubnetRoute.normalized("::/0") == nil, "v6 default rejected too")
}
```

- [ ] **Step 3: 跑 LOGIC 确认失败**（编译错误：SubnetRoutes 未定义）

- [ ] **Step 4: 实现 `SubnetRoutes.swift`**

```swift
// Copyright 2026 The Lattice Authors, Inc. (Apache 2.0 13 行头，同其他文件)

import Foundation
import Network

/// 输入侧的子网路由 CIDR 校验与规范化。服务端还会再校验一次；这里负责
/// 让用户在保存前就拿到干净的输入。default 路由（0.0.0.0/0、::/0）属于
/// 退出节点，从这里拒绝。
enum SubnetRoute {
    /// 规范化一个 CIDR：去空白、补全大小写、IPv4 掩掉主机位；不合法或
    /// default 路由返回 nil。
    static func normalized(_ raw: String) -> String? {
        let s = raw.trimmingCharacters(in: .whitespaces)
        let parts = s.split(separator: "/", omittingEmptySubsequences: false)
        guard parts.count == 2, let prefix = Int(parts[1]) else { return nil }
        let addr = String(parts[0])
        if let v4 = IPv4Address(addr) {
            guard (0...32).contains(prefix) else { return nil }
            if prefix == 0 { return nil } // 0.0.0.0/0 = exit node
            let octets = v4.rawValue.map { Int($0) }
            var net = [Int](repeating: 0, count: 4)
            for i in 0..<4 {
                let shift = max(0, 8 * (i + 1) - prefix)
                net[i] = shift >= 8 ? 0 : octets[i] & (~0 << shift & 0xff)
            }
            return net.map(String.init).joined(separator: ".") + "/\(prefix)"
        }
        if IPv6Address(addr) != nil {
            guard (0...128).contains(prefix) else { return nil }
            if prefix == 0 { return nil } // ::/0 = exit node
            return "\(addr)/\(prefix)"
        }
        return nil
    }

    /// 本机主接口的 IPv4 子网（按接口掩码算网络地址），作为编辑器的默认
    /// 建议；拿不到返回 nil。
    static func localSuggestion() -> String? {
        var ifap: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifap) == 0, let first = ifap else { return nil }
        defer { freeifaddrs(ifap) }

        var ptr: UnsafeMutablePointer<ifaddrs>? = first
        while let p = ptr {
            defer { ptr = p.pointee.ifa_next }
            guard let sa = p.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET),
                  String(cString: p.pointee.ifa_name) != "lo0",
                  let maskPtr = p.pointee.ifa_netmask else { continue }
            let addr = sa.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee.sin_addr }
            let mask = maskPtr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee.sin_addr }
            let a = withUnsafeBytes(of: addr.s_addr) { Array($0) }
            let m = withUnsafeBytes(of: mask.s_addr) { Array($0) }
            var prefix = 0
            for byte in m { while byte >> (7 - prefix % 8) & 1 == 1 && prefix < 32 { prefix += 1 } }
            let net = zip(a, m).map { $0 & $1 }
            // 只要 /24 以内的常规局域网段
            guard (1...24).contains(prefix) else { continue }
            return net.map(String.init).joined(separator: ".") + "/\(prefix)"
        }
        return nil
    }
}
```

- [ ] **Step 5: 跑 LOGIC 确认通过**

Run: `bash Scripts/test_apple_logic.sh` → `apple logic: all checks passed`

### Task 2: 页内编辑器（NetworkSettingsView）

- [ ] **Step 1: 状态与 load()**：加 `@State private var showingSubnetEditor = false`、`@State private var myAdvertisedRoutes: [String] = []`；编辑器草稿态 `draftRoutes/newRoute/editorError`。`load()` 里 `if let mine = peers.first(where: { $0.name == selfName })` 分支同时填 `myAdvertisedRoutes = mine.advertisedRoutes`；删除 `advertisingSubnet` 与 `toggleAdvertiseSubnet`（no-op）。
- [ ] **Step 2: 行改 chevron**："广播子网路由"行 `trailing` 改 `Text("›")`，desc 改 `advertisedDesc`（`myAdvertisedRoutes` 空显示"未广播任何子网 · 把本机所在局域网开放给 workspace 里的其它设备"，否则"已广播 \(count) 条：…列表"），点击 `showingSubnetEditor = true`。
- [ ] **Step 3: body 分支**加 `else if showingSubnetEditor { subnetEditor }`；`subnetEditor` 用 `PageHeader(title: "广播子网路由")` + 路由列表（行：CIDR 等宽 + 删除按钮）+ `TextField("192.168.1.0/24")` + "添加"按钮 + 建议行（`SubnetRoute.localSuggestion()` 存在时"使用本机子网 x.x.x.x/24"）+ 提示文案（"其他设备把本机选为路由提供方后即可使用；本机侧转发能力开发中"）+ 保存按钮（`setAdvertisedRoutes(selfName, routes: draftRoutes)`，成功 `showingSubnetEditor = false; await load()`，失败 `editorError`）。
- [ ] **Step 4: BUILD + LOGIC + 提交**

```bash
git add apple/Shared/SubnetRoutes.swift apple/Tests/AppleLogicTests.swift \
  apple/Scripts/test_apple_logic.sh apple/LatticeMac/NetworkPages.swift
git commit -s -m "feat(apple): subnet route editor with CIDR validation"
```

## Self-Review

- Spec §五一期的四点：编辑器/校验（default 拒绝）/保存调 API/诚实标注 → Task 1、2 覆盖；转发二期不混入。
- 类型一致：`SubnetRoute.normalized(_:) -> String?`、`localSuggestion() -> String?`。
- 人工验证项：建议值在真实 en0 上的正确性（跑在真机上看）。
