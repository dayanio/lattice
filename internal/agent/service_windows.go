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

//go:build windows

package agent

import (
	"fmt"
	"runtime"

	"github.com/alatticeio/lattice/internal/agent/config"
)

// InstallService is not supported on Windows: there is no systemd unit or
// launchd plist to write. It exists so the CLI's `service install` command
// compiles on every platform and reports the same error run.go's default case does.
func InstallService(flags *config.Config) error {
	return fmt.Errorf("service install not supported on %s", runtime.GOOS)
}

// UninstallService is a no-op on Windows: nothing was installed.
func UninstallService(flags *config.Config) error {
	return nil
}
