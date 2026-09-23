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

import SwiftUI

/// A persisted AI conversation.
/// 单条聊天消息："user" | "assistant" | "tool" | "error"。
struct ChatDisplayMessage: Identifiable, Equatable, Codable {
    var id = UUID()
    let role: String
    var text: String
}

struct ChatSession: Identifiable, Codable, Equatable {
    var id = UUID()
    var title: String
    var updatedAt: Date = Date()
    var messages: [ChatDisplayMessage] = []
    /// LLM context for the control plane (role/content pairs).
    var history: [ChatAPIMessage] = []

    static func == (lhs: ChatSession, rhs: ChatSession) -> Bool { lhs.id == rhs.id }
}

/// Loads/saves sessions under Application Support/Lattice/chats.json.
final class ChatSessionStore {
    static let shared = ChatSessionStore()

    private let fileURL: URL

    private init() {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Lattice", isDirectory: true)
        try? FileManager.default.createDirectory(at: base, withIntermediateDirectories: true)
        fileURL = base.appendingPathComponent("chats.json")
    }

    func load() -> [ChatSession] {
        guard let data = try? Data(contentsOf: fileURL),
              let sessions = try? JSONDecoder().decode([ChatSession].self, from: data) else {
            return []
        }
        return sessions.sorted { $0.updatedAt > $1.updatedAt }
    }

    func save(_ sessions: [ChatSession]) {
        guard let data = try? JSONEncoder().encode(sessions) else { return }
        try? data.write(to: fileURL, options: .atomic)
    }
}

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var sessions: [ChatSession] = []
    @Published var currentID: UUID?
    /// Working copy of the active conversation (streaming mutates it live).
    @Published var messages: [ChatDisplayMessage] = []
    @Published var input = ""
    @Published var isStreaming = false
    @Published var lastMessageID: UUID?

    let quickPrompts = [
        "列出网络里的所有设备",
        "检查当前网络有哪些安全风险",
        "总结现在生效的访问策略",
    ]

    private var history: [ChatAPIMessage] = []
    private let store = ChatSessionStore.shared
    private var streamTask: Task<Void, Never>?

    var canSend: Bool {
        !isStreaming && !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var currentTitle: String {
        sessions.first { $0.id == currentID }?.title ?? "新对话"
    }

    init() {
        sessions = store.load()
    }

    // MARK: Session management

    func loadStore() {
        if sessions.isEmpty {
            sessions = store.load()
        }
    }

    /// Starts a fresh conversation. An empty current session is dropped.
    func newSession() {
        finishStreaming()
        persistCurrent()
        if let id = currentID,
           let session = sessions.first(where: { $0.id == id }),
           session.messages.isEmpty {
            sessions.removeAll { $0.id == id }
            persistSessions()
        }
        currentID = nil
        messages = []
        history = []
        input = ""
    }

    func select(_ id: UUID) {
        guard id != currentID else { return }
        finishStreaming()
        persistCurrent()
        currentID = id
        if let session = sessions.first(where: { $0.id == id }) {
            messages = session.messages
            history = session.history
        } else {
            messages = []
            history = []
        }
    }

    func delete(_ id: UUID) {
        if id == currentID {
            finishStreaming()
            currentID = nil
            messages = []
            history = []
        }
        sessions.removeAll { $0.id == id }
        persistSessions()
    }

    // MARK: Sending

    func send(_ raw: String) {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !isStreaming else { return }
        input = ""

        ensureCurrentSession(titledFrom: text)
        messages.append(ChatDisplayMessage(role: "user", text: text))
        let assistantID = appendAssistantBubble()
        isStreaming = true
        lastMessageID = assistantID

        streamTask = Task { [weak self] in
            guard let self else { return }
            do {
                try await LatticeAPI.shared.streamChat(
                    message: text,
                    history: history
                ) { [weak self] event in
                    self?.handle(event)
                }
            } catch {
                self.replaceEmptyAssistant(with: ("error", "连接失败: \(error.localizedDescription)"))
            }
            self.recordHistory(userText: text, assistantID: assistantID)
            self.touchSession()
            self.persistCurrent()
            self.isStreaming = false
            self.lastMessageID = self.messages.last?.id
        }
    }

    func finishStreaming() {
        streamTask?.cancel()
        streamTask = nil
        isStreaming = false
    }

    // MARK: Internals

    /// Guarantees a live session: creates one titled from the first message.
    private func ensureCurrentSession(titledFrom text: String) {
        if currentID == nil || sessions.first(where: { $0.id == currentID }) == nil {
            let session = ChatSession(title: String(text.prefix(24)))
            sessions.insert(session, at: 0)
            currentID = session.id
        }
    }

    private func appendAssistantBubble() -> UUID {
        let bubble = ChatDisplayMessage(role: "assistant", text: "")
        messages.append(bubble)
        return bubble.id
    }

    private func handle(_ event: ChatEvent) {
        switch event {
        case .token(let content):
            if let idx = messages.lastIndex(where: { $0.role == "assistant" }) {
                messages[idx].text += content
                lastMessageID = messages[idx].id
            }
        case .toolUse(let tool):
            let chip = ChatDisplayMessage(role: "tool", text: tool)
            if let idx = messages.lastIndex(where: { $0.role == "assistant" }) {
                messages.insert(chip, at: idx)
            } else {
                messages.append(chip)
            }
            lastMessageID = chip.id
        case .error(let message):
            replaceEmptyAssistant(with: ("error", message))
        case .done:
            break
        }
    }

    private func replaceEmptyAssistant(with message: (role: String, text: String)) {
        if let idx = messages.lastIndex(where: { $0.role == "assistant" && $0.text.isEmpty }) {
            messages[idx] = ChatDisplayMessage(id: messages[idx].id, role: message.role, text: message.text)
        } else {
            messages.append(ChatDisplayMessage(role: message.role, text: message.text))
        }
        lastMessageID = messages.last?.id
    }

    private func recordHistory(userText: String, assistantID: UUID) {
        let assistantText = messages.first(where: { $0.id == assistantID })?.text ?? ""
        history.append(ChatAPIMessage(role: "user", content: userText))
        if !assistantText.isEmpty {
            history.append(ChatAPIMessage(role: "assistant", content: String(assistantText.suffix(2000))))
        }
        if history.count > 20 {
            history = Array(history.suffix(20))
        }
    }

    /// Writes the working copy back into its session and bumps recency.
    private func persistCurrent() {
        guard let id = currentID, let idx = sessions.firstIndex(where: { $0.id == id }) else { return }
        sessions[idx].messages = messages
        sessions[idx].history = history
        sessions[idx].updatedAt = Date()
        persistSessions()
    }

    private func touchSession() {
        guard let id = currentID else { return }
        if let idx = sessions.firstIndex(where: { $0.id == id }) {
            sessions[idx].updatedAt = Date()
            // Keep the sidebar sorted most-recent-first.
            let moved = sessions.remove(at: idx)
            sessions.insert(moved, at: 0)
        }
    }

    private func persistSessions() {
        store.save(sessions)
    }
}
