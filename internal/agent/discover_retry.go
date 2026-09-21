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
	"errors"
	"time"

	"github.com/alatticeio/lattice/internal/agent/log"
)

// discoverBackoff is the base delay between discovery attempts (doubling).
var discoverBackoff = time.Second

// discoverAttempts bounds discovery retries at startup.
const discoverAttempts = 5

// discoverWithRetry is discover with retries for transient failures. The
// control plane is often reached over lossy paths (proxies, mobile links) that
// reset an occasional connection; a single dropped request must not abort
// agent startup. A well-formed answer without a NATS URL is a configuration
// problem and is returned immediately.
func discoverWithRetry(ctx context.Context, serverURL string) (discoveryResult, error) {
	var lastErr error
	delay := discoverBackoff
	for attempt := 1; attempt <= discoverAttempts; attempt++ {
		d, err := discover(ctx, serverURL)
		if err == nil {
			return d, nil
		}
		lastErr = err
		if errors.Is(err, errDiscoveryEmpty) || attempt == discoverAttempts {
			break
		}
		log.GetLogger("node").Warn("discovery failed, retrying", "attempt", attempt, "err", err, "in", delay)
		select {
		case <-ctx.Done():
			return discoveryResult{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return discoveryResult{}, lastErr
}
