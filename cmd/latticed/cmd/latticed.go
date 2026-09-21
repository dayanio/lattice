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

package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/controller"
	internalnats "github.com/alatticeio/lattice/internal/agent/nats"
	"github.com/alatticeio/lattice/internal/db"
	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/alatticeio/lattice/internal/relay"
	"github.com/alatticeio/lattice/internal/server"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/go-logr/logr"

	"golang.org/x/sync/errgroup"
)

const embeddedNATSPort = 4222

func runLatticed(flags *config.Config) error {
	// 1. Create a global context that responds to system signals (Ctrl+C)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)

	fmt.Println("Latticed is starting all-in-one mode...")

	// 2. Start the embedded NATS (infrastructure); natsReady closes after NATS is ready
	natsReady := make(chan struct{})
	g.Go(func() error {
		fmt.Println("Starting embedded NATS server...")
		return internalnats.RunEmbedded(ctx, embeddedNATSPort, natsReady)
	})

	// 3. Initialize the database (SQLite open-source default, MariaDB for production)
	fmt.Println("Initializing storage...")
	st, err := db.NewStore(flags)
	if err != nil {
		return fmt.Errorf("failed to init db: %w", err)
	}

	// 4. Control plane logic layer: standalone (DB-backed) or K8s controller.
	if flags.Standalone {
		// Relay: standalone deployments run the Relay relay in-process so
		// peers that ICE cannot traverse (containers, double-NAT) still
		// connect. Advertise it in netmaps unless the operator pinned one.
		if flags.RelayAdvertiseURL == "" {
			flags.RelayAdvertiseURL = "127.0.0.1:6266"
		}
		relayFlags := *flags
		relayFlags.Listen = ":6266" // relayer's default relay port
		g.Go(func() error {
			rs := relay.NewServer(&relayFlags)
			return rs.Start()
		})
		g.Go(func() error {
			resync := reconcile.DefaultResyncInterval
			if flags.ResyncInterval != "" {
				if d, parseErr := time.ParseDuration(flags.ResyncInterval); parseErr == nil && d > 0 {
					resync = d
				}
			}
			fmt.Printf("Starting standalone reconcile runner (identity TTL, policy TTL, resync %s)...\n", resync)
			runner := reconcile.NewRunner()
			if err := reconcilers.RegisterAllWithResync(runner, st, resync, logr.Discard()); err != nil {
				return fmt.Errorf("register reconcilers: %w", err)
			}
			return runner.Start(ctx)
		})
	} else {
		g.Go(func() error {
			fmt.Println("Starting Lattice Controllers...")
			return controller.Start(flags)
		})
	}

	// 5. Wait for NATS to be ready before starting management
	g.Go(func() error {
		select {
		case <-natsReady:
		case <-ctx.Done():
			return ctx.Err()
		}
		fmt.Println("Starting Lattice Manager...")
		// In all-in-one mode, if the user has not configured signaling-url, use the embedded NATS address
		if flags.SignalingURL == "" {
			flags.SignalingURL = fmt.Sprintf("nats://localhost:%d", embeddedNATSPort)
		}
		return management.Start(flags)
	})

	// 6. Wait for all components to run, or exit if one of them reports an error
	fmt.Println("All systems go! Latticed is ready.")

	if err := g.Wait(); err != nil {
		return fmt.Errorf("latticed stopped with error: %w", err)
	}

	fmt.Println("Latticed stopped gracefully.")
	return nil
}
