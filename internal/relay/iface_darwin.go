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

// BindToPhysicalIfc pins a TCP socket to the first physical uplink
// (IP_BOUND_IF); no-op when no uplink is found. See agent/infra for the
// rationale: inside the macOS NE provider, sockets otherwise route into the
// provider's own tunnel.
func BindToPhysicalIfc(conn *net.TCPConn) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		if len(ifc.Name) >= 4 && ifc.Name[:4] == "utun" {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			b := ipn.IP.To4()
			if b == nil || b[0] == 169 && b[1] == 254 || b[0] == 127 || b[0] == 10 && b[1] == 96 {
				continue
			}
			ri, err := conn.SyscallConn()
			if err != nil {
				return
			}
			_ = ri.Control(func(fd uintptr) {
				_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, ifc.Index)
			})
			return
		}
	}
}
