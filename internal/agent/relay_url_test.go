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

import "testing"

func TestResolveRelayURL(t *testing.T) {
	for name, tc := range map[string]struct{ override, advertised, want string }{
		"advertised used when no override":        {"", "relay.example:6266?token=a%2Bb", "relay.example:6266?token=a%2Bb"},
		"override inherits advertised token":      {"172.17.0.1:6266", "relay.example:6266?token=a%2Bb", "172.17.0.1:6266?token=a%2Bb"},
		"override keeps its own token":            {"172.17.0.1:6266?token=own", "relay.example:6266?token=adv", "172.17.0.1:6266?token=own"},
		"override without any advertised token":   {"172.17.0.1:6266", "relay.example:6266", "172.17.0.1:6266"},
		"override with no advertised url at all":  {"172.17.0.1:6266", "", "172.17.0.1:6266"},
		"nothing configured":                      {"", "", ""},
		"token found among other advertised keys": {"h:1", "r:2?x=y&token=t1&z=w", "h:1?token=t1"},
	} {
		if got := resolveRelayURL(tc.override, tc.advertised); got != tc.want {
			t.Errorf("%s: resolveRelayURL(%q, %q) = %q, want %q", name, tc.override, tc.advertised, got, tc.want)
		}
	}
}
