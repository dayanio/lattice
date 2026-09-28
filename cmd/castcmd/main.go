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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	natsgo "github.com/nats-io/nats.go"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func main() {
	natsURL := flag.String("nats", "nats://101.36.119.12:4222", "signaling NATS URL")
	pubKey := flag.String("pubkey", "", "target device WireGuard public key (base64, as the engine's PublicKey() reports it)")
	peerID := flag.Uint64("peerid", 0, "target device peer id (lattice.cast.<peerid>.cmd); wins over -pubkey")
	media := flag.String("media", "", "media URL for the play command (required)")
	title := flag.String("title", "cast spike", "media title")
	flag.Parse()

	if *media == "" || (*pubKey == "" && *peerID == 0) {
		fmt.Fprintln(os.Stderr, "usage: castcmd (-pubkey <base64 wg key> | -peerid <id>) -media <url> [-title t] [-nats url]")
		os.Exit(2)
	}
	var subject string
	if *peerID != 0 {
		subject = fmt.Sprintf("lattice.cast.%d.cmd", *peerID)
	} else {
		key, err := wgtypes.ParseKey(*pubKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "castcmd: parse pubkey: %v\n", err)
			os.Exit(2)
		}
		subject = fmt.Sprintf("lattice.cast.%s.cmd", infra.FromKey(key))
	}

	payload, err := json.Marshal(map[string]any{
		"action": "play",
		"url":    *media,
		"title":  *title,
		"ts":     time.Now().UnixMilli(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "castcmd: marshal: %v\n", err)
		os.Exit(2)
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
