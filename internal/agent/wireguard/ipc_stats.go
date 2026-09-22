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

package wireguard

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// PeerStatsFromIpc extracts a peer's last handshake, received bytes and
// endpoint from the text a WireGuard device returns for an IpcGet. Unlike
// wgctrl it needs no UAPI socket file, so it works wherever the device runs
// in-process, including the iOS network extension, where the engine is built
// with NewNode and never opens a socket.
func PeerStatsFromIpc(ipc, publicKeyBase64 string) (lastHandshake time.Time, rxBytes uint64, endpoint *net.UDPAddr, err error) {
	key, err := wgtypes.ParseKey(publicKeyBase64)
	if err != nil {
		return time.Time{}, 0, nil, fmt.Errorf("public key: %w", err)
	}
	want := hex.EncodeToString(key[:])

	var (
		inPeer, found bool
		sec, nsec     int64
	)
	for _, line := range strings.Split(ipc, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			if found {
				break // the requested peer's section just ended
			}
			inPeer = v == want
			found = inPeer
			continue
		}
		if !inPeer {
			continue
		}
		switch k {
		case "endpoint":
			if ap, perr := netip.ParseAddrPort(v); perr == nil {
				endpoint = net.UDPAddrFromAddrPort(ap)
			}
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			if n, perr := strconv.ParseUint(v, 10, 64); perr == nil {
				rxBytes = n
			}
		}
	}
	if !found {
		return time.Time{}, 0, nil, fmt.Errorf("peer %s not configured", publicKeyBase64)
	}
	if sec != 0 || nsec != 0 {
		lastHandshake = time.Unix(sec, nsec)
	}
	return lastHandshake, rxBytes, endpoint, nil
}

// IpcPeerStats is one peer's WireGuard counters as reported by IpcGet.
type IpcPeerStats struct {
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

// PeerStatsAllFromIpc parses every peer section of an IpcGet dump, keyed by
// the peer public key's hex encoding (IpcGet emits keys hex-encoded).
// Device-level lines before the first public_key are ignored, and unknown or
// malformed lines are skipped, matching PeerStatsFromIpc's tolerance.
func PeerStatsAllFromIpc(ipc string) map[string]IpcPeerStats {
	out := map[string]IpcPeerStats{}
	var (
		curKey    string
		cur       IpcPeerStats
		sec, nsec int64
	)
	commit := func() {
		if curKey == "" {
			return
		}
		if sec != 0 || nsec != 0 {
			cur.LastHandshake = time.Unix(sec, nsec)
		}
		out[curKey] = cur
		curKey, cur, sec, nsec = "", IpcPeerStats{}, 0, 0
	}
	for _, line := range strings.Split(ipc, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if k == "public_key" {
			commit()
			curKey = v
			continue
		}
		if curKey == "" {
			continue
		}
		switch k {
		case "last_handshake_time_sec":
			sec, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_nsec":
			nsec, _ = strconv.ParseInt(v, 10, 64)
		case "rx_bytes":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				cur.RxBytes = n
			}
		case "tx_bytes":
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				cur.TxBytes = n
			}
		}
	}
	commit()
	return out
}
