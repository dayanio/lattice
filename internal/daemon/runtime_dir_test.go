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
	"runtime"
	"strings"
	"testing"
)

func TestRuntimeDir_PlatformDefaults(t *testing.T) {
	switch runtime.GOOS {
	case "linux":
		if got := RuntimeDir(); got != "/var/run/lattice" {
			t.Fatalf("RuntimeDir(linux) = %q", got)
		}
	case "darwin":
		if !strings.Contains(RuntimeDir(), ".lattice") {
			t.Fatalf("RuntimeDir(darwin) = %q, want ~/.lattice", RuntimeDir())
		}
	}
	if SocketPath() == "" || DefaultPidFilePath() == "" || DefaultLogFilePath() == "" {
		t.Fatal("paths must not be empty")
	}
}

func TestSystemdUnitContent(t *testing.T) {
	unit := SystemdUnitContent("/usr/local/bin/lattice", "francis")
	for _, want := range []string{
		"After=network-online.target",
		"User=francis",
		"ExecStart=/usr/local/bin/lattice up",
		"Restart=on-failure",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestLaunchdPlistContent(t *testing.T) {
	plist := LaunchdPlistContent("/usr/local/bin/lattice", "io.lattice.node")
	for _, want := range []string{"<plist", "io.lattice.node", "/usr/local/bin/lattice", "RunAtLoad"} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist missing %q", want)
		}
	}
}
