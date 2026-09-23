// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import Foundation
import Network

/// 输入侧的子网路由 CIDR 校验与规范化。服务端还会再校验一次；这里负责
/// 让用户在保存前就拿到干净的输入。default 路由（0.0.0.0/0、::/0）属于
/// 出口节点，从这里拒绝。
enum SubnetRoute {
    /// 规范化一个 CIDR：去空白、校验前缀长度、IPv4 掩掉主机位得到网络
    /// 地址；不合法或 default 路由返回 nil。
    static func normalized(_ raw: String) -> String? {
        let s = raw.trimmingCharacters(in: .whitespaces)
        let parts = s.split(separator: "/", omittingEmptySubsequences: false)
        guard parts.count == 2, let prefix = Int(parts[1]) else { return nil }
        let addr = String(parts[0])
        if let v4 = IPv4Address(addr) {
            guard (1...32).contains(prefix) else { return nil }
            let octets = withUnsafeBytes(of: v4) { Array($0) }.map { Int($0) } // 网络字节序
            var net = [Int](repeating: 0, count: 4)
            for i in 0..<4 {
                let shift = max(0, 8 * (i + 1) - prefix)
                net[i] = shift >= 8 ? 0 : octets[i] & (~0 << shift & 0xff)
            }
            return net.map(String.init).joined(separator: ".") + "/\(prefix)"
        }
        if IPv6Address(addr) != nil {
            guard (1...128).contains(prefix) else { return nil }
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
            for byte in m.prefix(4) {
                var bits = 0
                var b = byte
                while bits < 8 && b & 0x80 == 0x80 { b <<= 1; bits += 1 }
                prefix += bits
                if bits < 8 { break }
            }
            let net = zip(a, m).map { $0 & $1 }
            // 只要常规局域网段（/8…/24），更长的多在虚拟网卡上
            guard (8...24).contains(prefix) else { continue }
            return net.map(String.init).joined(separator: ".") + "/\(prefix)"
        }
        return nil
    }
}
