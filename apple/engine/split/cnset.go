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
	"fmt"
	"net"
	"sort"
	"strings"
)

// cidr is one IPv4 block: the first address and the prefix length.
type cidr struct {
	start uint32
	bits  int
}

func (c cidr) end() uint32 {
	if c.bits == 0 {
		return 0xFFFFFFFF
	}
	return c.start | (uint32(1)<<(32-c.bits) - 1)
}

func (c cidr) String() string { return fmt.Sprintf("%s/%d", uintToIP(c.start), c.bits) }

// reserved lists ranges that must never be excluded from the tunnel: the
// overlay itself (10.96.0.0/24 lives in 10/8), private space, loopback,
// link-local, CGNAT and multicast/reserved. A CN block that touches any of
// them is dropped whole — an exclusion here could black-hole local traffic.
var reserved = func() []Range {
	var out []Range
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/3",
	} {
		c, ok := parseCIDR(s)
		if !ok {
			panic("split: bad reserved range " + s)
		}
		out = append(out, Range{Start: c.start, End: c.end()})
	}
	return out
}()

func parseCIDR(s string) (cidr, bool) {
	ip, n, err := net.ParseCIDR(strings.TrimSpace(s))
	if err != nil || ip.To4() == nil {
		return cidr{}, false
	}
	ones, size := n.Mask.Size()
	if size != 32 {
		return cidr{}, false
	}
	return cidr{start: ipToUint(n.IP), bits: ones}, true
}

func touchesReserved(c cidr) bool {
	for _, r := range reserved {
		if c.start <= r.End && c.end() >= r.Start {
			return true
		}
	}
	return false
}

// CNSet is an immutable set of CN IPv4 blocks, sorted and non-overlapping.
// A nil *CNSet is a valid empty set.
type CNSet struct{ blocks []cidr }

// ParseCNSet reads one CIDR per line ('#' starts a comment). Invalid lines,
// non-IPv4 entries, blocks touching reserved space, and blocks nested inside
// an earlier one are dropped silently: the set can only ever get smaller than
// its input, which keeps failures on the "more traffic uses the tunnel" side.
func ParseCNSet(text string) *CNSet {
	var blocks []cidr
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		c, ok := parseCIDR(line)
		if !ok || touchesReserved(c) {
			continue
		}
		blocks = append(blocks, c)
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].start != blocks[j].start {
			return blocks[i].start < blocks[j].start
		}
		return blocks[i].bits < blocks[j].bits // larger block first
	})
	kept := blocks[:0]
	for _, c := range blocks {
		if n := len(kept); n > 0 && c.start <= kept[n-1].end() {
			continue // nested (CIDR blocks are either nested or disjoint)
		}
		kept = append(kept, c)
	}
	return &CNSet{blocks: kept}
}

// Len is the number of blocks.
func (s *CNSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.blocks)
}

// Contains reports whether ip is an IPv4 address inside the set.
func (s *CNSet) Contains(ip net.IP) bool {
	if s == nil || ip.To4() == nil {
		return false
	}
	v := ipToUint(ip)
	i := sort.Search(len(s.blocks), func(i int) bool { return s.blocks[i].start > v }) - 1
	return i >= 0 && v <= s.blocks[i].end()
}

// Routes returns the blocks as CIDR strings in address order. With max > 0 and
// more than max blocks, it keeps the max LARGEST ones: those cover the most
// address space per route, and the dropped small blocks simply keep using the
// tunnel (the safe direction).
func (s *CNSet) Routes(max int) []string {
	if s == nil {
		return nil
	}
	sel := s.blocks
	if max > 0 && len(sel) > max {
		sel = append([]cidr(nil), s.blocks...)
		sort.SliceStable(sel, func(i, j int) bool { return sel[i].bits < sel[j].bits })
		sel = sel[:max]
		sort.Slice(sel, func(i, j int) bool { return sel[i].start < sel[j].start })
	}
	out := make([]string, len(sel))
	for i, c := range sel {
		out[i] = c.String()
	}
	return out
}
