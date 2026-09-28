package repository

import (
	"context"

	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

// External MCP reuses the durable execution ledger without creating a fake DSH
// session. Its source marker keeps these rows out of Runtime reconciliation.
type ExternalMCPSessionRepository struct{ db *gorm.DB }

func NewExternalMCPSessionRepository(db *gorm.DB) *ExternalMCPSessionRepository {
	return &ExternalMCPSessionRepository{db: db}
}

func (r *ExternalMCPSessionRepository) Create(ctx context.Context, row domain.AgentRequestBinding) error {
	return sessionStoreError(r.db.WithContext(ctx).Create(&row).Error)
}

func (r *ExternalMCPSessionRepository) Get(ctx context.Context, session string) (domain.AgentRequestBinding, error) {
	var row domain.AgentRequestBinding
	err := r.db.WithContext(ctx).Where("session_id = ? AND actor_id = ? AND signing_key_id = ?", session, domain.ExternalMCPActor, domain.ExternalMCPSigningKey).First(&row).Error
	return row, sessionStoreError(err)
}

func (r *ExternalMCPSessionRepository) Revoke(ctx context.Context, session string) error {
	return sessionStoreError(r.db.WithContext(ctx).Model(&domain.AgentRequestBinding{}).
		Where("session_id = ? AND actor_id = ? AND signing_key_id = ?", session, domain.ExternalMCPActor, domain.ExternalMCPSigningKey).Update("revoked", true).Error)
}
