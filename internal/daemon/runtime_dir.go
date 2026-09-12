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

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// RuntimeDir returns the directory for runtime files (socket, pidfile, log).
// The daemon and the CLI must resolve the same path independently of euid:
//   - linux: /var/run/lattice (daemon may run as root via sudo)
//   - darwin/windows: ~/.lattice
func RuntimeDir() string {
	if runtime.GOOS == "linux" {
		return "/var/run/lattice"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/var/run/lattice"
	}
	return filepath.Join(home, ".lattice")
}

// SocketPath returns the local IPC socket path.
func SocketPath() string { return filepath.Join(RuntimeDir(), "lattice.sock") }

// DefaultPidFilePath returns the pidfile path.
func DefaultPidFilePath() string { return PidFilePath(RuntimeDir()) }

// DefaultLogFilePath returns the daemon log path.
func DefaultLogFilePath() string { return LogFilePath(RuntimeDir()) }

// EnsureRuntimeDir creates the runtime directory.
func EnsureRuntimeDir() error { return os.MkdirAll(RuntimeDir(), 0o755) }

// SystemdUnitContent renders a systemd unit for the node daemon.
func SystemdUnitContent(execPath, workingUser string) string {
	return fmt.Sprintf(`[Unit]
Description=Lattice node daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
ExecStart=%s up
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, workingUser, execPath)
}

// LaunchdPlistContent renders a launchd plist for the node daemon (macOS).
func LaunchdPlistContent(execPath, label string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>up</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`, label, execPath)
}
