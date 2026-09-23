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

package embedded

import (
	"encoding/base64"
	"fmt"
	"testing"

	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func TestParseConfig_RequiresServerURL(t *testing.T) {
	_, err := ParseConfig(`{"token":"lt-abc","name":"x"}`)
	if err == nil {
		t.Fatal("expected error when serverURL is missing")
	}
}

func TestParseConfig_RequiresToken(t *testing.T) {
	_, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","name":"x"}`)
	if err == nil {
		t.Fatal("expected error when token is missing")
	}
}

func TestParseConfig_DefaultsName(t *testing.T) {
	cfg, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","token":"lt-abc"}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Name != "lattice-embedded" {
		t.Errorf("expected default name %q, got %q", "lattice-embedded", cfg.Name)
	}
}

func TestParseConfig_KeepsExplicitName(t *testing.T) {
	cfg, err := ParseConfig(`{"serverURL":"http://127.0.0.1:8080","token":"lt-abc","name":"my-app"}`)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Name != "my-app" {
		t.Errorf("expected name %q, got %q", "my-app", cfg.Name)
	}
}

func TestParseConfig_InvalidJSON(t *testing.T) {
	_, err := ParseConfig(`not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseConfig_RejectsBadPrivateKey(t *testing.T) {
	if _, err := ParseConfig(`{"serverURL":"http://x","token":"t","privateKey":"not-base64-key"}`); err == nil {
		t.Fatal("expected error for malformed privateKey")
	}
	if _, err := ParseConfig(`{"serverURL":"http://x","token":"t","privateKey":"AAAA"}`); err == nil {
		t.Fatal("expected error for wrong-length privateKey")
	}
}

func TestParseConfig_AcceptsValidPrivateKey(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	cfg, err := ParseConfig(fmt.Sprintf(`{"serverURL":"http://x","token":"t","privateKey":%q}`, base64.StdEncoding.EncodeToString(key[:])))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.PrivateKey == "" {
		t.Error("expected privateKey to be kept")
	}
}

func TestEmbeddedEngine_PrivateKeyRoundTrip(t *testing.T) {
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(key[:])
	e, err := New(fmt.Sprintf(`{"serverURL":"http://x","token":"t","privateKey":%q}`, encoded))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := e.PrivateKey(); got != encoded {
		t.Errorf("PrivateKey round trip mismatch:\n got %q\nwant %q", got, encoded)
	}
}
