package gormstore

import (
	"context"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/repository"
	"gorm.io/gorm"
)

type peerIdentityRepo struct {
	*repository.BaseRepository[models.PeerIdentity]
}

func newPeerIdentityRepo(db *gorm.DB) *peerIdentityRepo {
	return &peerIdentityRepo{
		BaseRepository: repository.NewBaseRepository[models.PeerIdentity](db),
	}
}

func (r *peerIdentityRepo) ListByNetwork(ctx context.Context, networkID string) ([]*models.PeerIdentity, error) {
	return r.Find(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("network_id = ?", networkID)
	})
}

func (r *peerIdentityRepo) GetByNetworkAndName(ctx context.Context, networkID, name string) (*models.PeerIdentity, error) {
	return r.First(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("network_id = ? AND name = ?", networkID, name)
	})
}

func (r *peerIdentityRepo) GetByID(ctx context.Context, id string) (*models.PeerIdentity, error) {
	return r.BaseRepository.GetByID(ctx, id)
}

func (r *peerIdentityRepo) Delete(ctx context.Context, id string) error {
	return r.BaseRepository.Delete(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("id = ?", id)
	})
}

var _ store.PeerIdentityRepository = (*peerIdentityRepo)(nil)

// ListIDs enumerates every identity primary key (soft-deleted rows excluded).
func (r *peerIdentityRepo) ListIDs(ctx context.Context) ([]string, error) {
	var ids []string
	err := r.DB().WithContext(ctx).Model(&models.PeerIdentity{}).Pluck("id", &ids).Error
	return ids, err
}

// ClearGracePeriod zeroes the previous-device binding. Map-based Updates
// are required because struct-based Updates skip zero values, which would
// silently keep the old binding in place.
func (r *peerIdentityRepo) ClearGracePeriod(ctx context.Context, id string) error {
	return r.DB().WithContext(ctx).Model(&models.PeerIdentity{}).
		Where("id = ?", id).
		Updates(map[string]interface{}{
			"previous_peer_ref":       "",
			"previous_peer_ip":        "",
			"grace_period_expires_at": nil,
		}).Error
}
