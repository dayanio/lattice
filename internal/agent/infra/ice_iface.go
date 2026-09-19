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

package infra

import (
	"net"
	"strings"

	"github.com/pion/ice/v4"
)

// ICEInterfaceAllowed reports whether an interface may contribute ICE
// candidates. Virtual and tunnel interfaces are excluded: the mesh tunnel
// (wf0 on Linux, utunN on macOS) cannot carry its own transport, because
// connectivity checks sent to a mesh address are routed back into WireGuard
// and die there.
func ICEInterfaceAllowed(name string) bool {
	name = strings.ToLower(name)
	return !strings.Contains(name, "docker") &&
		!strings.Contains(name, "veth") &&
		!strings.Contains(name, "br-") &&
		!strings.HasPrefix(name, "wf") &&
		!strings.HasPrefix(name, "utun")
}

// filterListenAddrs drops mux listen addresses that ICE must not advertise:
// loopback and addresses owned by interfaces rejected by ICEInterfaceAllowed.
// ifaceOf resolves the interface owning an IP; unknown owners are kept.
//
// pion/ice ignores WithInterfaceFilter once a UDPMux is configured — the mux
// enumerates every local address itself — so the policy has to be applied to
// the mux's listen addresses. Loopback is dropped explicitly because pion only
// skips it for a bare *UDPMuxDefault, which the wrappers below are not.
func filterListenAddrs(addrs []net.Addr, ifaceOf func(net.IP) (string, bool)) []net.Addr {
	kept := make([]net.Addr, 0, len(addrs))
	for _, a := range addrs {
		udpAddr, ok := a.(*net.UDPAddr)
		if !ok {
			kept = append(kept, a)
			continue
		}
		if udpAddr.IP.IsLoopback() {
			continue
		}
		name := udpAddr.Zone
		if name == "" {
			name, _ = ifaceOf(udpAddr.IP)
		}
		if name != "" && !ICEInterfaceAllowed(name) {
			continue
		}
		kept = append(kept, a)
	}
	return kept
}

// localInterfaceOwners snapshots the interface owning each local IP.
func localInterfaceOwners() func(net.IP) (string, bool) {
	owners := make(map[string]string)
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				owners[ipn.IP.String()] = ifc.Name
			}
		}
	}
	return func(ip net.IP) (string, bool) {
		name, ok := owners[ip.String()]
		return name, ok
	}
}

// iceFilteredMux applies the ICE interface policy to a UDPMux's listen addresses.
type iceFilteredMux struct{ ice.UDPMux }

func (m iceFilteredMux) GetListenAddresses() []net.Addr {
	return filterListenAddrs(m.UDPMux.GetListenAddresses(), localInterfaceOwners())
}

// iceFilteredUniversalMux is iceFilteredMux for the server-reflexive mux.
type iceFilteredUniversalMux struct{ ice.UniversalUDPMux }

func (m iceFilteredUniversalMux) GetListenAddresses() []net.Addr {
	return filterListenAddrs(m.UniversalUDPMux.GetListenAddresses(), localInterfaceOwners())
}
