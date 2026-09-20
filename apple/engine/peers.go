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
	"encoding/json"
	"sort"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
)

// peerInfo is one remote node as the app shows it: what the tunnel itself
// knows, so the device list works without a management login.
type peerInfo struct {
	AppID    string `json:"appId"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Platform string `json:"platform,omitempty"`
	// State is the connection lifecycle: probing, ice-ready (direct), relay-ready
	// (relayed), failed, closed, or none (no probe yet).
	State  string `json:"state"`
	Online bool   `json:"online"`
}

// buildPeerList turns the netmap's peers and the probe states (keyed by AppID)
// into the app's device list, excluding this node itself and peers that have no
// overlay address yet. Sorted by name, then AppID.
func buildPeerList(peers []*infra.Peer, states map[string]string, selfAppID string) []peerInfo {
	out := make([]peerInfo, 0, len(peers))
	for _, p := range peers {
		if p == nil || p.Remove || p.AppID == selfAppID {
			continue
		}
		if p.Address == nil || *p.Address == "" {
			continue
		}
		state, ok := states[p.AppID]
		if !ok {
			state = "none"
		}
		name := p.Name
		if name == "" {
			name = p.AppID
		}
		out = append(out, peerInfo{
			AppID:    p.AppID,
			Name:     name,
			Address:  *p.Address,
			Platform: p.Platform,
			State:    state,
			Online:   state == "ice-ready" || state == "relay-ready",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].AppID < out[j].AppID
	})
	return out
}

func peerListJSON(peers []*infra.Peer, states map[string]string, selfAppID string) string {
	blob, err := json.Marshal(buildPeerList(peers, states, selfAppID))
	if err != nil {
		return "[]"
	}
	return string(blob)
}
