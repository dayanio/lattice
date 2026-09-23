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

package transport

import "testing"

func TestMergeAllowedIPs(t *testing.T) {
	cases := []struct {
		name            string
		existing, other string
		want            string
		wantContains    []string // used when order among merged-in entries is unspecified
		wantNotContains []string
	}{
		{
			name:     "self-description must not narrow widened exit entry",
			existing: "10.96.0.2/32,0.0.0.0/0",
			other:    "10.96.0.2/32",
			want:     "10.96.0.2/32,0.0.0.0/0",
		},
		{
			name:     "empty incoming keeps existing",
			existing: "10.96.0.2/32",
			other:    "",
			want:     "10.96.0.2/32",
		},
		{
			name:     "empty existing adopts incoming",
			existing: "",
			other:    "10.96.0.2/32",
			want:     "10.96.0.2/32",
		},
		{
			name:     "subnet route survives alongside exit route",
			existing: "10.96.0.2/32,0.0.0.0/0,192.168.1.0/24",
			other:    "10.96.0.2/32",
			want:     "10.96.0.2/32,0.0.0.0/0,192.168.1.0/24",
		},
		{
			name:            "new address from self-description is added",
			existing:        "10.96.0.2/32",
			other:           "10.96.0.9/32",
			wantNotContains: nil,
			wantContains:    []string{"10.96.0.2/32", "10.96.0.9/32"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeAllowedIPs(tc.existing, tc.other)
			if tc.want != "" && got != tc.want {
				t.Fatalf("mergeAllowedIPs(%q, %q) = %q, want %q", tc.existing, tc.other, got, tc.want)
			}
			for _, cidr := range tc.wantContains {
				if !containsCIDR(got, cidr) {
					t.Fatalf("mergeAllowedIPs(%q, %q) = %q, want it to contain %q", tc.existing, tc.other, got, cidr)
				}
			}
			for _, cidr := range tc.wantNotContains {
				if containsCIDR(got, cidr) {
					t.Fatalf("mergeAllowedIPs(%q, %q) = %q, want it to NOT contain %q", tc.existing, tc.other, got, cidr)
				}
			}
		})
	}
}

func containsCIDR(list, cidr string) bool {
	for _, part := range splitCSV(list) {
		if part == cidr {
			return true
		}
	}
	return false
}

func splitCSV(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
