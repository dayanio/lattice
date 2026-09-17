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

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestEnsureDeviceKeyAt_GeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")

	k1, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms = %v, want 0600", info.Mode().Perm())
	}

	// Second call must return the SAME key (persisted, not regenerated).
	k2, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Fatal("device key must be stable across calls")
	}
}

func TestEnsureDeviceKeyAt_RegeneratesOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "device.key")
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := ensureDeviceKeyAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if k == (wgtypes.Key{}) {
		t.Fatal("expected a regenerated key")
	}
}
