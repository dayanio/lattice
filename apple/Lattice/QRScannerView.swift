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

import AVFoundation
import SwiftUI

/// Live camera QR scanner for join codes (AVCaptureMetadataOutput detects QR
/// natively — no Vision pass needed). Same payload contract as macOS's
/// CameraScannerView: lattice://join?server=<url>&token=<token>, parsed by
/// the shared JoinPayload type.
struct QRScannerView: UIViewRepresentable {
    /// Called on the main queue with the first QR string detected.
    var onCode: (String) -> Void
    /// Called on the main queue when the camera cannot be used.
    var onError: (String) -> Void

    func makeUIView(context: Context) -> UIView {
        let view = UIView(frame: .zero)
        context.coordinator.attach(to: view, onCode: onCode, onError: onError)
        return view
    }

    func updateUIView(_ uiView: UIView, context: Context) {
        context.coordinator.previewLayer?.frame = uiView.bounds
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    static func dismantleUIView(_ uiView: UIView, coordinator: Coordinator) {
        coordinator.stop()
    }

    final class Coordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate {
        private let session = AVCaptureSession()
        private var configured = false
        private var onCode: ((String) -> Void)?
        private var delivered = false
        var previewLayer: AVCaptureVideoPreviewLayer?

        func attach(to view: UIView, onCode: @escaping (String) -> Void, onError: @escaping (String) -> Void) {
            self.onCode = onCode
            // makeUIView runs inside view update — SwiftUI state writes must
            // never happen synchronously here, so every callback defers.
            let safeOnError: (String) -> Void = { message in
                DispatchQueue.main.async { onError(message) }
            }
            switch AVCaptureDevice.authorizationStatus(for: .video) {
            case .authorized:
                configure(view: view, onError: safeOnError)
            case .notDetermined:
                AVCaptureDevice.requestAccess(for: .video) { granted in
                    DispatchQueue.main.async {
                        granted ? self.configure(view: view, onError: safeOnError)
                                : safeOnError("相机权限被拒绝，请在设置中允许 Lattice 使用摄像头")
                    }
                }
            default:
                safeOnError("相机权限未开启，请在设置 → Lattice → 相机中允许访问")
            }
        }

        private func configure(view: UIView, onError: @escaping (String) -> Void) {
            configure(view: view, onError: onError, attempt: 1)
        }

        /// 会话装配偶发失败（权限授予瞬间的竞争、相机被占用）会自动重试一次。
        private func configure(view: UIView, onError: @escaping (String) -> Void, attempt: Int) {
            guard !configured else { return }
            configured = true
            guard let device = AVCaptureDevice.default(for: .video) else {
                onError("未找到可用摄像头")
                return
            }
            let input: AVCaptureDeviceInput
            do {
                input = try AVCaptureDeviceInput(device: device)
            } catch {
                // 权限被拒时这里会失败——给出可操作的指引而不是泛化错误。
                onError("无法访问摄像头（\(error.localizedDescription)）。"
                        + "请在 设置 → Lattice → 相机 中允许访问")
                return
            }
            NSLog("LATTICE-QR configure attempt %d", attempt)
            session.beginConfiguration()
            var setupError: String?
            let canIn = session.canAddInput(input)
            NSLog("LATTICE-QR canAddInput=%@", canIn ? "YES" : "NO")
            if canIn { session.addInput(input) }
            let output = AVCaptureMetadataOutput()
            let canOut = session.canAddOutput(output)
            NSLog("LATTICE-QR canAddOutput=%@", canOut ? "YES" : "NO")
            if canOut { session.addOutput(output) }
            if !canIn || !canOut {
                session.commitConfiguration()
                configured = false
                if attempt < 2 {
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.4) { [weak self] in
                        self?.configure(view: view, onError: onError, attempt: attempt + 1)
                    }
                } else {
                    onError("摄像头会话装配失败（\(canIn ? "输入" : "输出")不可用），请重试")
                }
                return
            }
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]
            session.commitConfiguration()
            NSLog("LATTICE-QR session configured")

            let preview = AVCaptureVideoPreviewLayer(session: session)
            preview.videoGravity = .resizeAspectFill
            preview.frame = view.bounds
            view.layer.addSublayer(preview)
            previewLayer = preview

            DispatchQueue.global(qos: .userInitiated).async { [session] in
                session.startRunning()
                NSLog("LATTICE-QR camera running")
            }
        }

        func metadataOutput(_ output: AVCaptureMetadataOutput,
                            didOutput metadataObjects: [AVMetadataObject],
                            from connection: AVCaptureConnection) {
            guard !delivered else { return }
            guard let object = metadataObjects.first as? AVMetadataMachineReadableCodeObject,
                  object.type == .qr,
                  let value = object.stringValue, !value.isEmpty else { return }
            delivered = true
            DispatchQueue.main.async { [onCode] in
                onCode?(value)
            }
        }

        func stop() {
            DispatchQueue.global(qos: .userInitiated).async { [session] in
                session.stopRunning()
            }
        }
    }
}
