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
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerService_ListPeersStandalone(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))

	for _, p := range []struct{ name, appID string }{
		{"node-a", "app-a"},
		{"node-b", "app-b"},
	} {
		_, err := svc.Register(ctx, &dto.PeerDto{Name: p.name, AppID: p.appID, Token: "enr-test-token"})
		require.NoError(t, err)
	}

	// Standalone list: workspace context resolves rows from t_peer.
	listCtx := context.WithValue(ctx, infra.WorkspaceKey, "ws1")
	page, err := svc.ListPeers(listCtx, &dto.PageRequest{Page: 1, PageSize: 10})
	require.NoError(t, err)
	assert.EqualValues(t, 2, page.Total)
	require.Len(t, page.List, 2)

	names := []string{page.List[0].Name, page.List[1].Name}
	assert.ElementsMatch(t, []string{"node-a", "node-b"}, names)
	for _, pv := range page.List {
		assert.NotNil(t, pv.Address)
		assert.NotEmpty(t, pv.PublicKey)
	}

	// Keyword filter.
	kwCtx := listCtx
	page, err = svc.ListPeers(kwCtx, &dto.PageRequest{Page: 1, PageSize: 10, Keyword: "node-b"})
	require.NoError(t, err)
	require.Len(t, page.List, 1, "keyword filter narrows the list")
	assert.Equal(t, "node-b", page.List[0].Name)
	assert.EqualValues(t, 2, page.Total) // Total mirrors the K8s path: unfiltered count

	// Public key round-trips into the netmap Current for the owning peer.
	msg, err := svc.GetNetmap(ctx, "enr-test-token", "app-a")
	require.NoError(t, err)
	var found bool
	for _, p := range msg.Network.Peers {
		if p.Name == "node-a" && p.PublicKey != "" {
			found = true
		}
	}
	assert.True(t, found, "netmap peers must carry their public keys")
}

func TestNetworkService_ListTokensStandalone(t *testing.T) {
	_, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))
	seedEnrollmentToken(t, st, nil)

	networkSvc := service.NewNetworkService(nil, st)
	listCtx := context.WithValue(ctx, infra.WorkspaceKey, "ws1")
	page, err := networkSvc.ListTokens(listCtx, &dto.PageRequest{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, page.List, 1)
	assert.Equal(t, "enr-test-token", page.List[0].Token)
	assert.Equal(t, "Dev", page.List[0].WorkspaceDisplayName)
	assert.False(t, page.List[0].IsExpired)
}
