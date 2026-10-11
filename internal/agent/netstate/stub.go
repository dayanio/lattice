//go:build !linux

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

package netstate

// ApplyExitTakeover is a no-op off Linux: on macOS the NE tunnel and on
// Windows the TUN/wintun adapters are managed by the platform, which restores
// routing when the process dies.
func ApplyExitTakeover(r Runner, iface string) error { return nil }

// RemoveExitTakeover is a no-op off Linux.
func RemoveExitTakeover(r Runner) error { return nil }

// Cleanup is a no-op off Linux: no kernel state is written there today.
func Cleanup(r Runner) error { return nil }
