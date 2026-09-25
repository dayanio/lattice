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

package split

import (
	"net"
	"strings"
	"testing"
)

func TestDefaultCNSet_EmbeddedData(t *testing.T) {
	s := DefaultCNSet()
	if s.Len() < 3000 {
		t.Fatalf("embedded CN set has %d blocks, expected thousands — regenerate with hack/scripts/gen-cn-lists.sh", s.Len())
	}
	for _, ip := range []string{"114.114.114.114", "223.5.5.5"} {
		if !s.Contains(net.ParseIP(ip)) {
			t.Errorf("%s (a CN resolver) should be in the CN set", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "142.250.80.46", "10.96.0.1", "192.168.1.1"} {
		if s.Contains(net.ParseIP(ip)) {
			t.Errorf("%s must NOT be in the CN set", ip)
		}
	}
	if DefaultCNSet() != s {
		t.Error("DefaultCNSet must return the same instance on every call")
	}
}

func TestEmbeddedData_NothingWasDroppedByTheReservedFilter(t *testing.T) {
	// The generator writes only clean blocks; if ParseCNSet drops any line the
	// data file and the filter disagree.
	lines := 0
	for _, l := range strings.Split(cnIPv4Data, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			lines++
		}
	}
	if got := DefaultCNSet().Len(); got != lines {
		t.Fatalf("data has %d blocks but the parsed set has %d", lines, got)
	}
}
