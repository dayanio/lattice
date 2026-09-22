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

// Package embedded is a Lattice mesh engine that runs entirely in user
// space via lattice-shim's gVisor-backed Server — no Network Extension, no
// kernel TUN device, no system VPN authorization. It is meant to be
// embedded directly into a host process (e.g. via gomobile) that wants to
// Dial/Listen on the overlay without owning a full VPN client experience.
//
// Unlike apple/engine (which bridges wireguard-go to Apple's
// NEPacketTunnelProvider via packetTUN), embedded reuses the same
// internal/agent.NewNode data plane through its CustomTUN/ProvisionerFactory
// extension point — the same one internal/agent/gvisor uses for the Linux
// AgentSandbox. No new WireGuard/gVisor bridging code is written here.
package embedded

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Config is the JSON-decoded configuration for a Config.
type Config struct {
	ServerURL string `json:"serverURL"`
	Token     string `json:"token"`
	Name      string `json:"name"`
}

// DefaultName is used when Config.Name is empty.
const DefaultName = "lattice-embedded"

// ParseConfig decodes and validates configJSON. ServerURL and Token are
// required; Name defaults to DefaultName when omitted.
func ParseConfig(configJSON string) (Config, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ServerURL == "" {
		return Config{}, errors.New("config: serverURL is required")
	}
	if cfg.Token == "" {
		return Config{}, errors.New("config: token is required")
	}
	if cfg.Name == "" {
		cfg.Name = DefaultName
	}
	return cfg, nil
}
