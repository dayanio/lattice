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

//go:build !ios

package agent

import (
	"context"
	"time"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/telemetry"
	"golang.org/x/sync/errgroup"
)

// startTelemetry launches the Pro telemetry pipeline when the flags enable
// it. Split out of run.go behind a build tag: the VictoriaMetrics backend
// has no GOOS=ios implementation, and an iOS NE process has nothing to
// scrape anyway (see telemetry_noop.go).
func startTelemetry(ctx context.Context, g *errgroup.Group, c *Node, flags *config.Config, logger *log.Logger) {
	if !flags.EnableMetric || flags.Telemetry.VMEndpoint == "" {
		return
	}
	tc := telemetry.Config{
		VMEndpoint: flags.Telemetry.VMEndpoint,
		Interval:   time.Duration(flags.Telemetry.IntervalSeconds) * time.Second,
	}
	collector, err := telemetry.New(tc, c.GetPeerManager(), logger)
	if err != nil {
		logger.Warn("telemetry init failed, skipping", "err", err)
		return
	}
	// Resolve NetworkID after workspace config is applied; fall back to AppId namespace.
	networkID := ""
	if c.current != nil {
		networkID = c.current.NetworkId
	}
	collector.SetIdentity(telemetry.Identity{
		PeerID:    flags.AppId,
		NetworkID: networkID,
		Interface: c.GetDeviceName(),
	})
	g.Go(func() error { return collector.Run(ctx) })
}
