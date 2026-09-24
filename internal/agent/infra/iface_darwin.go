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

package infra

import (
	"net"
	"syscall"
)

// DefaultPhysicalInterface returns the name and index of the first non-utun,
// non-loopback interface carrying a usable global IPv4 address — the physical
// uplink (en0 on a typical Mac). index 0 means "not found".
//
// Needed inside the NE packet-tunnel provider: macOS routes the provider's own
// sockets through its own tunnel (no automatic self-exemption), so WG/ICE UDP
// to the WG endpoint loops into the tunnel and dies; the sockets must be pinned
// to the physical uplink with IP_BOUND_IF.
func DefaultPhysicalInterface() (string, int) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", 0
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 {
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
			if b == nil {
				continue
			}
			// Skip link-local, loopback and the Lattice overlay range.
			if b[0] == 169 && b[1] == 254 || b[0] == 127 || b[0] == 10 && b[1] == 96 {
				continue
			}
			return ifc.Name, ifc.Index
		}
	}
	return "", 0
}

// BindUDPToInterface pins a UDP socket to the interface (IP_BOUND_IF).
func BindUDPToInterface(conn *net.UDPConn, index int) error {
	ri, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cerr error
	if err := ri.Control(func(fd uintptr) {
		cerr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, index)
	}); err != nil {
		return err
	}
	return cerr
}

// BindTCPToInterface pins a TCP socket to the interface (IP_BOUND_IF).
func BindTCPToInterface(conn *net.TCPConn, index int) error {
	ri, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var cerr error
	if err := ri.Control(func(fd uintptr) {
		cerr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_BOUND_IF, index)
	}); err != nil {
		return err
	}
	return cerr
}
