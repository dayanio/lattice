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

package relay

import "net"

// BindInterfaceName is only honoured on darwin (see iface_darwin.go); it is
// declared here so callers such as apple/engine compile on every platform.
var BindInterfaceName string

// BindToPhysicalIfc is a no-op off darwin.
func BindToPhysicalIfc(conn *net.TCPConn) {}
