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

package service_test

import (
	"context"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerService_AdvertisedRoutesAndSelection(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "gw", AppID: "gw-app", Token: "enr-test-token"})
	require.NoError(t, err)
	_, err = svc.Register(ctx, &dto.PeerDto{Name: "mac", AppID: "mac-app", Token: "enr-test-token"})
	require.NoError(t, err)

	wsCtx := context.WithValue(ctx, infra.WorkspaceKey, "ws1")

	require.NoError(t, svc.SetAdvertisedRoutes(wsCtx, "gw", []string{"192.168.1.0/24"}))
	require.NoError(t, svc.SetRouteSelection(wsCtx, "mac", "gw", true))

	selected, err := svc.ListRouteSelections(wsCtx, "mac")
	require.NoError(t, err)
	assert.Equal(t, []string{"gw"}, selected)

	// Deselect and confirm it's gone.
	require.NoError(t, svc.SetRouteSelection(wsCtx, "mac", "gw", false))
	selected, err = svc.ListRouteSelections(wsCtx, "mac")
	require.NoError(t, err)
	assert.Empty(t, selected)

	// A peer cannot select itself.
	err = svc.SetRouteSelection(wsCtx, "mac", "mac", true)
	require.Error(t, err)
}
