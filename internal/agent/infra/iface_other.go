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

package infra

import "net"

// DefaultPhysicalInterface is a no-op off darwin: the self-tunnel capture the
// darwin variant works around is an NE packet-tunnel provider behavior. index
// 0 means "not found / no binding needed".
func DefaultPhysicalInterface() (string, int) {
	return "", 0
}

// BindUDPToInterface is a no-op off darwin.
func BindUDPToInterface(conn *net.UDPConn, index int) error { return nil }

// BindTCPToInterface is a no-op off darwin.
func BindTCPToInterface(conn *net.TCPConn, index int) error { return nil }
