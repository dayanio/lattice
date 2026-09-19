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

package agent

import (
	"net"
	"strings"
)

// resolveRelayURL picks the relay address for the LRP client. An explicit
// override wins: the advertised address may be unreachable from this network
// (e.g. a container that cannot hairpin to its host's public IP). The
// override inherits the advertised token when it carries none of its own, so
// operators need not copy the shared secret into every launch command.
func resolveRelayURL(override, advertised string) string {
	// relay-url doubles as the relay server's listen address, so agent configs
	// written from server defaults carry ":6266" or "0.0.0.0:6266". That is
	// not dialable (it would reach whatever relay runs on localhost), and must
	// not shadow the relay the control plane advertises.
	if advertised != "" && isListenAddress(override) {
		override = ""
	}
	if override == "" {
		return advertised
	}
	if rawToken(override) != "" {
		return override
	}
	if tok := rawToken(advertised); tok != "" {
		sep := "?"
		if strings.Contains(override, "?") {
			sep = "&"
		}
		return override + sep + "token=" + tok
	}
	return override
}

// isListenAddress reports whether addr has an empty or wildcard host.
func isListenAddress(addr string) bool {
	hostPort, _, _ := strings.Cut(addr, "?")
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return false
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// rawToken returns the still-escaped value of the "token" query key.
func rawToken(addr string) string {
	_, query, ok := strings.Cut(addr, "?")
	if !ok {
		return ""
	}
	for _, kv := range strings.Split(query, "&") {
		if v, found := strings.CutPrefix(kv, "token="); found {
			return v
		}
	}
	return ""
}
