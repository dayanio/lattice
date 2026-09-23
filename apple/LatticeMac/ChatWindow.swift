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

/// AI assistant pane, Codex/Claude style: conversation-history sidebar on
/// the left, streaming thread + composer on the right. Conversations stream
/// from the control plane's /api/v1/ai/chat endpoint, where the model drives
/// MCP tools against the live network. Embedded as the AI tab's right pane
/// in the main window (formerly a standalone window).
struct AIChatPane: View {
    @StateObject private var chat = ChatViewModel()

    var body: some View {
        HStack(spacing: 0) {
            sidebar
            Divider()
            thread
        }
        .onAppear { chat.loadStore() }
    }

    // MARK: Sidebar

    private var sidebar: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                chat.newSession()
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "square.and.pencil")
                    Text("新对话").font(.system(.body, design: .rounded).weight(.medium))
                    Spacer()
                }
                .padding(.horizontal, 11)
                .padding(.vertical, 7)
                .background(Color.primary.opacity(0.06))
                .cornerRadius(8)
            }
            .buttonStyle(.plain)
            .padding(12)

            Text("历史对话")
                .font(.caption2.weight(.bold))
                .textCase(.uppercase)
                .foregroundColor(.secondary)
                .padding(.horizontal, 14)
                .padding(.bottom, 4)

            if chat.sessions.isEmpty {
                Text("暂无历史对话")
                    .font(.caption)
                    .foregroundColor(.secondary)
                    .padding(.horizontal, 14)
                    .padding(.top, 6)
            }

            ScrollView {
                VStack(spacing: 1) {
                    ForEach(chat.sessions) { session in
                        sessionRow(session)
                    }
                }
            }

            Spacer(minLength: 0)

            Divider()
            HStack(spacing: 6) {
                Image(systemName: "sparkles")
                    .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                Text("AI 可操作真实网络")
                    .font(.caption2)
                    .foregroundColor(.secondary)
                Spacer()
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
        }
        .frame(width: 232)
        .background(Color.primary.opacity(0.035))
    }

    private func sessionRow(_ session: ChatSession) -> some View {
        let selected = chat.currentID == session.id
        return Button {
            chat.select(session.id)
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Text(session.title)
                    .font(.system(size: 12.5, weight: selected ? .semibold : .regular))
                    .lineLimit(1)
                Text(relativeTime(session.updatedAt))
                    .font(.caption2)
                    .foregroundColor(.secondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 11)
            .padding(.vertical, 8)
            .background(
                RoundedRectangle(cornerRadius: 8)
                    .fill(selected ? Color.accentColor.opacity(0.14) : Color.clear)
            )
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button("删除对话", role: .destructive) {
                chat.delete(session.id)
            }
        }
        .padding(.horizontal, 8)
    }

    // MARK: Thread

    private var thread: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "sparkles")
                    .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                Text(chat.currentTitle)
                    .font(.system(.headline, design: .rounded))
                    .lineLimit(1)
                Spacer()
                if chat.isStreaming {
                    Text("正在思考…").font(.caption).foregroundColor(.secondary)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            Divider()

            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        if chat.messages.isEmpty {
                            emptyState
                        }
                        ForEach(chat.messages) { message in
                            ChatBubble(message: message)
                                .id(message.id)
                        }
                    }
                    .padding(14)
                }
                .onChange(of: chat.lastMessageID) { id in
                    if let id {
                        withAnimation { proxy.scrollTo(id, anchor: .bottom) }
                    }
                }
            }

            composer
        }
    }

    private var emptyState: some View {
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
                    HStack(spacing: 6) {
                        Image(systemName: "sparkle")
                        Text(prompt)
                    }
                    .font(.caption)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(Color.primary.opacity(0.06))
                    .cornerRadius(8)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Composer in the Claude/Codex style: an elevated rounded container with
    /// the field inside and a circular send button docked bottom-right.
    private var composer: some View {
        HStack(alignment: .bottom, spacing: 6) {
            TextField("描述你想做的操作…", text: $chat.input, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...6)
                .padding(.horizontal, 6)
                .padding(.vertical, 8)

            Button {
                chat.send(chat.input)
            } label: {
                Image(systemName: "arrow.up")
                    .font(.system(size: 12, weight: .bold))
                    .foregroundColor(.white)
                    .frame(width: 26, height: 26)
                    .background(
                        Circle().fill(
                            chat.canSend ? Color.primary : Color.secondary.opacity(0.4)
                        )
                    )
            }
            .buttonStyle(.plain)
            .disabled(!chat.canSend)
            .padding(5)
        }
        .background(
            RoundedRectangle(cornerRadius: 14)
                .fill(Color.primary.opacity(0.04))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 14)
                .strokeBorder(Color.primary.opacity(0.14))
        )
        .shadow(color: Color.black.opacity(0.07), radius: 10, y: 3)
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
    }

    private func relativeTime(_ date: Date) -> String {
        let formatter = RelativeDateTimeFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        return formatter.localizedString(for: date, relativeTo: Date())
    }
}

// MARK: - Message rendering


struct ChatBubble: View {
    let message: ChatDisplayMessage

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            if message.role == "user" { Spacer(minLength: 64) }
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
                        .padding(.horizontal, 12)
                        .padding(.vertical, 9)
                        .foregroundColor(message.role == "user" ? .white : .primary)
                        .background(bubbleColor)
                        .cornerRadius(14)
                }
            }
            if message.role != "user" { Spacer(minLength: 64) }
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
