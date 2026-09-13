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

/// LLM assistant window. Conversations stream from the control plane's
/// /api/v1/ai/chat endpoint, where the model drives MCP tools against the
/// live network — this view renders tokens and tool activity only.
struct ChatWindow: View {
    @StateObject private var chat = ChatViewModel()

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles")
                    .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                Text("Lattice AI 助手")
                    .font(.system(.headline, design: .rounded))
                Spacer()
                Button {
                    chat.reset()
                } label: {
                    Text("新对话").font(.caption)
                }
                .buttonStyle(.plain)
                .disabled(chat.isStreaming)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        if chat.messages.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("用自然语言操作你的网络")
                                    .font(.system(.headline, design: .rounded))
                                Text("AI 助手可以查看设备、读写策略、执行诊断。试一试：")
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                ForEach(chat.quickPrompts, id: \.self) { prompt in
                                    Button {
                                        chat.send(prompt)
                                    } label: {
                                        Text(prompt)
                                            .font(.caption)
                                            .padding(.horizontal, 10)
                                            .padding(.vertical, 6)
                                            .background(Color.accentColor.opacity(0.1))
                                            .cornerRadius(8)
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                            .padding(16)
                        }
                        ForEach(chat.messages) { message in
                            ChatBubble(message: message)
                                .id(message.id)
                        }
                    }
                    .padding(12)
                }
                .onChange(of: chat.lastMessageID) { id in
                    if let id {
                        withAnimation { proxy.scrollTo(id, anchor: .bottom) }
                    }
                }
            }

            Divider()

            HStack(spacing: 8) {
                TextField("描述你想做的操作…", text: $chat.input, axis: .vertical)
                    .textFieldStyle(.plain)
                    .lineLimit(1...4)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 7)
                    .background(Color.primary.opacity(0.06))
                    .cornerRadius(10)
                    .onSubmit { chat.send(chat.input) }

                Button {
                    chat.send(chat.input)
                } label: {
                    if chat.isStreaming {
                        ProgressView().controlSize(.small)
                    } else {
                        Image(systemName: "paperplane.fill")
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(chat.isStreaming || chat.input.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .padding(12)
        }
        .frame(width: 480)
        .frame(minHeight: 500, maxHeight: 760)
    }
}

// MARK: - Message rendering

struct ChatDisplayMessage: Identifiable, Equatable {
    var id = UUID()
    /// "user" | "assistant" | "tool" | "error"
    let role: String
    var text: String
}

struct ChatBubble: View {
    let message: ChatDisplayMessage

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            if message.role == "user" { Spacer(minLength: 48) }
            VStack(alignment: .leading, spacing: 4) {
                if message.role == "tool" {
                    HStack(spacing: 5) {
                        Image(systemName: "wrench.and.screwdriver")
                            .font(.caption2)
                        Text("调用工具 \(message.text)")
                            .font(.caption)
                    }
                    .foregroundColor(.secondary)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 5)
                    .background(Color.primary.opacity(0.05))
                    .cornerRadius(8)
                } else {
                    Text(bubbleText)
                        .font(.system(.caption))
                        .textSelection(.enabled)
                        .padding(.horizontal, 11)
                        .padding(.vertical, 8)
                        .foregroundColor(message.role == "user" ? .white : .primary)
                        .background(bubbleColor)
                        .cornerRadius(12)
                }
            }
            if message.role != "user" { Spacer(minLength: 48) }
        }
    }

    private var bubbleColor: Color {
        if message.role == "user" { return .accentColor }
        if message.role == "error" { return Color.red.opacity(0.12) }
        return Color.primary.opacity(0.06)
    }

    private var bubbleText: AttributedString {
        if message.role == "assistant", let rendered = try? AttributedString(
            markdown: message.text,
            options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        ) {
            return rendered
        }
        return AttributedString(message.text)
    }
}

// MARK: - View model

@MainActor
final class ChatViewModel: ObservableObject {
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

    func reset() {
        messages = []
        history = []
        input = ""
    }

    func send(_ raw: String) {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !isStreaming else { return }
        input = ""

        messages.append(ChatDisplayMessage(role: "user", text: text))
        let assistantID = appendAssistantBubble()
        isStreaming = true
        lastMessageID = assistantID

        Task {
            defer { isStreaming = false }
            do {
                try await LatticeAPI.shared.streamChat(
                    message: text,
                    history: history
                ) { [weak self] event in
                    self?.handle(event)
                }
            } catch {
                replaceEmptyAssistant(with: ("error", "连接失败: \(error.localizedDescription)"))
            }
            recordHistory(userText: text, assistantID: assistantID)
            lastMessageID = messages.last?.id
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

    /// Fills the trailing empty assistant bubble, or appends, with an error.
    private func replaceEmptyAssistant(with message: (role: String, text: String)) {
        if let idx = messages.lastIndex(where: { $0.role == "assistant" && $0.text.isEmpty }) {
            messages[idx] = ChatDisplayMessage(id: messages[idx].id, role: message.role, text: message.text)
        } else {
            messages.append(ChatDisplayMessage(role: message.role, text: message.text))
        }
        lastMessageID = messages.last?.id
    }

    /// Keeps the trailing turns for conversational context.
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
}
