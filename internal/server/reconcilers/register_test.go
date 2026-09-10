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

package reconcilers_test

import (
	"context"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAll_RegistersBothKinds(t *testing.T) {
	st := newTestStore(t)
	runner := reconcile.NewRunner(reconcile.WithClock(reconcile.NewFakeClock(time.Now())))

	require.NoError(t, reconcilers.RegisterAll(runner, st, logr.Discard()))
	assert.False(t, runner.Started(), "RegisterAll must not start the runner")
}

// TestIdentityLifecycleEndToEnd is the M2 acceptance test from the design
// doc: an AgentIdentity with a TTL converges Pending→Active→Expired in the
// database, and a PeerIdentity grace period clears once it expires — all
// through the real gormstore and the reconcile Runner, without K8s.
func TestIdentityLifecycleEndToEnd(t *testing.T) {
	clk := reconcile.NewFakeClock(time.Now())
	st := newTestStore(t)
	runner := reconcile.NewRunner(reconcile.WithClock(clk), reconcile.WithWorkers(2))
	require.NoError(t, reconcilers.RegisterAll(runner, st, logr.Discard(), reconcilers.WithNow(clk.Now)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Start(ctx) }()
	t.Cleanup(cancel)

	// AgentIdentity with a 60s virtual TTL.
	m := &models.AgentIdentity{
		TenantID: "t1", Name: "agent-1", PeerRef: "p1",
		Phase: "Pending", ExpiresAt: timePtr(clk.Now().Add(60 * time.Second)),
	}
	require.NoError(t, st.AgentIdentities().Create(ctx, m))
	runner.Notify(reconcilers.KindAgentIdentity, m.ID)

	waitFor(t, 2*time.Second, func() bool {
		got, err := st.AgentIdentities().GetByID(ctx, m.ID)
		return err == nil && got.Phase == "Active"
	}, "identity should activate on first reconcile")

	// Cross the TTL expiry and let the scheduled requeue fire.
	time.Sleep(20 * time.Millisecond) // allow the requeue timer to be armed
	clk.Advance(61 * time.Second)
	waitFor(t, 2*time.Second, func() bool {
		got, err := st.AgentIdentities().GetByID(ctx, m.ID)
		return err == nil && got.Phase == "Expired"
	}, "identity should expire after its TTL")

	// PeerIdentity with an already-expired grace period.
	pi := &models.PeerIdentity{
		NetworkID:            "net-1",
		Name:                 "prod-db",
		PeerRef:              "node-v2",
		PreviousPeerRef:      "node-v1",
		PreviousPeerIP:       "10.0.0.4",
		GracePeriodSeconds:   300,
		GracePeriodExpiresAt: timePtr(clk.Now().Add(-time.Second)),
	}
	require.NoError(t, st.PeerIdentities().Create(ctx, pi))
	runner.Notify(reconcilers.KindPeerIdentity, pi.ID)

	waitFor(t, 2*time.Second, func() bool {
		got, err := st.PeerIdentities().GetByID(ctx, pi.ID)
		return err == nil && got.PreviousPeerRef == "" && got.GracePeriodExpiresAt == nil
	}, "expired grace period should be cleared")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop after cancel")
	}
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, msg)
}
