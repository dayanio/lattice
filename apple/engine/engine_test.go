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

package engine

import (
	"os"
	"path/filepath"
	"testing"

	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestResetIdentity_RemovesExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CFFIXED_USER_HOME", dir)

	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	identityDir := wgIdentityDir()
	if err := os.MkdirAll(identityDir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(identityDir, "wg-identity.key")
	if err := os.WriteFile(path, []byte(key.String()), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ResetIdentity(); err != nil {
		t.Fatalf("ResetIdentity() = %v, want nil", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("expected key file to be removed, stat err = %v", statErr)
	}
}

func TestResetIdentity_NoFilePresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CFFIXED_USER_HOME", dir)

	if err := ResetIdentity(); err != nil {
		t.Fatalf("ResetIdentity() with no file present = %v, want nil", err)
	}
}

func TestEnginePublicKey_EmptyBeforeKeyLoaded(t *testing.T) {
	e := &Engine{}
	if got := e.PublicKey(); got != "" {
		t.Fatalf("PublicKey() before any key is loaded = %q, want \"\"", got)
	}
}

func TestEnginePublicKey_ReturnsDerivedKey(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	e := &Engine{privKey: key}
	want := key.PublicKey().String()
	if got := e.PublicKey(); got != want {
		t.Fatalf("PublicKey() = %q, want %q", got, want)
	}
}
