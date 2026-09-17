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
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/provision"
)

// A pending peer's netmap (Current set, Address a pointer to an empty
// string, no peers) must apply as a silent no-op — no error, no device
// writes. The handler is built with a nil deviceManager on purpose: the
// pending guard must early-return before any device access, so reaching
// the deviceManager at all panics and fails the test.
func TestApplyFullConfig_PendingPeerIsNoop(t *testing.T) {
	logger := log.GetLogger("test")
	// Same composition as node.go's production wiring, with a nil WireGuard
	// device: the pending guard must return before the device is touched.
	provisioner := provision.NewProvisioner(
		provision.NewRouteProvisioner(logger),
		provision.NewNoopEnforcer(logger),
		&provision.Params{})
	h := NewMessageHandler(nil, logger, provisioner)
	addr := ""
	msg := &infra.Message{Current: &infra.Peer{AppID: "self", Address: &addr}}
	if err := h.ApplyFullConfig(context.Background(), msg); err != nil {
		t.Fatalf("pending netmap must apply cleanly: %v", err)
	}
}
