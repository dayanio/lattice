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

/// iOS AI 助手：与 Mac 面板共用 ChatViewModel（会话历史按设备存储）。
/// 内置大脑由控制面提供（provider 可插拔），这里只做对话 UI。
struct AIChatView: View {
    @StateObject private var chat = ChatViewModel()

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                headerBar
                Divider()
                thread
                Divider()
                inputBar
            }
            .navigationTitle("AI 助手")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    sessionMenu
                }
            }
            .onAppear { chat.loadStore() }
        }
    }

    private var headerBar: some View {
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
        .padding(.vertical, 10)
    }

    private var sessionMenu: some View {
        Menu {
            Button { chat.newSession() } label: {
                Label("新对话", systemImage: "square.and.pencil")
            }
            if !chat.sessions.isEmpty {
                Menu("历史对话") {
                    ForEach(chat.sessions) { session in
                        Button(session.title) { chat.select(session.id) }
                    }
                }
            }
        } label: {
            Image(systemName: "ellipsis.circle")
        }
    }

    private var thread: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if chat.messages.isEmpty {
                        VStack(spacing: 8) {
                            Image(systemName: "sparkles")
                                .font(.title)
                                .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                            Text("向内置 AI 助手提问\n它可以直接操作网络里的设备")
                                .font(.caption)
                                .foregroundColor(.secondary)
                                .multilineTextAlignment(.center)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.top, 60)
                    }
                    ForEach(chat.messages) { message in
                        bubble(message)
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
    }

    private func bubble(_ message: ChatDisplayMessage) -> some View {
        let isUser = message.role == "user"
        return HStack {
            if isUser { Spacer(minLength: 40) }
            Text(message.text)
                .font(.system(.subheadline))
                .foregroundColor(isUser ? .white : .primary)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .fill(isUser ? LatticePalette.accent : Color.primary.opacity(0.06))
                )
                .textSelection(.enabled)
            if !isUser { Spacer(minLength: 40) }
        }
    }

    private var inputBar: some View {
        HStack(spacing: 10) {
            TextField("输入消息…", text: $chat.input, axis: .vertical)
                .textFieldStyle(.roundedBorder)
                .lineLimit(1...4)
                .onSubmit { chat.send(chat.input) }
                .disabled(chat.isStreaming)
            Button {
                chat.send(chat.input)
            } label: {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.title2)
                    .foregroundColor(chat.input.isEmpty || chat.isStreaming ? .secondary : LatticePalette.accent)
            }
            .disabled(chat.input.isEmpty || chat.isStreaming)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
    }
}
