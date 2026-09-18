package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// notifyFakeSignal records Publish calls so tests can assert which peers
// were notified about netmap changes.
type notifyFakeSignal struct {
	subjects []string
}

func (f *notifyFakeSignal) Send(_ context.Context, _ infra.PeerID, _ []byte) error { return nil }
func (f *notifyFakeSignal) Request(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return nil, nil
}
func (f *notifyFakeSignal) Service(_, _ string, _ func([]byte) ([]byte, error)) {}
func (f *notifyFakeSignal) Flush() error                                        { return nil }
func (f *notifyFakeSignal) Close() error                                        { return nil }
func (f *notifyFakeSignal) Publish(_ context.Context, subject string, _ []byte) error {
	f.subjects = append(f.subjects, subject)
	return nil
}

func newNotifyTestService(t *testing.T) (context.Context, service.PeerService, *notifyFakeSignal) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{}, &models.Workspace{}, &models.UserProfile{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Home",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "pa"}, WorkspaceID: "ws1", Name: "peer-a",
		AppID: "app-a", Address: "10.96.0.2",
	}))
	require.NoError(t, st.Peers().Create(ctx, &models.Peer{
		Model: models.Model{ID: "pb"}, WorkspaceID: "ws1", Name: "peer-b",
		AppID: "app-b", Address: "10.96.0.3",
	}))
	fake := &notifyFakeSignal{}
	return workspaceContext(ctx, "ws1"), service.NewPeerService(nil, st, nil, license.NewVerifier("pro"), fake), fake
}

func TestUpdatePeerStandalone_NotifiesOtherPeers(t *testing.T) {
	ctx, svc, fake := newNotifyTestService(t)

	_, err := svc.UpdatePeer(ctx, &dto.PeerDto{Name: "peer-a", DisplayName: "renamed"})
	require.NoError(t, err)

	require.NotEmpty(t, fake.subjects, "UpdatePeer must publish at least one notification")
	foundPeerB := false
	for _, s := range fake.subjects {
		if s == infra.NetmapChangedSubject("app-b") {
			foundPeerB = true
		}
		require.NotEqual(t, infra.NetmapChangedSubject("app-a"), s,
			"the peer that just changed must not be notified about itself")
	}
	require.True(t, foundPeerB, "expected a notification to app-b's subject, got %v", fake.subjects)
}

func TestSetRouteSelectionStandalone_NotifiesAllPeers(t *testing.T) {
	ctx, svc, fake := newNotifyTestService(t)

	// peer-b selects peer-a's advertised routes: both sides' netmaps change
	// (consumer gains the routed CIDRs), so everyone gets notified.
	err := svc.SetRouteSelection(ctx, "peer-b", "peer-a", true)
	require.NoError(t, err)

	require.Contains(t, fake.subjects, infra.NetmapChangedSubject("app-a"))
	require.Contains(t, fake.subjects, infra.NetmapChangedSubject("app-b"))
}

func TestRegisterStandalone_NormalizesAppID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{}, &models.Workspace{}, &models.UserProfile{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Home",
	}))
	require.NoError(t, st.EnrollmentTokens().Create(ctx, &models.EnrollmentToken{
		Token: "enr-norm", WorkspaceID: "ws1", ExpiresAt: time.Now().Add(time.Hour),
	}))
	fake := &notifyFakeSignal{}
	svc := service.NewPeerService(nil, st, nil, license.NewVerifier("pro"), fake)

	node, err := svc.Register(context.Background(), &dto.PeerDto{
		Name: "MacBook Pro", AppID: "MacBook Pro 16", Token: "enr-norm",
		PublicKey: "ePcj1OOpgncNTUmYPqpzg58L5DvDjfU7cltRoALF2Rs=",
	})
	require.NoError(t, err)
	require.Equal(t, "MacBook-Pro-16", node.AppID, "AppID must be normalized to a NATS-safe token")

	got, err := st.Peers().GetByAppID(ctx, "MacBook-Pro-16")
	require.NoError(t, err)
	require.Equal(t, "MacBook-Pro-16", got.AppID)

	// The push subject derived from the normalized ID is a legal NATS subject.
	require.Equal(t, "lattice.signals.peers.MacBook-Pro-16.netmap",
		infra.NetmapChangedSubject(got.AppID))
}
