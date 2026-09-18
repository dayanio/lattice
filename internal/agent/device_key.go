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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/pkg/utils"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// ensureDeviceKey returns this device's WireGuard private key, generating
// and persisting one on first run (ADR-0003: keys are generated on the
// agent and never leave it — only the derived public key is registered).
func ensureDeviceKey() (wgtypes.Key, error) {
	return ensureDeviceKeyAt(deviceKeyPath())
}

func deviceKeyPath() string {
	return filepath.Join(filepath.Dir(config.GetConfigFilePath()), "device.key")
}

func ensureDeviceKeyAt(path string) (wgtypes.Key, error) {
	if data, err := os.ReadFile(path); err == nil {
		if key, perr := utils.ParseKey(strings.TrimSpace(string(data))); perr == nil {
			return key, nil
		}
		// Corrupt file: fall through and regenerate.
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, fmt.Errorf("generate device key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return wgtypes.Key{}, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(key.String()+"\n"), 0o600); err != nil {
		return wgtypes.Key{}, fmt.Errorf("persist device key: %w", err)
	}
	return key, nil
}
