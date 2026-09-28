package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentSessionRepository struct{ db *gorm.DB }

func NewAgentSessionRepository(db *gorm.DB) *AgentSessionRepository {
	return &AgentSessionRepository{db: db.Session(&gorm.Session{NowFunc: func() time.Time {
		return time.Now().UTC().Truncate(time.Millisecond)
	}})}
}
func sessionStoreError(err error) error {
	if err != nil && err != gorm.ErrRecordNotFound {
		logger.L().Error("agent session storage failed", zap.String("error_type", fmt.Sprintf("%T", err)))
	}
	return err
}
func (r *AgentSessionRepository) Get(ctx context.Context, actor, id string) (domain.AgentSessionBinding, error) {
	var row domain.AgentSessionBinding
	err := r.db.WithContext(ctx).Where("actor_id = ? AND id = ?", actor, id).First(&row).Error
	return row, sessionStoreError(err)
}
func (r *AgentSessionRepository) GetByKey(ctx context.Context, actor, key string) (domain.AgentSessionBinding, error) {
	var row domain.AgentSessionBinding
	err := r.db.WithContext(ctx).Where("actor_id = ? AND provision_request_key = ?", actor, key).First(&row).Error
	return row, sessionStoreError(err)
}
func (r *AgentSessionRepository) InsertOrGet(ctx context.Context, row domain.AgentSessionBinding) (domain.AgentSessionBinding, error) {
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "actor_id"}, {Name: "provision_request_key"}}, DoNothing: true}).Create(&row).Error
	if err != nil {
		return domain.AgentSessionBinding{}, sessionStoreError(err)
	}
	return r.GetByKey(ctx, row.ActorID, row.ProvisionRequestKey)
}
func (r *AgentSessionRepository) List(ctx context.Context, actor, cursor string, limit int) ([]domain.AgentSessionBinding, error) {
	rows := []domain.AgentSessionBinding{}
	q := r.db.WithContext(ctx).Where("actor_id = ?", actor)
	if cursor != "" {
		q = q.Where("id < ?", cursor)
	}
	err := q.Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, sessionStoreError(err)
}

// Claim/finish use a compare-and-set lease across API processes. No database
// lock is held while calling Runtime. Late replies cannot override a new lease.
func (r *AgentSessionRepository) Claim(ctx context.Context, actor, id, attempt string, now int64) (bool, error) {
	result := r.db.WithContext(ctx).Model(&domain.AgentSessionBinding{}).
		Where("actor_id = ? AND id = ? AND status IN ? AND lease_until_ms <= ?", actor, id, []string{domain.AgentSessionProvisioning, domain.AgentSessionFailed}, now).
		Updates(map[string]any{"status": domain.AgentSessionProvisioning, "provision_attempt": attempt, "lease_until_ms": now + 30000, "provisioning_error_code": ""})
	return result.RowsAffected == 1, sessionStoreError(result.Error)
}
func (r *AgentSessionRepository) Finish(ctx context.Context, actor, id, attempt string, values map[string]any) (bool, error) {
	result := r.db.WithContext(ctx).Model(&domain.AgentSessionBinding{}).
		Where("actor_id = ? AND id = ? AND provision_attempt = ? AND status = ?", actor, id, attempt, domain.AgentSessionProvisioning).Updates(values)
	return result.RowsAffected == 1, sessionStoreError(result.Error)
}
func (r *AgentSessionRepository) Patch(ctx context.Context, actor, id string, values map[string]any, lifecycle bool) error {
	if _, manualTitle := values["title"]; manualTitle {
		// Fence off a generator already in flight, including when the user keeps
		// the same title. The next accepted message can schedule a new summary.
		values["title_settled_generation"] = gorm.Expr("title_generation + 1")
		// Use a separate transaction update below to avoid dialect assignment order.
		return sessionStoreError(r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&domain.AgentSessionBinding{}).Where("actor_id = ? AND id = ?", actor, id)
			if lifecycle {
				q = q.Where("status IN ?", []string{domain.AgentSessionActive, domain.AgentSessionArchived})
			}
			values["title_attempt"], values["title_lease_until_ms"] = "", 0
			if err := q.Updates(values).Error; err != nil {
				return err
			}
			return q.Update("title_generation", gorm.Expr("title_settled_generation")).Error
		}))
	}
	q := r.db.WithContext(ctx).Model(&domain.AgentSessionBinding{}).Where("actor_id = ? AND id = ?", actor, id)
	if lifecycle {
		q = q.Where("status IN ?", []string{domain.AgentSessionActive, domain.AgentSessionArchived})
	}
	return sessionStoreError(q.Updates(values).Error)
}
func (r *AgentSessionRepository) Due(ctx context.Context, limit int) ([]domain.AgentSessionBinding, error) {
	rows := []domain.AgentSessionBinding{}
	now := time.Now().UnixMilli()
	err := r.db.WithContext(ctx).Where("status IN ? AND lease_until_ms <= ? AND retry_at_ms <= ?", []string{domain.AgentSessionProvisioning, domain.AgentSessionFailed}, now, now).
		Order("updated_at ASC").Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, sessionStoreError(err)
}
