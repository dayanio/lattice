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

// castcmd publishes a cast command to a device over the Lattice NATS
// signaling plane — the push transport binding for cast (Phase 0b test
// tool): the target device's engine subscribes lattice.cast.<peerid>.cmd,
// so no inbound listener is needed on the renderer. The peer id is derived
// from the device's WireGuard public key exactly like the signaling subject
// (infra.FromKey).
//
// Usage:
//
//	go run ./cmd/castcmd -pubkey <device wg public key base64> \
//	    -media https://example.com/movie.mp4 -title "demo"
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	natsgo "github.com/nats-io/nats.go"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func main() {
	natsURL := flag.String("nats", "nats://101.36.119.12:4222", "signaling NATS URL (fallback transport)")
	pubKey := flag.String("pubkey", "", "target device WireGuard public key (base64, as the engine's PublicKey() reports it)")
	peerID := flag.Uint64("peerid", 0, "target device peer id (lattice.cast.<peerid>.cmd); wins over -pubkey")
	appID := flag.String("appid", "", "target device AppID (lattice.cast.<appid>.cmd); wins over -peerid")
	media := flag.String("media", "", "media URL for the play command (required)")
	title := flag.String("title", "cast spike", "media title")
	transport := flag.String("transport", "nats", "nats | overlay — overlay sends a UDP datagram to the device's overlay IP on the reserved engine port and waits for the in-engine ACK")
	target := flag.String("target", "10.96.0.4:47822", "overlay target for -transport overlay (device overlay IP : reserved port 47822)")
	retries := flag.Int("retries", 3, "overlay retransmit count while no ACK arrives")
	flag.Parse()

	if *media == "" || (*pubKey == "" && *peerID == 0 && *appID == "") {
		fmt.Fprintln(os.Stderr, "usage: castcmd (-pubkey <key> | -peerid <id> | -appid <id> | -transport overlay) -media <url> [-title t] [-nats url]")
		os.Exit(2)
	}

	cmdID := randomID()
	payload, err := json.Marshal(map[string]any{
		"id":     cmdID,
		"action": "play",
		"url":    *media,
		"title":  *title,
		"ts":     time.Now().UnixMilli(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: marshal: %v\n", err)
		os.Exit(2)
	}

	if *transport == "overlay" {
		sendOverlay(*target, *retries, payload, cmdID)
		return
	}

	var subject string
	switch {
	case *appID != "":
		subject = fmt.Sprintf("lattice.cast.%s.cmd", *appID)
	case *peerID != 0:
		subject = fmt.Sprintf("lattice.cast.%d.cmd", *peerID)
	default:
		key, keyErr := wgtypes.ParseKey(*pubKey)
		if keyErr != nil {
			fmt.Fprintf(os.Stderr, "castcmd: parse pubkey: %v\n", keyErr)
			os.Exit(2)
		}
		subject = fmt.Sprintf("lattice.cast.%s.cmd", infra.FromKey(key))
	}

	nc, err := natsgo.Connect(*natsURL, natsgo.Timeout(10*time.Second))
	if err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: connect %s: %v\n", *natsURL, err)
		os.Exit(1)
	}
	defer nc.Close()
	if err := nc.Publish(subject, payload); err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: publish: %v\n", err)
		os.Exit(1)
	}
	if err := nc.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: flush: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("published to %s: %s\n", subject, payload)
}

// sendOverlay sends the command as a UDP datagram to the device's overlay
// IP on the reserved engine port and waits for the in-engine {"ack":id}.
// The Mac's OS routes the datagram into the lattice TUN (10.96.0.0/24), so
// no special sender support is needed — plain sockets, kernel routing.
func sendOverlay(target string, retries int, payload []byte, cmdID string) {
	raddr, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: resolve %s: %v\n", target, err)
		os.Exit(2)
	}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: dial %s: %v\n", target, err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadBuffer(1024)

	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		if _, err := conn.Write(payload); err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "castcmd: attempt %d write: %v\n", attempt, err)
			time.Sleep(time.Second)
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		n, err := conn.Read(buf)
		if err != nil {
			lastErr = err
			fmt.Fprintf(os.Stderr, "castcmd: attempt %d no ack (%v), retrying\n", attempt, err)
			continue
		}
		var ack struct {
			Ack string `json:"ack"`
		}
		_ = json.Unmarshal(buf[:n], &ack)
		fmt.Printf("overlay %s: acked id=%q after %d attempt(s): %s\n", target, ack.Ack, attempt, payload)
		return
	}
	fmt.Fprintf(os.Stderr, "castcmd: overlay %s: no ack after %d attempts (last: %v) — overlay path down? falling back to nats transport would be the gateway's job\n", target, retries, lastErr)
	os.Exit(1)
}

// randomID returns 16 hex chars of crypto randomness for command dedup.
func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
