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
	"context"
	"regexp"
	"strings"
)

// appIDUnsafe matches everything a NATS subject token may not contain
// (subjects are dot-separated; spaces and most specials are illegal).
var appIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// NormalizeAppID collapses a client-supplied AppID into a NATS-safe token.
// Device names arrive with spaces ("MacBook Pro", "iPhone 15 Pro Max"),
// which would produce illegal subjects like lattice.signals.peers.MacBook
// Pro.netmap and silently break per-peer push. Apply identically on the
// client (before register) and the server (at registration) so both sides
// derive the same subject for the same device.
func NormalizeAppID(id string) string {
	return appIDUnsafe.ReplaceAllString(strings.TrimSpace(id), "-")
}

// NetmapChangedSubject returns the NATS subject an agent with the given
// AppID subscribes to for "your netmap changed, refresh now" notifications.
// Deliberately a different subject than the per-peer ICE-signal channel
// (lattice.signals.peers.<appID>) so this never collides with
// signal.SignalPacket parsing on that channel.
func NetmapChangedSubject(appID string) string {
	return "lattice.signals.peers." + appID + ".netmap"
}

// PublishNetmapChanged notifies appID's agent that something in its netmap
// changed, so it should refresh sooner than its next poll/liveness cycle.
// The payload content doesn't matter today (the agent just re-fetches the
// full netmap on receipt) — a fixed "changed" body keeps this forward
// compatible if a reason ever needs to go in it later.
// A nil signal (e.g. a peerService instance constructed without one, see
// internal/server/service/token.go) is a silent no-op, not an error.
func PublishNetmapChanged(ctx context.Context, signal SignalService, appID string) error {
	if signal == nil {
		return nil
	}
	return signal.Publish(ctx, NetmapChangedSubject(appID), []byte("changed"))
}
