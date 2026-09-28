package repository

import (
	"context"
	"errors"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
	"time"
)

var ErrApprovalDenied = errors.New("agent_approval_rejected")

type AgentApprovalRepository struct{ db *gorm.DB }

func NewAgentApprovalRepository(db *gorm.DB) *AgentApprovalRepository {
	return &AgentApprovalRepository{db}
}

func (r *AgentApprovalRepository) Ensure(ctx context.Context, candidate domain.AgentApprovalReceipt) (domain.AgentApprovalReceipt, error) {
	var row domain.AgentApprovalReceipt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Same lock order as execution: Run -> audit -> approval.
		if err := tx.Model(&domain.AgentRequestBinding{}).Where("id = ?", candidate.RunID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.AgentToolAudit{}).Where("id = ?", candidate.AuditID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		err := tx.Where("id = ?", candidate.ID).First(&row).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var audit domain.AgentToolAudit
		if err := tx.Where("id = ?", candidate.AuditID).First(&audit).Error; err != nil {
			return err
		}
		if audit.Status != "pending_approval" || audit.Started || audit.ArgsHash != candidate.ArgsHash || audit.EnvelopeDigest != candidate.EnvelopeDigest || audit.Risk != candidate.Risk {
			return ErrApprovalDenied
		}
		if err := tx.Create(&candidate).Error; err != nil {
			return err
		}
		row = candidate
		return nil
	})
	return row, sessionStoreError(err)
}
func (r *AgentApprovalRepository) Get(ctx context.Context, actor, id string) (domain.AgentApprovalReceipt, error) {
	var row domain.AgentApprovalReceipt
	err := r.db.WithContext(ctx).Where("id = ? AND actor_id = ?", id, actor).First(&row).Error
	return row, sessionStoreError(err)
}
func (r *AgentApprovalRepository) Decide(ctx context.Context, actor, id, decision, nonce string) (domain.AgentApprovalReceipt, error) {
	var row domain.AgentApprovalReceipt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Read identity first, then lock in the same order used by Gateway writes.
		if err := tx.Where("id = ? AND actor_id = ?", id, actor).First(&row).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.AgentRequestBinding{}).Where("id = ?", row.RunID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.AgentToolAudit{}).Where("id = ?", row.AuditID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.AgentApprovalReceipt{}).Where("id = ?", id).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ? AND actor_id = ?", id, actor).First(&row).Error; err != nil {
			return err
		}
		var run domain.AgentRequestBinding
		if err := tx.Where("id = ?", row.RunID).First(&run).Error; err != nil {
			return err
		}
		if run.Revoked || run.DeadlineMS <= time.Now().UnixMilli() || run.EnvelopeDigest != row.EnvelopeDigest || !time.Now().Before(row.ExpiresAt) {
			return ErrApprovalDenied
		}
		status := "rejected"
		if decision == "allow_once" {
			status = "approved"
		} else if decision != "reject" {
			return ErrApprovalDenied
		}
		if row.Status == status || (status == "approved" && row.Status == "consumed") {
			return nil
		}
		if row.Status != "pending" {
			return ErrApprovalDenied
		}
		updates := map[string]any{"status": status}
		if status == "approved" {
			updates["receipt_nonce"] = nonce
			row.ReceiptNonce = &nonce
		}
		res := tx.Model(&domain.AgentApprovalReceipt{}).Where("id = ? AND status = ?", id, "pending").Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrApprovalDenied
		}
		if status == "rejected" {
			res = tx.Model(&domain.AgentToolAudit{}).Where("id = ? AND status = ? AND started = ?", row.AuditID, "pending_approval", false).Updates(map[string]any{"status": "failed", "error_code": "agent_approval_denied", "finished_at": time.Now().UTC()})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrApprovalDenied
			}
		}
		row.Status = status
		return nil
	})
	return row, sessionStoreError(err)
}
