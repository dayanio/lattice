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

//go:build darwin

package relay

import (
	"net"
	"syscall"
)

// BindInterfaceName, when set, pins the relay TCP socket to that interface
// (IP_BOUND_IF) — same rationale as agent/infra: inside the macOS NE provider
// the relay socket would otherwise route into the provider's own tunnel.
var BindInterfaceName string

// BindToPhysicalIfc pins a TCP socket to BindInterfaceName when set, else to
// the first physical uplink found; no-op when neither resolves.
func BindToPhysicalIfc(conn *net.TCPConn) {
	name := BindInterfaceName
	if name == "" {
		ifaces, err := net.Interfaces()
		if err != nil {
			return
		}
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || len(ifc.Name) < 4 || ifc.Name[:4] == "utun" {
				continue
			}
			addrs, err := ifc.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok {
					if b := ipn.IP.To4(); b != nil && b[0] != 127 && !(b[0] == 169 && b[1] == 254) && !(b[0] == 10 && b[1] == 96) {
						name = ifc.Name
					}
				}
			}
		}
	}
	if name == "" {
		return
	}
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return
	}
	ri, err := conn.SyscallConn()
	if err != nil {
		return
	}
	_ = ri.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, ifc.Index)
	})
}
