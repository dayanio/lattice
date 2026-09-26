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
	"strconv"
	"strings"
)

// PidFilePath returns the default pidfile path under dir.
func PidFilePath(dir string) string { return filepath.Join(dir, "lattice.pid") }

// LogFilePath returns the default daemon log path under dir.
func LogFilePath(dir string) string { return filepath.Join(dir, "lattice.log") }

// Write atomically records pid at path.
func Write(path string, pid int) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read parses the pid recorded at path.
func Read(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse pidfile %s: %w", path, err)
	}
	return pid, nil
}

// IsAlive reports whether the process is running. The probe is platform
// specific (pidfile_unix.go, pidfile_windows.go): syscall.Kill does not exist
// on Windows.
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return processAlive(pid)
}

// Remove deletes the pidfile if present.
func Remove(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
