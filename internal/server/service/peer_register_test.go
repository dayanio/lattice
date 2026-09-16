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

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeVerifier fakes the license check: valid=true enforces MaxNodes,
// valid=false behaves like Community (no restriction).
type fakeVerifier struct {
	valid    bool
	maxNodes int
}

func (f *fakeVerifier) Verify() (*license.License, license.Status, error) {
	if !f.valid {
		return nil, license.StatusNotFound, nil
	}
	return &license.License{Limits: license.LicenseLimits{MaxNodes: f.maxNodes}}, license.StatusValid, nil
}

func (f *fakeVerifier) HasFeature(string) bool { return false }

func newRegisterService(t *testing.T, verifier license.Verifier) (service.PeerService, store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Workspace{}, &models.UserProfile{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	svc := service.NewPeerService(nil, st, nil, verifier, nil)
	return svc, st
}

func seedEnrollmentToken(t *testing.T, st store.Store, opts func(*models.EnrollmentToken)) *models.EnrollmentToken {
	t.Helper()
	tok := &models.EnrollmentToken{
		Token:       "enr-test-token",
		WorkspaceID: "ws1",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if opts != nil {
		opts(tok)
	}
	require.NoError(t, st.EnrollmentTokens().Create(context.Background(), tok))
	return tok
}

func TestPeerService_RegisterStandalone_CreatesPeer(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	tok := seedEnrollmentToken(t, st, nil)

	node, err := svc.Register(ctx, &dto.PeerDto{
		Name: "api", AppID: "app-1", Token: "enr-test-token",
		PublicKey: "pub-1", Endpoint: "1.2.3.4:51820", Platform: "linux",
	})
	require.NoError(t, err)

	assert.Equal(t, "api", node.Name)
	require.NotNil(t, node.Address)
	assert.Equal(t, "10.96.0.2", *node.Address, "first peer gets the first address")
	assert.Equal(t, "enr-test-token", node.Token, "netmap token follows the K8s semantics (enrollment token)")
	assert.NotEmpty(t, node.PrivateKey, "the control plane must issue a WireGuard private key")
	assert.Equal(t, "ws1", node.NetworkId)

	got, err := st.Peers().GetByAppID(ctx, "app-1")
	require.NoError(t, err)
	assert.Equal(t, node.Token, got.Token)
	assert.Equal(t, "ws1", got.WorkspaceID)

	enr, err := st.EnrollmentTokens().GetByToken(ctx, tok.Token)
	require.NoError(t, err)
	assert.Equal(t, 1, enr.UsedCount)
}

func TestPeerService_RegisterStandalone_SecondPeerGetsNextAddress(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)
	node2, err := svc.Register(ctx, &dto.PeerDto{Name: "db", AppID: "app-2", Token: "enr-test-token"})
	require.NoError(t, err)
	require.NotNil(t, node2.Address)
	assert.Equal(t, "10.96.0.3", *node2.Address)
}

func TestPeerService_RegisterStandalone_ReRegistrationResumes(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	first, err := svc.Register(ctx, &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)

	again, err := svc.Register(ctx, &dto.PeerDto{
		Name: "api", AppID: "app-1", Token: "enr-test-token", Endpoint: "5.6.7.8:51820",
	})
	require.NoError(t, err)
	assert.Equal(t, *first.Address, *again.Address, "re-registration keeps the overlay address")
	assert.Equal(t, first.Token, again.Token, "re-registration keeps the peer credential")
	assert.Equal(t, first.PrivateKey, again.PrivateKey, "re-registration keeps the WireGuard key")

	rows, err := st.Peers().ListByWorkspace(ctx, "ws1")
	require.NoError(t, err)
	assert.Len(t, rows, 1, "no duplicate registry record")

	got, err := st.Peers().GetByAppID(ctx, "app-1")
	require.NoError(t, err)
	assert.Equal(t, "5.6.7.8:51820", got.Endpoint, "endpoint refreshed on resume")
}

func TestPeerService_RegisterStandalone_ExpiredTokenRejected(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	seedEnrollmentToken(t, st, func(tok *models.EnrollmentToken) {
		tok.ExpiresAt = time.Now().Add(-time.Minute)
	})

	_, err := svc.Register(context.Background(), &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	assert.Error(t, err)
}

func TestPeerService_RegisterStandalone_UsageLimitExhaustedRejected(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, func(tok *models.EnrollmentToken) { tok.UsageLimit = 1 })

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)

	_, err = svc.Register(ctx, &dto.PeerDto{Name: "db", AppID: "app-2", Token: "enr-test-token"})
	assert.Error(t, err, "token with usage limit 1 must not enroll a second peer")
}

func TestPeerService_RegisterStandalone_UnknownTokenRejected(t *testing.T) {
	svc, _ := newRegisterService(t, &fakeVerifier{valid: false})

	_, err := svc.Register(context.Background(), &dto.PeerDto{Name: "api", AppID: "app-1", Token: "ghost"})
	assert.Error(t, err)
}

func TestPeerService_RegisterStandalone_EmptyTokenRejected(t *testing.T) {
	svc, _ := newRegisterService(t, &fakeVerifier{valid: false})

	_, err := svc.Register(context.Background(), &dto.PeerDto{Name: "api", AppID: "app-1"})
	assert.Error(t, err)
}

func TestPeerService_RegisterStandalone_NodeLimitEnforced(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: true, maxNodes: 1})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	_, err := svc.Register(ctx, &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)

	_, err = svc.Register(ctx, &dto.PeerDto{Name: "db", AppID: "app-2", Token: "enr-test-token"})
	assert.Error(t, err, "second peer must be rejected once the node limit is reached")
}

func TestPeerService_RegisterStandalone_EnforcerModeFromWorkspaceOwner(t *testing.T) {
	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)

	// The workspace record's primary key must equal the token's workspace id.
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "ws1", DisplayName: "Dev", CreatedBy: "owner-1",
	}))
	require.NoError(t, st.Profiles().Upsert(ctx, &models.UserProfile{UserID: "owner-1", EnforcerMode: "enforce"}))

	node, err := svc.Register(ctx, &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)
	assert.Equal(t, "enforce", node.EnforcerMode)
}
