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
import Speech
import AVFoundation

/// 语音转文字（本机 SFSpeechRecognizer，音频不出设备——与 v2 语音入口
/// 的设计原则一致）。开始录音即流式转写 transcript，停止后文本保留。
@MainActor
final class SpeechTranscriber: ObservableObject {
    @Published private(set) var isRecording = false
    @Published private(set) var transcript = ""
    @Published private(set) var errorText = ""

    private let audioEngine = AVAudioEngine()
    private var recognizer: SFSpeechRecognizer?
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?

    func toggle() {
        if isRecording { stop() } else { start() }
    }

    func start() {
        errorText = ""
        SFSpeechRecognizer.requestAuthorization { [weak self] status in
            Task { @MainActor in
                guard let self else { return }
                guard status == .authorized else {
                    self.errorText = "语音识别权限被拒绝，请在设置中开启"
                    return
                }
                AVAudioSession.sharedInstance().requestRecordPermission { granted in
                    Task { @MainActor in
                        guard granted else {
                            self.errorText = "麦克风权限被拒绝，请在设置中开启"
                            return
                        }
                        self.begin()
                    }
                }
            }
        }
    }

    private func begin() {
        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .measurement, options: .duckOthers)
            try session.setActive(true, options: .notifyOthersOnDeactivation)

            let locale = Locale(identifier: "zh-CN")
            let recognizer = SFSpeechRecognizer(locale: locale) ?? SFSpeechRecognizer()
            guard let recognizer, recognizer.isAvailable else {
                errorText = "语音识别服务不可用"
                return
            }
            self.recognizer = recognizer

            let request = SFSpeechAudioBufferRecognitionRequest()
            request.shouldReportPartialResults = true
            self.request = request

            let input = audioEngine.inputNode
            let format = input.outputFormat(forBus: 0)
            input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
                request.append(buffer)
            }
            audioEngine.prepare()
            try audioEngine.start()

            transcript = ""
            isRecording = true
            task = recognizer.recognitionTask(with: request) { [weak self] result, _ in
                if let result {
                    let text = result.bestTranscription.formattedString
                    Task { @MainActor in self?.transcript = text }
                }
            }
        } catch {
            errorText = "录音启动失败：\(error.localizedDescription)"
            cleanup()
        }
    }

    func stop() {
        audioEngine.stop()
        audioEngine.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        task?.finish()
        request = nil
        task = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        isRecording = false
    }

    private func cleanup() {
        audioEngine.stop()
        audioEngine.inputNode.removeTap(onBus: 0)
        request = nil
        task = nil
        isRecording = false
    }
}

/// iOS AI 助手：与 Mac 面板共用 ChatViewModel（会话历史按设备存储）。
/// 支持语音转文字输入（本机识别）与滚动/点击收起键盘。
struct AIChatView: View {
    @StateObject private var chat = ChatViewModel()
    @StateObject private var speech = SpeechTranscriber()

