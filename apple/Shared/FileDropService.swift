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

/// 设备间传文件（LatticeDrop）：走 overlay 的 TCP 直传。
///
/// 协议（v1，固定端口 9530）：
///   [4B 大端文件名长度][文件名 UTF-8][8B 大端文件大小][原始字节流]
/// 接收方保存到「下载/LatticeDrop」目录（iOS 暴露在文件 App）。
@MainActor
final class FileDropService: ObservableObject {
    static let shared = FileDropService()
    static let port: UInt16 = 9530

    @Published private(set) var isListening = false
    /// 最近收到/发出的传输记录（新的在前）。
    @Published var events: [DropEvent] = []

    struct DropEvent: Identifiable {
        let id = UUID()
        var name: String
        var size: Int
        var direction: String   // "in" | "out"
        var detail: String      // 对端或保存路径
        var success: Bool
    }

    private var listener: NWListener?
    private var receivedDirectory: URL {
        let base = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
        let dir = base.appendingPathComponent("LatticeDrop", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    func startListening() {
        guard listener == nil else { return }
        do {
            let params = NWParameters(tls: nil)
            params.allowLocalEndpointReuse = true
            let listener = try NWListener(using: params, on: NWEndpoint.Port(rawValue: Self.port)!)
            self.listener = listener
            listener.newConnectionHandler = { [weak self] connection in
                Task { @MainActor in self?.handle(connection) }
            }
            listener.stateUpdateHandler = { [weak self] state in
                Task { @MainActor in
                    self?.isListening = (state == .ready)
                }
            }
            listener.start(queue: DispatchQueue.global(qos: .utility))
        } catch {
            addLog("监听启动失败：\(error.localizedDescription)", success: false)
        }
    }

    func stopListening() {
        listener?.cancel()
        listener = nil
        isListening = false
    }

    func send(fileURL: URL, to host: String) {
        guard let data = try? Data(contentsOf: fileURL) else {
            addLog("读取文件失败：\(fileURL.lastPathComponent)", success: false)
            return
        }
        let name = fileURL.lastPathComponent
        var packet = Data()
        var nameLen = UInt32(name.utf8.count).bigEndian
        withUnsafeBytes(of: &nameLen) { packet.append(contentsOf: $0) }
        packet.append(contentsOf: name.utf8)
        var size = UInt64(data.count).bigEndian
        withUnsafeBytes(of: &size) { packet.append(contentsOf: $0) }
        packet.append(data)

        let connection = NWConnection(
            host: NWEndpoint.Host(host),
            port: NWEndpoint.Port(rawValue: Self.port)!,
            using: .tcp
        )
        addLog("向 \(host) 发送 \(name)（\(data.count) 字节）…", success: true)
        connection.stateUpdateHandler = { [weak self] state in
            guard let self, case .failed(let err) = state else { return }
            Task { @MainActor in
                self.addLog("发送失败：\(err.localizedDescription)", success: false)
            }
        }
        connection.start(queue: DispatchQueue.global(qos: .utility))
        connection.send(content: packet, completion: .contentProcessed { [weak self] error in
            Task { @MainActor in
                if let error {
                    self?.addLog("发送失败：\(error.localizedDescription)", success: false)
                } else {
                    self?.events.insert(DropEvent(name: name, size: data.count,
                                                  direction: "out", detail: host, success: true), at: 0)
                    self?.addLog("已送达 \(host)", success: true)
                }
                connection.cancel()
            }
        })
    }

    private func handle(_ connection: NWConnection) {
        let remote = connection.endpoint.debugDescription
        connection.start(queue: DispatchQueue.global(qos: .utility))
        receiveHeader(connection, accumulated: Data(), remote: remote)
    }

    private func receiveHeader(_ connection: NWConnection, accumulated: Data, remote: String) {
        // 头部至少 12 字节；名字长度未知，逐块收直到能解出名字+大小再收正文。
        connection.receive(minimumIncompleteLength: 1, maximumLength: 64 * 1024) { [weak self] data, _, done, error in
            guard let self else { return }
            var buffer = accumulated
            if let data { buffer.append(data) }
            if buffer.count >= 12, let parsed = Self.parseHeader(buffer) {
                let (name, size, headerLen) = parsed
                // 同批到达的正文残余不能丢：作为 body 的起始数据。
                let leftover = Data(buffer.dropFirst(headerLen))
                self.addLog("来自 \(remote) 的文件：\(name)（\(size) 字节）", success: true)
                self.receiveBody(connection, name: name, size: size, consumed: headerLen,
                                 fileData: leftover, remote: remote)
                return
            }
            if done || error != nil {
                Task { @MainActor in self.addLog("连接中断（头部不完整）", success: false) }
                connection.cancel()
                return
            }
            self.receiveHeader(connection, accumulated: buffer, remote: remote)
        }
    }

    private func receiveBody(_ connection: NWConnection, name: String, size: Int, consumed: Int,
                             fileData: Data, remote: String) {
        let need = consumed + size
        connection.receive(minimumIncompleteLength: 1, maximumLength: 1024 * 1024) { [weak self] data, _, done, error in
            guard let self else { return }
            var file = fileData
            if let data { file.append(data) }
            if file.count >= size {
                let total = self.save(name: name, bytes: file.prefix(size))
                Task { @MainActor in
                    self.events.insert(DropEvent(name: name, size: size, direction: "in",
                                                 detail: total, success: true), at: 0)
                    self.addLog("已保存：\(total)", success: true)
                }
                connection.cancel()
                return
            }
            if done || error != nil {
                Task { @MainActor in
                    self.addLog("传输中断：收到 \(file.count)/\(size) 字节", success: false)
                }
                connection.cancel()
                return
            }
            self.receiveBody(connection, name: name, size: size, consumed: consumed,
                             fileData: file, remote: remote)
        }
    }

    private func save(name: String, bytes: Data) -> String {
        var target = receivedDirectory.appendingPathComponent(name)
        var n = 1
        while FileManager.default.fileExists(atPath: target.path) {
            target = receivedDirectory.appendingPathComponent("(\(n)) \(name)")
            n += 1
        }
        try? bytes.write(to: target)
        return target.lastPathComponent
    }

    private func addLog(_ text: String, success: Bool) {
        events.insert(DropEvent(name: text, size: 0, direction: "log", detail: "", success: success), at: 0)
        if events.count > 100 { events.removeLast(events.count - 100) }
    }

    private static func parseHeader(_ buffer: Data) -> (name: String, size: Int, headerLen: Int)? {
        guard buffer.count >= 4 else { return nil }
        let nameLen = Int(buffer.prefix(4).map { $0 }.asUInt32.bigEndian)
        guard nameLen > 0, nameLen <= 1024, buffer.count >= 4 + nameLen + 8 else { return nil }
        guard let name = String(data: buffer.subdata(in: 4..<(4 + nameLen)), encoding: .utf8) else { return nil }
        let sizeBytes = buffer.subdata(in: (4 + nameLen)..<(4 + nameLen + 8))
        let size = Int(sizeBytes.map { $0 }.asUInt64.bigEndian)
        guard size >= 0 else { return nil }
        return (name, size, 4 + nameLen + 8)
    }
}

private extension Array where Element == UInt8 {
    var asUInt32: UInt32 {
        var v: UInt32 = 0
        for b in self.prefix(4) { v = (v << 8) | UInt32(b) }
        return v
    }
    var asUInt64: UInt64 {
        var v: UInt64 = 0
        for b in self.prefix(8) { v = (v << 8) | UInt64(b) }
        return v
    }
}
