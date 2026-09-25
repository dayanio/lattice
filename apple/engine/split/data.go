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
	_ "embed"
	"sync"
)

//go:embed data/cn_ipv4.txt
var cnIPv4Data string

var (
	defaultOnce sync.Once
	defaultSet  *CNSet
)

// DefaultCNSet returns the CN block set built into the engine. It parses the
// embedded data once and returns the same instance afterwards. If the data is
// missing or corrupt the set is simply empty: split routing then excludes
// nothing and every flow keeps using the tunnel.
func DefaultCNSet() *CNSet {
	defaultOnce.Do(func() { defaultSet = ParseCNSet(cnIPv4Data) })
	return defaultSet
}
