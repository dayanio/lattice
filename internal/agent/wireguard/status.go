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
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const handshakeActiveThreshold = 3 * time.Minute

// PeerLabel is what the running agent knows about a peer beyond its WireGuard
// state: the name it is known by and its transport lifecycle state
// (probing, ice-ready, relay-ready, failed, closed, none).
type PeerLabel struct {
	Name      string
	Transport string
}

// PrintStatus prints the current WireGuard interface and peer status to stdout.
// labels, keyed by peer public key, adds agent-side context; it may be nil.
func PrintStatus(interfaceName string, labels map[string]PeerLabel) error {
	ctr, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("failed to open wgctrl: %w", err)
	}
	defer ctr.Close() //nolint:errcheck

	var devices []*wgtypes.Device
	if interfaceName != "" {
		var dev *wgtypes.Device
		dev, err = ctr.Device(interfaceName)
		if err != nil {
			return fmt.Errorf("interface %q not found: %w", interfaceName, err)
		}
		devices = []*wgtypes.Device{dev}
	} else {
		devices, err = ctr.Devices()
		if err != nil {
			return fmt.Errorf("failed to list WireGuard devices: %w", err)
		}
		if len(devices) == 0 {
			return fmt.Errorf("lattice is not running (no WireGuard interfaces found)")
		}
	}

	for _, dev := range devices {
		iface, err := net.InterfaceByName(dev.Name)
		var addrs []string
		if err == nil {
			ifAddrs, _ := iface.Addrs()
			for _, a := range ifAddrs {
				addrs = append(addrs, a.String())
			}
		}

		fmt.Printf("Interface : %s\n", dev.Name)
		if len(addrs) > 0 {
			fmt.Printf("Address   : %s\n", strings.Join(addrs, ", "))
		}
		fmt.Printf("Public Key: %s\n", dev.PublicKey.String())
		fmt.Printf("Port      : %d\n", dev.ListenPort)

		connected := 0
		for _, p := range dev.Peers {
			if !p.LastHandshakeTime.IsZero() && time.Since(p.LastHandshakeTime) < handshakeActiveThreshold {
				connected++
			}
		}
		fmt.Printf("\nPeers: %d total, %d connected\n", len(dev.Peers), connected)

		seen := map[string]bool{dev.PublicKey.String(): true}
		for _, p := range dev.Peers {
			key := p.PublicKey.String()
			seen[key] = true
			var label *PeerLabel
			if l, ok := labels[key]; ok {
				label = &l
			}
			writePeer(os.Stdout, p, label)
		}
		writeUnmatchedPeers(os.Stdout, labels, seen)
	}
	return nil
}

// Path values reported by actualPath.
const (
	pathDirect  = "direct"
	pathRelayed = "relayed"
)

// actualPath reports how traffic actually reaches a peer, judged by the
// WireGuard endpoint rather than the probe's state: WireGuard re-points a peer
// at whichever address its authenticated packets arrive from, so the probe can
// say "ice-ready" while the data flows through the relay (and the reverse).
// Empty when the peer has no endpoint yet.
func actualPath(endpoint *net.UDPAddr) string {
	if endpoint == nil {
		return ""
	}
	addr, ok := netip.AddrFromSlice(endpoint.IP)
	if !ok {
		return ""
	}
	if infra.IsRelayFakeAddr(addr.Unmap()) {
		return pathRelayed
	}
	return pathDirect
}

// pathDisagrees reports whether the probe's transport state contradicts the
// path WireGuard is really using.
func pathDisagrees(path, transport string) bool {
	return (transport == "ice-ready" && path == pathRelayed) ||
		(transport == "relay-ready" && path == pathDirect)
}

// transportDescription renders a transport state for humans.
func transportDescription(state string) string {
	switch state {
	case "ice-ready":
		return "ice-ready (direct)"
	case "relay-ready":
		return "relay-ready (relayed)"
	default:
		return state
	}
}

// writeUnmatchedPeers lists peers the agent knows about that have no
// WireGuard session (keys in labels but not in seen), so peers that never
// came up are visible instead of silently missing from the table.
func writeUnmatchedPeers(w io.Writer, labels map[string]PeerLabel, seen map[string]bool) {
	type row struct{ name, transport string }
	var rows []row
	for key, l := range labels {
		if seen[key] {
			continue
		}
		name := l.Name
		if name == "" {
			name = key
		}
		rows = append(rows, row{name, l.Transport})
	}
	if len(rows) == 0 {
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var b strings.Builder
	fmt.Fprintf(&b, "\nKnown peers without a WireGuard session: %d\n\n", len(rows))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-24s : %s\n", r.name, transportDescription(r.transport))
	}
	_, _ = io.WriteString(w, b.String())
}

func writePeer(w io.Writer, p wgtypes.Peer, label *PeerLabel) {
	status := "disconnected"
	handshakeStr := "never"
	if !p.LastHandshakeTime.IsZero() {
		elapsed := time.Since(p.LastHandshakeTime)
		handshakeStr = formatDuration(elapsed) + " ago"
		if elapsed < handshakeActiveThreshold {
			status = "connected"
		}
	}

	var allowedIPs []string
	for _, ip := range p.AllowedIPs {
		allowedIPs = append(allowedIPs, ip.String())
	}
	ipStr := strings.Join(allowedIPs, ", ")
	if ipStr == "" {
		ipStr = "(none)"
	}

	endpointStr := "(none)"
	if p.Endpoint != nil {
		endpointStr = p.Endpoint.String()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n  Peer      : %s\n", p.PublicKey.String())
	if label != nil && label.Name != "" {
		fmt.Fprintf(&b, "  Name      : %s\n", label.Name)
	}
	fmt.Fprintf(&b, "  Address   : %s\n", ipStr)
	fmt.Fprintf(&b, "  Endpoint  : %s\n", endpointStr)
	if label != nil {
		fmt.Fprintf(&b, "  Transport : %s\n", transportDescription(label.Transport))
	}
	path := actualPath(p.Endpoint)
	if path != "" {
		fmt.Fprintf(&b, "  Path      : %s\n", path)
		if label != nil && pathDisagrees(path, label.Transport) {
			fmt.Fprintf(&b, "  Note      : the path in use differs from the transport state (WireGuard re-points a peer at the address its packets arrive from)\n")
		}
	}
	fmt.Fprintf(&b, "  Handshake : %s\n", handshakeStr)
	fmt.Fprintf(&b, "  Traffic   : ↑ %s  ↓ %s\n", formatBytes(p.TransmitBytes), formatBytes(p.ReceiveBytes))
	fmt.Fprintf(&b, "  Status    : %s\n", status)
	_, _ = io.WriteString(w, b.String())
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
