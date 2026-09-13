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

// Package metrics is the platform-neutral counter surface for packages that
// must build on every platform the engine targets — including GOOS=ios,
// where VictoriaMetrics/metrics has no implementation (process metrics are
// linux/darwin-only). Non-iOS builds delegate to VictoriaMetrics; iOS gets a
// no-op (an NE process has nothing to scrape).
package metrics

// Counter is the minimal counter surface used by engine packages.
type Counter interface {
	Inc()
	Add(delta int)
}

// NewCounter creates (and registers, where a backend exists) a counter.
func NewCounter(name string) Counter { return newCounter(name) }
