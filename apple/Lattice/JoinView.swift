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

/// 加入入口模式：.scan 出现即打开摄像头；.manual 停留在表单。
/// Identifiable：供 OverviewView 的 sheet(item:) 原子传递，避免两个
/// @State 同事务变更时 sheet 拿到旧 mode 的竞态。
enum JoinMode: Identifiable {
    case scan
    case manual

    var id: Self { self }
}

/// Join-network flow (scan or manual entry): server URL + enrollment token +
/// device name → creates the VPN profile (TunnelManager.saveJoin) and
/// connects. Admin login is a separate optional step (LoginView) — the
/// tunnel itself only needs the enrollment token.
///
/// 扫码器直接内嵌为本视图的一个形态（scannerStep），不再作为二级 sheet
/// 弹出——sheet 套 sheet 曾导致二次点击无效与闪退。
struct JoinView: View {
    var onFinished: () -> Void
    var mode: JoinMode = .manual
    /// When true, a successful join also discards this device's persisted
    /// WireGuard identity first (see SettingsView's "重新生成密钥" action).
    /// Every other call site omits this and gets ordinary rejoin behavior.
    var initialResetIdentity: Bool = false

    @State private var useScanner: Bool
    @State private var joinInput = ""
    /// "Log in and join": an account issues this device's token, instead of an
    /// invite link or token being pasted.
    @State private var accountMode = false
    @State private var username = UserDefaults.standard.string(forKey: "lattice.adminUser") ?? "admin"
    @State private var password = ""
    @State private var serverURL = UserDefaults.standard.string(forKey: "lattice.serverURL") ?? ""
    @State private var deviceName = UIDevice.current.name
    @State private var isSavingNetwork = false
    @State private var failure: JoinFailure?
    @State private var scannerError = ""
    @Environment(\.dismiss) private var dismiss

    private var payload: JoinPayload? { JoinPayload(joinInput) }
    private var effectiveToken: String { payload?.token ?? "" }
    private var effectiveServer: String {
        (payload?.serverURL ?? serverURL).trimmingCharacters(in: .whitespacesAndNewlines)
    }
    private var effectiveName: String { payload?.name ?? deviceName }
    private var canJoin: Bool {
        if accountMode {
            return !serverURL.trimmingCharacters(in: .whitespaces).isEmpty && !username.isEmpty && !password.isEmpty && !isSavingNetwork
        }
        return !effectiveToken.isEmpty && !effectiveServer.isEmpty && !isSavingNetwork
    }

    init(onFinished: @escaping () -> Void, mode: JoinMode = .manual, initialResetIdentity: Bool = false) {
        self.onFinished = onFinished
        self.mode = mode
        self.initialResetIdentity = initialResetIdentity
        _useScanner = State(initialValue: mode == .scan)
    }

    var body: some View {
        NavigationStack {
            Group {
                if useScanner {
                    scannerStep
                } else {
                    networkStep
                }
            }
            .toolbar {
                ToolbarItem(placement: .navigationBarLeading) {
                    Button {
                        dismiss()
                    } label: {
                        Label("返回", systemImage: "chevron.left")
                    }
                }
            }
        }
    }

