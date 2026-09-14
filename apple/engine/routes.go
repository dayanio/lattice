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
	"sort"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
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
		for _, cidr := range strings.Split(p.AllowedIPs, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" || strings.HasSuffix(cidr, "/32") {
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
