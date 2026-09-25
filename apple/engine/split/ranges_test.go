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
	"reflect"
	"strings"
	"testing"
)

const apnicSample = `2|apnic|20260925|123456|19830613|20260924|+1000
apnic|*|ipv4|*|9000|summary
# a comment
apnic|CN|ipv4|1.0.1.0|256|20110414|allocated
apnic|CN|ipv4|1.0.2.0|512|20110414|allocated
apnic|JP|ipv4|1.0.16.0|4096|20110412|allocated
apnic|CN|ipv6|2001:250::|32|20000101|allocated
apnic|CN|ipv4|bad|256|20110414|allocated
apnic|CN|ipv4|2.0.0.0|0|20110414|allocated
apnic|CN|ipv4|255.255.255.0|512|20110414|allocated
apnic|CN|ipv4|3.0.0.0|256

apnic|CN|ipv4|4.0.0.0|256|20110414|reserved
`

func TestParseAPNIC_KeepsOnlyCNIPv4AndSkipsDirtyLines(t *testing.T) {
	got, err := ParseAPNIC(strings.NewReader(apnicSample), "CN")
	if err != nil {
		t.Fatal(err)
	}
	// 1.0.1.0/24 and 1.0.2.0/23 survive; everything else (other country, ipv6,
	// bad address, zero count, overflow past 255.255.255.255, short line, reserved) is skipped.
	want := []Range{
		{Start: 0x01000100, End: 0x010001FF},
		{Start: 0x01000200, End: 0x010003FF},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAPNIC() = %v, want %v", got, want)
	}
}

func TestMergeRanges_MergesAdjacentAndOverlapping(t *testing.T) {
	in := []Range{
		{Start: 0x01000200, End: 0x010003FF},
		{Start: 0x01000100, End: 0x010001FF}, // adjacent to the one above once sorted
		{Start: 0x01000300, End: 0x010004FF}, // overlaps
		{Start: 0x0A000000, End: 0x0A0000FF},
	}
	got := MergeRanges(in)
	want := []Range{
		{Start: 0x01000100, End: 0x010004FF},
		{Start: 0x0A000000, End: 0x0A0000FF},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeRanges() = %v, want %v", got, want)
	}
}

func TestMergeRanges_HandlesTopOfAddressSpace(t *testing.T) {
	got := MergeRanges([]Range{{Start: 0xFFFFFF00, End: 0xFFFFFFFF}, {Start: 0xFFFFFE00, End: 0xFFFFFEFF}})
	want := []Range{{Start: 0xFFFFFE00, End: 0xFFFFFFFF}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeRanges() = %v, want %v", got, want)
	}
}

func TestRangeToCIDRs(t *testing.T) {
	cases := []struct {
		name string
		in   Range
		want []string
	}{
		{"aligned /24", Range{0x01000000, 0x010000FF}, []string{"1.0.0.0/24"}},
		{"unaligned run splits into two blocks", Range{0x01000000, 0x010002FF}, []string{"1.0.0.0/23", "1.0.2.0/24"}},
		{"single address", Range{0x01020304, 0x01020304}, []string{"1.2.3.4/32"}},
		{"starts mid-block", Range{0x01000100, 0x010003FF}, []string{"1.0.1.0/24", "1.0.2.0/23"}},
		{"top of space", Range{0xFFFFFFFF, 0xFFFFFFFF}, []string{"255.255.255.255/32"}},
	}
	for _, c := range cases {
		if got := RangeToCIDRs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: RangeToCIDRs(%v) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}