    /// 摄像头取景全屏形态。
    private var scannerStep: some View {
        ZStack(alignment: .bottom) {
            QRScannerView(
                onCode: { code in
                    handleScanned(code)
                },
                onError: { message in
                    scannerError = message
                }
            )
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .ignoresSafeArea(edges: .bottom)

            if isSavingNetwork {
                VStack(spacing: 10) {
                    ProgressView().tint(.white)
                    Text("正在加入网络…")
                        .font(.caption)
                        .foregroundColor(.white)
                }
                .padding(16)
                .background(.black.opacity(0.6))
                .cornerRadius(12)
            }

            VStack(spacing: 10) {
                if !scannerError.isEmpty {
                    Text(scannerError)
                        .font(.caption)
                        .foregroundColor(.white)
                        .padding(8)
                        .background(.black.opacity(0.6))
                        .cornerRadius(8)
                }
                Button {
                    useScanner = false
                } label: {
                    Label("改用手动输入", systemImage: "keyboard")
                        .font(.system(size: 13, weight: .semibold))
                }
                .buttonStyle(.borderedProminent)
            }
            .padding(.bottom, 24)
        }
        .navigationTitle("扫描二维码")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("取消") { onFinished() }
            }
        }
    }

    /// 表单形态：一个输入框接受邀请链接或令牌；服务器地址和设备名折叠在“高级”里，
    /// 只有输入里没有服务器地址时才需要填。
    private var networkStep: some View {
        Form {
            Section {
                Picker("加入方式", selection: $accountMode) {
                    Text("邀请链接 / 令牌").tag(false)
                    Text("账号登录").tag(true)
                }
                .pickerStyle(.segmented)
            }

            if accountMode {
                Section {
                    TextField("服务器 URL (http://…)", text: $serverURL)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    TextField("用户名", text: $username)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    SecureField("密码", text: $password)
                } header: {
                    Text("账号")
                } footer: {
                    Text("用账号为这台设备签发入网令牌，登录状态会保留，之后的管理操作不必再登录。")
                }
            } else {
                Section {
                    TextField("粘贴邀请链接或入网令牌", text: $joinInput)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)
                    Button {
                        pasteFromClipboard()
                    } label: {
                        Label("从剪贴板粘贴", systemImage: "doc.on.clipboard")
                    }
                    Button {
                        useScanner = true
                    } label: {
                        Label("扫描二维码", systemImage: "qrcode.viewfinder")
                    }
                } header: {
                    Text("邀请链接或令牌")
                } footer: {
                    if let payload, payload.token != nil, payload.serverURL == nil, serverURL.isEmpty {
                        Text("这个令牌不含服务器地址，请在下面的“高级”里填写。")
                    } else if let server = payload?.serverURL {
                        Text("服务器：\(server)")
                    }
                }
            }

            Section {
                DisclosureGroup(accountMode ? "高级（设备名）" : "高级（服务器地址、设备名）") {
                    if !accountMode {
                        TextField("服务器 URL (http://…)", text: $serverURL)
                            .keyboardType(.URL)
                            .autocorrectionDisabled()
                            .textInputAutocapitalization(.never)
                    }
                    TextField("设备名", text: $deviceName)
                    if let stored = DeviceName.preview(effectiveName) {
                        Text("将保存为 \(stored)")
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                }
            }

            if let failure {
                Section {
                    Text(failure.title).font(.subheadline.weight(.semibold)).foregroundColor(.red)
                    if !failure.advice.isEmpty {
                        Text(failure.advice).font(.caption).foregroundColor(.secondary)
                    }
                }
            }

            Section {
                Button {
                    join()
                } label: {
                    if isSavingNetwork {
                        HStack(spacing: 8) {
                            ProgressView()
                            Text(accountMode ? "正在登录…" : "正在保存配置…")
                        }
                    } else {
                        Text(accountMode ? "登录并加入" : "加入网络")
                    }
                }
                .disabled(!canJoin)
            } footer: {
                Text("加入后系统会请求授权创建 VPN 配置，请在弹窗里点“允许”。")
            }
        }
        .navigationTitle("加入网络")
    }

    private func join() {
        if accountMode {
            joinWithAccount()
        } else {
            saveAndConnect(server: effectiveServer, token: effectiveToken, name: effectiveName)
        }
    }

    /// Logs in, has the account issue this device's token, then joins with it.
    private func joinWithAccount() {
        isSavingNetwork = true
        failure = nil
        let server = serverURL.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmed = server.hasSuffix("/") ? String(server.dropLast()) : server
        Task {
            do {
                let issued = try await LatticeAPI.shared.loginAndCreateDeviceToken(server: trimmed, user: username, pass: password)
                password = ""
                saveAndConnect(server: trimmed, token: issued, name: deviceName)
            } catch let error as AccountJoinError {
                isSavingNetwork = false
                failure = error.failure
            } catch {
                isSavingNetwork = false
                failure = .tokenNotIssued(error.localizedDescription)
            }
        }
    }

    private func pasteFromClipboard() {
        guard let raw = UIPasteboard.general.string else {
            failure = JoinFailure(title: "剪贴板是空的", advice: "先复制邀请链接或入网令牌。")
            return
        }
        guard JoinPayload(raw) != nil else {
            failure = JoinFailure(title: "剪贴板里不是有效的入网信息", advice: "需要 lattice://join?… 链接，或不含空格的入网令牌。")
            return
        }
        joinInput = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        failure = nil
    }

    private func handleScanned(_ code: String) {
        guard let payload = JoinPayload(code) else {
            scannerError = "二维码格式不正确"
            return
        }
        // 完整入网码（服务端地址 + 令牌都在）→ 直接继续，省去手输与再次点击；
        // 停留在扫码页展示加入进度/报错，不要在异步结果出来前就跳走。
        if let server = payload.serverURL, let token = payload.token {
            saveAndConnect(server: server, token: token, name: payload.name ?? deviceName)
            return
        }
        joinInput = code
        useScanner = false
    }

    private func saveAndConnect(server: String, token: String, name: String) {
        isSavingNetwork = true
        failure = nil
        scannerError = ""
        let trimmed = server.hasSuffix("/") ? String(server.dropLast()) : server
        UserDefaults.standard.set(trimmed, forKey: "lattice.serverURL")
        // Peers appear under the server's normalized name; keep the same form so
        // "this device" is recognised in the list.
        UserDefaults.standard.set(DeviceName.normalized(name), forKey: "lattice.nodeName")
        TunnelManager.shared.saveJoin(serverURL: trimmed, token: token, name: name, resetIdentity: initialResetIdentity) { err in
            isSavingNetwork = false
            if let err {
                let f = JoinFailure(title: "保存 VPN 配置失败", advice: "\(err)。请在系统弹窗里点“允许”后重试。")
                failure = f
                scannerError = f.title
                return
            }
            joinInput = ""
            TunnelManager.shared.connect()
            onFinished()
        }
    }
}
