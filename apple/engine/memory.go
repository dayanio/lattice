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

import "runtime"

// memStats is a compact view of the Go runtime memory for the periodic
// diagnostics log, so extension memory growth can be attributed to the Go
// heap or to something outside it. (2026-09-30: the iOS extension footprint
// climbed 12 -> 48 MiB while the Go heap stayed at 2-3 MiB; the growth was
// in the Swift delivery loop, not here.)
type memStats struct {
	HeapAlloc  uint64
	HeapInuse  uint64
	StackInuse uint64
	Sys        uint64
	NumGC      uint32
	Goroutines int
}

func readMemStats() memStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return memStats{
		HeapAlloc:  m.HeapAlloc,
		HeapInuse:  m.HeapInuse,
		StackInuse: m.StackInuse,
		Sys:        m.Sys,
		NumGC:      m.NumGC,
		Goroutines: runtime.NumGoroutine(),
	}
}
