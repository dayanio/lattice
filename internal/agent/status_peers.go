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
	"sort"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/wireguard"
	"github.com/alatticeio/lattice/internal/daemon"
)

// buildPeerStatuses combines the known peers with their transport states
// (keyed by AppID). Peers without a public key are skipped because status
// output is matched to WireGuard peers by key; peers without a probe report
// "none". The result is sorted by AppID.
func buildPeerStatuses(peers []*infra.Peer, states map[string]string) []daemon.PeerStatus {
	var out []daemon.PeerStatus
	for _, p := range peers {
		if p == nil || p.PublicKey == "" {
			continue
		}
		transport, ok := states[p.AppID]
		if !ok {
			transport = "none"
		}
		out = append(out, daemon.PeerStatus{
			AppID:     p.AppID,
			Name:      p.Name,
			PublicKey: p.PublicKey,
			Transport: transport,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppID < out[j].AppID })
	return out
}

// peerLabels turns the daemon's status reply into per-public-key labels for
// wireguard.PrintStatus. A nil info yields nil labels.
func peerLabels(info *daemon.StatusInfo) map[string]wireguard.PeerLabel {
	if info == nil {
		return nil
	}
	labels := make(map[string]wireguard.PeerLabel, len(info.Peers))
	for _, p := range info.Peers {
		name := p.AppID
		if p.Name != "" {
			name = p.Name
			if p.AppID != "" && p.AppID != p.Name {
				name = p.Name + " (" + p.AppID + ")"
			}
		}
		labels[p.PublicKey] = wireguard.PeerLabel{Name: name, Transport: p.Transport}
	}
	return labels
}
