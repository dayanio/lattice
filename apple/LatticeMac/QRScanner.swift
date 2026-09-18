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
/// natively — no Vision pass needed). Payload contract:
/// lattice://join?server=<management-url>&token=<enrollment-token>
struct CameraScannerView: NSViewRepresentable {
    /// Called on the main queue with the first QR string detected.
    var onCode: (String) -> Void
    /// Called on the main queue when the camera cannot be used.
    var onError: (String) -> Void

    func makeNSView(context: Context) -> NSView {
        let view = NSView(frame: .zero)
        context.coordinator.attach(to: view, onCode: onCode, onError: onError)
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator: NSObject, AVCaptureMetadataOutputObjectsDelegate {
        private let session = AVCaptureSession()
        private var configured = false
        private var onCode: ((String) -> Void)?
        private var delivered = false

        func attach(to view: NSView, onCode: @escaping (String) -> Void, onError: @escaping (String) -> Void) {
            self.onCode = onCode
            // makeNSView runs inside view update — SwiftUI state writes must
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
                                : safeOnError("相机权限被拒绝，请在系统设置中允许 Lattice 使用摄像头")
                    }
                }
            default:
                safeOnError("相机权限未开启，请在系统设置 → 隐私与安全性 → 摄像头中允许 Lattice")
            }
        }

        private func configure(view: NSView, onError: @escaping (String) -> Void) {
            guard !configured else { return }
            configured = true
            guard let device = AVCaptureDevice.default(for: .video),
                  let input = try? AVCaptureDeviceInput(device: device) else {
                onError("未找到可用摄像头")
                return
            }
            session.beginConfiguration()
            session.addInput(input)
            let output = AVCaptureMetadataOutput()
            guard session.canAddOutput(output), session.canAddInput(input) else {
                session.commitConfiguration()
                onError("摄像头初始化失败")
                return
            }
            session.addOutput(output)
            output.setMetadataObjectsDelegate(self, queue: .main)
            output.metadataObjectTypes = [.qr]
            session.commitConfiguration()

            let preview = AVCaptureVideoPreviewLayer(session: session)
            preview.videoGravity = .resizeAspectFill
            view.wantsLayer = true
            view.layer?.addSublayer(preview)
            DispatchQueue.main.async {
                preview.frame = view.bounds
            }

            DispatchQueue.global(qos: .userInitiated).async { [session] in
                session.startRunning()
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
