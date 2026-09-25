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

// Package split holds the data and logic behind "China direct" routing: the
// set of CN IPv4 blocks that should bypass the exit-node tunnel.
package split

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Range is an inclusive IPv4 address range.
type Range struct{ Start, End uint32 }

func ipToUint(ip net.IP) uint32 { return binary.BigEndian.Uint32(ip.To4()) }

func uintToIP(v uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, v)
	return ip
}

// ParseAPNIC reads an APNIC "delegated" file and returns the IPv4 ranges
// allocated or assigned to country cc. Comments, summary lines, other
// countries, IPv6, and anything malformed are skipped rather than failing:
// the input is a third-party file and a bad line must never break the build
// of the route set.
func ParseAPNIC(r io.Reader, cc string) ([]Range, error) {
	var out []Range
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "|")
		if len(f) < 7 || f[1] != cc || f[2] != "ipv4" {
			continue
		}
		if f[6] != "allocated" && f[6] != "assigned" {
			continue
		}
		ip := net.ParseIP(f[3]).To4()
		count, err := strconv.ParseUint(f[4], 10, 32)
		if ip == nil || err != nil || count == 0 {
			continue
		}
		start := ipToUint(ip)
		end := uint64(start) + count - 1
		if end > 0xFFFFFFFF {
			continue
		}
		out = append(out, Range{Start: start, End: uint32(end)})
	}
	return out, sc.Err()
}

// MergeRanges sorts the ranges and merges any that overlap or touch.
func MergeRanges(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]Range(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	out := []Range{sorted[0]}
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		// uint64 so End+1 cannot wrap at 255.255.255.255.
		if uint64(r.Start) <= uint64(last.End)+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// RangeToCIDRs covers r with the fewest aligned CIDR blocks.
func RangeToCIDRs(r Range) []string {
	var out []string
	start, end := uint64(r.Start), uint64(r.End)
	for start <= end {
		size := uint64(1) << 32
		if start != 0 {
			size = start & -start // largest block aligned at start
		}
		for size > end-start+1 {
			size >>= 1
		}
		prefix := 33 - bits.Len64(size) // size is a power of two: /32 for 1, /0 for 2^32
		out = append(out, fmt.Sprintf("%s/%d", uintToIP(uint32(start)), prefix))
		start += size
	}
	return out
}
