package repository

import (
	"context"
	"time"

	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentRunRepository struct{ db *gorm.DB }

func NewAgentRunRepository(db *gorm.DB) *AgentRunRepository {
	return &AgentRunRepository{db.Session(&gorm.Session{NowFunc: func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }})}
}
func (r *AgentRunRepository) Get(ctx context.Context, id string) (domain.AgentRequestBinding, error) {
	var row domain.AgentRequestBinding
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	return row, sessionStoreError(err)
}

// A bounded keyset scan avoids a second persisted Run state machine. A complete
// sweep starts over, so newly inserted lower IDs are not permanently skipped.
func (r *AgentRunRepository) Scan(ctx context.Context, after string) ([]domain.AgentRequestBinding, error) {
	var rows []domain.AgentRequestBinding
	err := r.db.WithContext(ctx).Where("id > ? AND signing_key_id <> ?", after, domain.ExternalMCPSigningKey).Order("id ASC").Limit(32).Find(&rows).Error
	return rows, sessionStoreError(err)
}
func (r *AgentRunRepository) GetByKey(ctx context.Context, actor, session, key string) (domain.AgentRequestBinding, error) {
	var row domain.AgentRequestBinding
	err := r.db.WithContext(ctx).Where("actor_id = ? AND session_id = ? AND client_request_key = ?", actor, session, key).First(&row).Error
	return row, sessionStoreError(err)
}
func (r *AgentRunRepository) InsertOrGet(ctx context.Context, candidate domain.AgentRequestBinding) (domain.AgentRequestBinding, error) {
	var row domain.AgentRequestBinding
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// This no-op write takes the Session row/write lock on MySQL/SQLite.
		// Archive and admission therefore cannot pass each other in the database.
		lock := tx.Model(&domain.AgentSessionBinding{}).Where("id = ? AND actor_id = ? AND status = ? AND dsh_session_id = id", candidate.SessionID, candidate.ActorID, domain.AgentSessionActive).UpdateColumn("id", gorm.Expr("id"))
		if lock.Error != nil {
			return lock.Error
		}
		// Do not rely on MySQL's changed-row count for a no-op update.
		var session domain.AgentSessionBinding
		if err := tx.Where("id = ? AND actor_id = ? AND status = ? AND dsh_session_id = id", candidate.SessionID, candidate.ActorID, domain.AgentSessionActive).First(&session).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "session_id"}, {Name: "client_request_key"}}, DoNothing: true}).Create(&candidate).Error; err != nil {
			return err
		}
		return tx.Where("session_id = ? AND client_request_key = ? AND actor_id = ?", candidate.SessionID, candidate.ClientRequestKey, candidate.ActorID).First(&row).Error
	})
	return row, sessionStoreError(err)
}
func (r *AgentRunRepository) Acknowledge(ctx context.Context, id, messageID string) error {
	var run domain.AgentRequestBinding
	if err := r.db.WithContext(ctx).First(&run, "id = ?", id).Error; err != nil {
		return sessionStoreError(err)
	}
	if run.MessageID != nil {
		return nil
	}
	return sessionStoreError(r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match admission's session-before-request lock order. Start with a
		// write so SQLite WAL never has to upgrade a stale read transaction.
		if err := tx.Model(&domain.AgentSessionBinding{}).Where("id = ? AND actor_id = ?", run.SessionID, run.ActorID).
			UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		// Only the first durable acknowledgement schedules title work. GET,
		// reconciliation and duplicate sends cannot repeatedly rename a session.
		result := tx.Model(&domain.AgentRequestBinding{}).Where("id = ? AND message_id IS NULL", id).Update("message_id", messageID)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		return tx.Model(&domain.AgentSessionBinding{}).Where("id = ? AND actor_id = ?", run.SessionID, run.ActorID).
			Updates(map[string]any{"title_generation": gorm.Expr("title_generation + 1"), "title_attempt": "", "title_lease_until_ms": 0}).Error
	}))
}
func (r *AgentRunRepository) Revoke(ctx context.Context, actor, id string) error {
	return sessionStoreError(r.db.WithContext(ctx).Model(&domain.AgentRequestBinding{}).Where("id = ? AND actor_id = ?", id, actor).Update("revoked", true).Error)
}
