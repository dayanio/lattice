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
	"testing"
)

func TestICEInterfaceAllowed(t *testing.T) {
	for name, want := range map[string]bool{
		"en0": true, "eth0": true, "wlan0": true, "pdp_ip0": true,
		"wf0": false, "utun4": false, "UTUN5": false,
		"docker0": false, "br-8ddf6efbd0a8": false, "veth8ef18b6f": false,
	} {
		if got := ICEInterfaceAllowed(name); got != want {
			t.Errorf("ICEInterfaceAllowed(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFilterListenAddrs(t *testing.T) {
	owners := map[string]string{
		"192.168.1.8": "en0",
		"10.96.0.4":   "utun4",
		"198.18.0.1":  "utun5",
		"172.17.0.1":  "docker0",
		"10.96.0.2":   "wf0",
		"127.0.0.1":   "lo0",
	}
	ifaceOf := func(ip net.IP) (string, bool) {
		name, ok := owners[ip.String()]
		return name, ok
	}
	in := []net.Addr{
		&net.UDPAddr{IP: net.ParseIP("192.168.1.8"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("10.96.0.4"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("198.18.0.1"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("172.17.0.1"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("10.96.0.2"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("203.0.113.9"), Port: 51820},
		&net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 51820, Zone: "en0"},
		&net.UDPAddr{IP: net.ParseIP("fe80::2"), Port: 51820, Zone: "utun4"},
	}

	got := filterListenAddrs(in, ifaceOf)

	want := []string{"192.168.1.8:51820", "203.0.113.9:51820", "[fe80::1%en0]:51820"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("kept[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestFilteringUDPMuxListenAddressesSkipDisallowedInterfaces(t *testing.T) {
	conn, _, err := ListenUDP("udp4", 0)
	if err != nil {
		t.Fatal(err)
	}
	mux := NewFilteringUDPMux(conn, nil)
	t.Cleanup(func() { _ = mux.Close() })

	ownerOf := func(ip net.IP) string {
		ifaces, _ := net.Interfaces()
		for _, ifc := range ifaces {
			addrs, _ := ifc.Addrs()
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
					return ifc.Name
				}
			}
		}
		return ""
	}

	for name, addrs := range map[string][]net.Addr{
		"host mux":  mux.UDPMux().GetListenAddresses(),
		"srflx mux": mux.UDPMuxSrflx().GetListenAddresses(),
	} {
		for _, a := range addrs {
			ip := a.(*net.UDPAddr).IP
			if ip.IsLoopback() {
				t.Errorf("%s advertises loopback %s", name, ip)
			}
			if ifc := ownerOf(ip); ifc != "" && !ICEInterfaceAllowed(ifc) {
				t.Errorf("%s advertises %s from filtered interface %s", name, ip, ifc)
			}
		}
	}
}