    /// 语音输入开始时暂存的草稿，转写文本追加在其后。
    @State private var draftBeforeSpeech = ""
    @FocusState private var inputFocused: Bool

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
                ToolbarItem(placement: .topBarTrailing) { sessionMenu }
                // 键盘工具条上的收起按钮——输入框聚焦时也能一键收起。
                ToolbarItemGroup(placement: .keyboard) {
                    Spacer()
                    Button("收起") { inputFocused = false }
                        .foregroundColor(.secondary)
                }
            }
            .onAppear { chat.loadStore() }
            .onDisappear { if speech.isRecording { speech.stop() } }
        }
    }

    // MARK: - 头部

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
            sessionMenu
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
                .foregroundColor(.secondary)
        }
    }

    // MARK: - 消息流

    private var thread: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if chat.messages.isEmpty && chat.input.isEmpty && !speech.isRecording {
                        emptyState
                    }
                    ForEach(chat.messages) { message in
                        bubble(message).id(message.id)
                    }
                    if speech.isRecording {
                        recordingHint
                    }
                }
                .padding(14)
            }
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: chat.lastMessageID) { id in
                if let id {
                    withAnimation { proxy.scrollTo(id, anchor: .bottom) }
                }
            }
            .onChange(of: speech.transcript) { text in
                // 转写实时进输入框；开始录音前的草稿保留在前面。
                let prefix = draftBeforeSpeech.isEmpty ? "" : draftBeforeSpeech + " "
                chat.input = (prefix + text).trimmingCharacters(in: .whitespaces)
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 14) {
            ZStack {
                Circle()
                    .fill(Color(red: 0.49, green: 0.48, blue: 1.0).opacity(0.12))
                    .frame(width: 72, height: 72)
                Image(systemName: "sparkles")
                    .font(.title)
                    .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
            }
            Text("向 AI 助手提问")
                .font(.headline)
            Text("它可以直接操作网络里的设备")
                .font(.caption)
                .foregroundColor(.secondary)

            VStack(spacing: 8) {
                ForEach(["列出网络里的设备", "我的 IP 地址是什么", "路由器状态怎么样"], id: \.self) { suggestion in
                    Button {
                        chat.input = suggestion
                        inputFocused = true
                    } label: {
                        Text(suggestion)
                            .font(.subheadline)
                            .padding(.horizontal, 14)
                            .padding(.vertical, 8)
                            .background(Capsule().fill(Color.primary.opacity(0.06)))
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 50)
    }

    private var recordingHint: some View {
        HStack(spacing: 8) {
            Circle().fill(.red).frame(width: 8, height: 8)
            Text("正在聆听，说完后点麦克风结束")
                .font(.caption)
                .foregroundColor(.secondary)
        }
        .padding(.vertical, 4)
    }

    // MARK: - 消息气泡

    private func bubble(_ message: ChatDisplayMessage) -> some View {
        let isUser = message.role == "user"
        let isError = message.role == "error"
        return HStack {
            if isUser { Spacer(minLength: 48) }
            if !isUser {
                Image(systemName: "sparkles")
                    .font(.caption)
                    .foregroundColor(Color(red: 0.49, green: 0.48, blue: 1.0))
                    .padding(.top, 6)
            }
            Text(message.text)
                .font(.system(.subheadline))
                .foregroundColor(isUser ? .white : (isError ? .red : .primary))
                .padding(.horizontal, 13)
                .padding(.vertical, 9)
                .background(
                    RoundedRectangle(cornerRadius: 16, style: .continuous)
                        .fill(
                            isUser ? AnyShapeStyle(LatticePalette.accent)
                                   : (isError ? AnyShapeStyle(Color.red.opacity(0.08))
                                              : AnyShapeStyle(Color.primary.opacity(0.06)))
                        )
                )
                .textSelection(.enabled)
            if !isUser { Spacer(minLength: 48) }
        }
    }

    // MARK: - 输入栏

    private var inputBar: some View {
        HStack(spacing: 10) {
            Button {
                if speech.isRecording {
                    speech.stop()
                    inputFocused = true
                } else {
                    draftBeforeSpeech = chat.input
                    speech.start()
                }
            } label: {
                Image(systemName: speech.isRecording ? "stop.circle.fill" : "mic.circle.fill")
                    .font(.system(size: 26))
                    .foregroundColor(speech.isRecording ? .red : LatticePalette.accent)
                    .symbolRenderingMode(.hierarchical)
            }
            .buttonStyle(.plain)

            TextField(speech.isRecording ? "正在聆听…" : "输入消息…",
                      text: $chat.input, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...4)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(Capsule().fill(Color.primary.opacity(0.06)))
                .focused($inputFocused)
                .onSubmit {
                    if !chat.input.isEmpty { chat.send(chat.input) }
                }
                .disabled(chat.isStreaming)

            Button {
                chat.send(chat.input)
                inputFocused = false
            } label: {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.system(size: 28))
                    .foregroundColor(canSend ? LatticePalette.accent : .secondary)
                    .symbolRenderingMode(.hierarchical)
            }
            .buttonStyle(.plain)
            .disabled(!canSend)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
    }

    private var canSend: Bool { !chat.input.isEmpty && !chat.isStreaming }
}
