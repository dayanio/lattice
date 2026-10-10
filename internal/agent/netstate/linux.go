//go:build linux

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

package netstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateDirOverride lets deployments relocate the state directory (tests,
// containers). Empty means the default.
var StateDirOverride string

// StateDir returns the directory holding lattice's network-state manifest.
func StateDir() string {
	if StateDirOverride != "" {
		return StateDirOverride
	}
	return "/var/lib/lattice"
}

// ManifestPath is the net-state manifest: what lattice last applied, so
// `net status` can report even after the engine died.
const manifestName = "net-state.json"

type manifest struct {
	ExitTakeover bool      `json:"exit_takeover"`
	Iface        string    `json:"iface,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func writeManifest(m *manifest) error {
	if err := os.MkdirAll(StateDir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(StateDir(), manifestName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ApplyExitTakeover installs the 0/1+128/1 policy-routing takeover for iface
// and records it in the net-state manifest.
func ApplyExitTakeover(r Runner, iface string) error {
	if err := Execute(r, ExitTakeoverCommands(iface)); err != nil {
		return err
	}
	if err := writeManifest(&manifest{ExitTakeover: true, Iface: iface, UpdatedAt: time.Now().UTC()}); err != nil {
		// The system state is applied; the manifest is best-effort.
		return fmt.Errorf("exit takeover applied but manifest write failed: %w", err)
	}
	return nil
}

// RemoveExitTakeover removes the takeover routes and rules, updating the
// manifest. Missing state is not an error.
func RemoveExitTakeover(r Runner) error {
	err := Execute(r, ExitTakeoverRemoveCommands())
	if mErr := writeManifest(&manifest{UpdatedAt: time.Now().UTC()}); mErr != nil && err == nil {
		err = mErr
	}
	return err
}

// Cleanup sweeps every piece of network state lattice may have written:
// exit-takeover rules/routes plus all LATTICE-* iptables chains and their
// jumps in the built-in chains. Idempotent; safe offline.
func Cleanup(r Runner) error {
	err := Execute(r, CleanupCommands())
	if mErr := writeManifest(&manifest{UpdatedAt: time.Now().UTC()}); mErr != nil && err == nil {
		err = mErr
	}
	return err
}
