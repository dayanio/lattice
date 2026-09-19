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
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newStandaloneTokenService(t *testing.T) (service.TokenService, store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{}, &models.Workspace{}, &models.UserProfile{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return service.NewTokenService(nil, st), st
}

func workspaceContext(ctx context.Context, wsID string) context.Context {
	return context.WithValue(ctx, infra.WorkspaceKey, wsID)
}

func TestTokenService_CreateStandalone_WritesEnrollmentToken(t *testing.T) {
	svc, st := newStandaloneTokenService(t)
	ctx := workspaceContext(context.Background(), "ws1")
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev", CreatedBy: "owner-1",
	}))

	tokenStr, err := svc.Create(ctx, &dto.TokenDto{Expiry: "1h", Limit: 3})
	require.NoError(t, err)
	assert.NotEmpty(t, tokenStr)

	tok, err := st.EnrollmentTokens().GetByToken(ctx, tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "ws1", tok.WorkspaceID, "token binds to the workspace id")
	assert.Equal(t, 3, tok.UsageLimit)
	assert.WithinDuration(t, time.Now().Add(time.Hour), tok.ExpiresAt, time.Minute)

	// The token creation flow also seeds the default-deny policy.
	pol, err := st.Policies().GetByName(ctx, "ws1", "default-deny")
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusActive, pol.Status)
}

// Regression: issuing a token with a caller-supplied name must return the
// stored token. It used to return a freshly generated random string that was
// never persisted, so every join with the issued token failed "token not
// exists" (cloud test host, 2026-09-19).
func TestTokenService_CreateStandalone_NamedTokenReturnsStoredValue(t *testing.T) {
	svc, st := newStandaloneTokenService(t)
	ctx := workspaceContext(context.Background(), "ws1")
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))

	tokenStr, err := svc.Create(ctx, &dto.TokenDto{Name: "cloud-agent", Expiry: "1h", Limit: 5})
	require.NoError(t, err)
	assert.Equal(t, "cloud-agent", tokenStr, "the issued token must be the stored one, not an unpersisted random string")

	tok, err := st.EnrollmentTokens().GetByToken(ctx, tokenStr)
	require.NoError(t, err)
	assert.Equal(t, "ws1", tok.WorkspaceID)
}

func TestTokenService_DeleteStandalone(t *testing.T) {
	svc, st := newStandaloneTokenService(t)
	ctx := workspaceContext(context.Background(), "ws1")
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))
	require.NoError(t, st.EnrollmentTokens().Create(ctx, &models.EnrollmentToken{
		Token: "enr-del", WorkspaceID: "ws1", ExpiresAt: time.Now().Add(time.Hour),
	}))

	require.NoError(t, svc.Delete(ctx, "enr-del"))

	_, err := st.EnrollmentTokens().GetByToken(ctx, "enr-del")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Deleting again is a no-op (mirrors K8s IgnoreNotFound).
	require.NoError(t, svc.Delete(ctx, "enr-del"))
}

func TestPolicyService_ApplyDirectStandalone_WritesActivePolicy(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&models.Policy{}, &models.Workspace{}))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(context.Background(), &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))

	svc := service.NewPolicyService(nil, st)
	vo, err := svc.ApplyDirect(context.Background(), "ws1", "op-1", "Operator", &dto.PolicyDto{
		Name:   "allow-egress",
		Action: "Allow",
	})
	require.NoError(t, err)
	assert.Equal(t, "allow-egress", vo.Name)

	pol, err := st.Policies().GetByName(context.Background(), "ws1", "allow-egress")
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusActive, pol.Status, "policy must be active without any K8s client")
}
