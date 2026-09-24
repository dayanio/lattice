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
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingHandler is a Handler that records the versions it was asked to apply.
type recordingHandler struct {
	applied []string
	err     error
}

func (h *recordingHandler) HandleEvent(context.Context, *infra.Message) error { return nil }

func (h *recordingHandler) ApplyFullConfig(_ context.Context, msg *infra.Message) error {
	if h.err != nil {
		return h.err
	}
	h.applied = append(h.applied, msg.ConfigVersion)
	return nil
}

// newRefreshNode returns a Node whose control plane serves whatever *current
// points at, standing in for the server-side netmap.
func newRefreshNode(h *recordingHandler, current **infra.Message) *Node {
	return &Node{
		logger:         log.GetLogger("refresh-test"),
		messageHandler: h,
		GetNetworkMap:  func() (*infra.Message, error) { return *current, nil },
	}
}

// The exit-node round trip: connect with no exit node (V0), select one (V1),
// switch back to direct (V0). Returning to the version that was recorded at
// connect time used to be skipped as "already applied" because RefreshConfig
// applied V1 without recording it, so the exit route stayed installed on the
// device after the server had withdrawn it.
func TestRefreshConfig_ReturningToTheStartVersionIsApplied(t *testing.T) {
	h := &recordingHandler{}
	msg := &infra.Message{ConfigVersion: "v0-no-exit"}
	n := newRefreshNode(h, &msg)
	ctx := context.Background()

	n.setAppliedVersion("v0-no-exit") // what Start records after the first apply

	msg = &infra.Message{ConfigVersion: "v1-exit-selected"}
	require.NoError(t, n.RefreshConfig(ctx))

	msg = &infra.Message{ConfigVersion: "v0-no-exit"} // back to direct
	require.NoError(t, n.RefreshConfig(ctx))

	assert.Equal(t, []string{"v1-exit-selected", "v0-no-exit"}, h.applied,
		"switching back to the connect-time version must be applied, not skipped")
	assert.Equal(t, "v0-no-exit", n.AppliedVersion())
}

// A refresh that fetches the version already applied stays a no-op, including
// right after RefreshConfig itself applied it.
func TestRefreshConfig_SameVersionTwiceAppliesOnce(t *testing.T) {
	h := &recordingHandler{}
	msg := &infra.Message{ConfigVersion: "v1"}
	n := newRefreshNode(h, &msg)
	ctx := context.Background()

	n.setAppliedVersion("v0")
	require.NoError(t, n.RefreshConfig(ctx))
	require.NoError(t, n.RefreshConfig(ctx))

	assert.Equal(t, []string{"v1"}, h.applied)
}

// A failed apply must not be recorded, so the next refresh retries it.
func TestRefreshConfig_FailedApplyIsNotRecordedAndIsRetried(t *testing.T) {
	h := &recordingHandler{err: errors.New("boom")}
	msg := &infra.Message{ConfigVersion: "v1"}
	n := newRefreshNode(h, &msg)
	ctx := context.Background()

	n.setAppliedVersion("v0")
	require.Error(t, n.RefreshConfig(ctx))
	assert.Equal(t, "v0", n.AppliedVersion(), "a failed apply must leave the recorded version alone")

	h.err = nil
	require.NoError(t, n.RefreshConfig(ctx))
	assert.Equal(t, []string{"v1"}, h.applied, "the retry after a failure must apply")
}
