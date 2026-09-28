package repository

import (
	"context"
	"errors"
	"time"

	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

var ErrToolAuditDenied = errors.New("agent_tool_audit_denied")
var ErrToolAuditBudget = errors.New("agent_tool_budget_exceeded")

type AgentToolAuditRepository struct{ db *gorm.DB }

func NewAgentToolAuditRepository(db *gorm.DB) *AgentToolAuditRepository {
	return &AgentToolAuditRepository{db}
}

// Serialize reservations on the durable Run row, including distinct call IDs.
// Existing executing records are never stolen or automatically redispatched.
func (r *AgentToolAuditRepository) Reserve(ctx context.Context, candidate domain.AgentToolAudit, limit int64) (domain.AgentToolAudit, bool, error) {
	var row domain.AgentToolAudit
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.AgentRequestBinding{}).Where("id = ?", candidate.RunID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		var run domain.AgentRequestBinding
		if err := tx.Where("id = ?", candidate.RunID).First(&run).Error; err != nil {
			return err
		}
		if run.Revoked || run.DeadlineMS <= time.Now().UnixMilli() || run.ActorID != candidate.ActorID || run.SessionID != candidate.SessionID || run.EnvelopeDigest != candidate.EnvelopeDigest {
			return ErrToolAuditDenied
		}
		err := tx.Where("run_id = ? AND tool_call_id = ?", candidate.RunID, candidate.ToolCallID).First(&row).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err = tx.Model(&domain.AgentToolAudit{}).Where("run_id = ?", candidate.RunID).Count(&count).Error; err != nil {
			return err
		}
		if limit < 1 || count >= limit {
			return ErrToolAuditBudget
		}
		if err = tx.Create(&candidate).Error; err != nil {
			return err
		}
		row, created = candidate, true
		return nil
	})
	return row, created, sessionStoreError(err)
}
func (r *AgentToolAuditRepository) Finish(ctx context.Context, id, status, ref, code string) error {
	result := r.db.WithContext(ctx).Model(&domain.AgentToolAudit{}).Where("id = ? AND status IN ?", id, []string{"executing", "pending_approval"}).Updates(map[string]any{"status": status, "result_ref": ref, "error_code": code, "finished_at": time.Now().UTC()})
	if result.Error != nil {
		return sessionStoreError(result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrToolAuditDenied
	}
	return nil
}

func (r *AgentToolAuditRepository) MarkStarted(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Model(&domain.AgentToolAudit{}).Where("id = ? AND status IN ? AND started = ?", id, []string{"executing", "pending_approval"}, false).Update("started", true)
	if result.Error != nil {
		return sessionStoreError(result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrToolAuditDenied
	}
	return nil
}

func (r *AgentToolAuditRepository) Effect(ctx context.Context, id string) (domain.AgentToolEffect, error) {
	var effect domain.AgentToolEffect
	err := r.db.WithContext(ctx).Where("audit_id = ?", id).First(&effect).Error
	return effect, sessionStoreError(err)
}

func (r *AgentToolAuditRepository) GetCall(ctx context.Context, run, call string) (domain.AgentToolAudit, error) {
	var row domain.AgentToolAudit
	err := r.db.WithContext(ctx).Where("run_id = ? AND tool_call_id = ?", run, call).First(&row).Error
	return row, sessionStoreError(err)
}
