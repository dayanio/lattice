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
	"context"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
)

// RunNetmapSync periodically re-fetches the network map and re-applies it
// when the control plane's content-hash ConfigVersion changes. It is the
// pull-based convergence fallback for deployments without the K8s watch
// push channel (standalone mode): policy, peer and topology changes reach
// running agents even when the push path is absent.
//
// The loop never stops on errors — a failed fetch or apply is logged and
// retried on the next tick. Cancel ctx to stop.
func RunNetmapSync(ctx context.Context, interval time.Duration, fetch func() (*infra.Message, error), apply func(*infra.Message) error) {
	logger := log.GetLogger("netmap-sync")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastVersion string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msg, err := fetch()
			if err != nil {
				logger.Error("netmap sync: fetch failed", err)
				continue
			}
			if msg == nil {
				continue
			}
			if msg.ConfigVersion != "" && msg.ConfigVersion == lastVersion {
				continue
			}
			if err := apply(msg); err != nil {
				logger.Error("netmap sync: apply failed", err)
				continue
			}
			lastVersion = msg.ConfigVersion
		}
	}
}
