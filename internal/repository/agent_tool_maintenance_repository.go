package repository

import (
	"context"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
	"time"
)

// Maintenance preserves audit identities forever. A timed-out execution is an
// unknown outcome, never a reservation another worker is allowed to steal.
func (r *AgentToolAuditRepository) Maintain(ctx context.Context, now, retainAfter time.Time) (int64, error) {
	var changed int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []domain.AgentToolAudit
		err := tx.Where("status IN ? AND created_at < ? AND NOT EXISTS (SELECT 1 FROM agent_request_binding r WHERE r.id = agent_tool_audit.run_id AND r.deadline_ms > ?) AND NOT EXISTS (SELECT 1 FROM agent_tool_effect e WHERE e.audit_id = agent_tool_audit.id)", []string{"executing", "pending_approval"}, now.Add(-time.Minute), now.Add(-time.Minute).UnixMilli()).Order("created_at ASC").Limit(100).Find(&rows).Error
		if err != nil {
			return err
		}
		for _, row := range rows {
			code := "agent_tool_not_started"
			if row.Started {
				code = "tool_outcome_unknown"
			}
			res := tx.Model(&domain.AgentToolAudit{}).Where("id = ? AND status IN ?", row.ID, []string{"executing", "pending_approval"}).Updates(map[string]any{"status": "failed", "error_code": code, "finished_at": now})
			if res.Error != nil {
				return res.Error
			}
			changed += res.RowsAffected
		}
		var expired []string
		if err := tx.Model(&domain.AgentToolAudit{}).Where("status = ? AND finished_at < ?", "succeeded", retainAfter).Order("finished_at ASC").Limit(100).Pluck("id", &expired).Error; err != nil {
			return err
		}
		if len(expired) > 0 {
			res := tx.Model(&domain.AgentToolAudit{}).Where("id IN ? AND status = ?", expired, "succeeded").Updates(map[string]any{"status": "expired", "result_ref": "", "error_code": "agent_tool_result_expired"})
			if res.Error != nil {
				return res.Error
			}
			changed += res.RowsAffected
		}
		return nil
	})
	return changed, sessionStoreError(err)
}

// ArtifactReferenced is called with the artifact directory's exclusive lock.
// Any active execution retains every artifact, covering the Put -> Finish gap.
func (r *AgentToolAuditRepository) ArtifactReferenced(ctx context.Context, name string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&domain.AgentToolAudit{}).Where("status = ? OR result_ref = ?", "executing", name).Count(&count).Error
	return count > 0, sessionStoreError(err)
}

func (r *AgentToolAuditRepository) CommittedEffects(ctx context.Context) ([]domain.AgentToolAudit, error) {
	var rows []domain.AgentToolAudit
	err := r.db.WithContext(ctx).Where("status = ? AND EXISTS (SELECT 1 FROM agent_tool_effect e WHERE e.audit_id = agent_tool_audit.id)", "executing").Order("created_at ASC").Limit(100).Find(&rows).Error
	return rows, sessionStoreError(err)
}
