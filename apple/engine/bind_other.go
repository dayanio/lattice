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

//go:build !darwin

package engine

import (
	"errors"
	"syscall"
)

// boundToInterface has no implementation off darwin (IP_BOUND_IF is a darwin
// socket option). The returned Control fails the dial rather than letting an
// unbound socket through: a query that was meant to be pinned to the tunnel
// but goes out the default route would silently do the wrong thing.
func boundToInterface(int) func(network, address string, c syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error {
		return errors.New("binding a socket to an interface is only supported on darwin")
	}
}
