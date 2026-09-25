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
	"net/netip"
	"sort"
	"strings"

	"github.com/alatticeio/lattice/apple/engine/split"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/overlay6"
)

// computeExtraRoutes returns the deduped, sorted set of CIDRs across all
// peers' AllowedIPs that are NOT a peer's own /32 overlay address — i.e.
// the routes a selected Exit Node / subnet-route provider has expanded
// into this node's netmap (see netmap_builder.go's per-recipient
// expansion on the server side). Plain /32 peer addresses need no extra
// OS route: they're already covered by the base 10.96.0.0/24 overlay
// route every tunnel installs regardless of this feature.
func computeExtraRoutes(peers []*infra.Peer) []string {
	seen := make(map[string]struct{})
	for _, p := range peers {
		if p == nil || p.AllowedIPs == "" {
			continue
		}
		peerAddr := ""
		if p.Address != nil {
			peerAddr = *p.Address
		}
		for _, cidr := range strings.Split(p.AllowedIPs, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" {
				continue
			}
			if cidr == peerAddr+"/32" {
				continue
			}
			// A peer's overlay IPv6 host route is, like its IPv4 /32, covered by
			// the tunnel's own address setup and needs no route of its own.
			if isOverlay6HostRoute(cidr) {
				continue
			}
			seen[cidr] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for cidr := range seen {
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out
}

// isOverlay6HostRoute reports whether cidr is a /128 inside the overlay IPv6
// prefix, i.e. some node's own overlay address.
func isOverlay6HostRoute(cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	return err == nil && p.Addr().Is6() && p.Bits() == 128 && overlay6.Contains(p.Addr())
}

// maxStaticExcludes caps how many CN blocks are handed to the OS as excluded
// routes. The M0 device experiment measured the full embedded set (~5,500
// blocks) installing in under a second without disturbing the tunnel, so the
// cap is disabled. A non-zero value would keep only the LARGEST blocks.
const maxStaticExcludes = 0

// routesPayload encodes what OnRoutesChanged carries. With nothing beyond the
// included routes it stays the legacy bare JSON array, so an older Swift side
// keeps decoding it; otherwise it is {"included":[...]} plus "excluded" (CIDRs
// that bypass the tunnel) and "overlay6" (this node's overlay IPv6, present when
// the Swift side should install IPv6 settings) when they apply.
func routesPayload(included, excluded []string, overlay6Addr string) (string, error) {
	if included == nil {
		included = []string{}
	}
	if len(excluded) == 0 && overlay6Addr == "" {
		b, err := json.Marshal(included)
		return string(b), err
	}
	b, err := json.Marshal(struct {
		Included []string `json:"included"`
		Excluded []string `json:"excluded,omitempty"`
		Overlay6 string   `json:"overlay6,omitempty"`
	}{included, excluded, overlay6Addr})
	return string(b), err
}

// splitExcluded returns the CN blocks to bypass the tunnel: only when the
// switch is on AND an exit node's default route (0.0.0.0/0) is in play. Any
// other situation returns nil — "no exclusions" is always the safe answer.
func splitExcluded(included []string, enabled bool, set *split.CNSet, budget int) []string {
	if !enabled || set == nil {
		return nil
	}
	for _, r := range included {
		if r == "0.0.0.0/0" {
			return set.Routes(budget)
		}
	}
	return nil
}

// routesSnapshot builds the OnRoutesChanged payload for the current state.
func routesSnapshot(included []string, enabled bool, set *split.CNSet, budget int, overlay6Addr string) (string, error) {
	return routesPayload(included, splitExcluded(included, enabled, set, budget), overlay6Addr)
}
