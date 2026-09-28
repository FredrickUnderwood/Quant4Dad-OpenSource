package repository

import (
	"context"
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AgentMutationRepository struct{ db *gorm.DB }

func NewAgentMutationRepository(db *gorm.DB) *AgentMutationRepository {
	return &AgentMutationRepository{db}
}

// Execute commits the business write, receipt consumption, and idempotent
// result together. A retry may read an effect but can never perform it again.
func (r *AgentMutationRepository) Execute(ctx context.Context, audit domain.AgentToolAudit, approval domain.AgentApprovalReceipt, mutation domain.AgentMutation) (domain.AgentMutationResult, error) {
	ctx = logger.AgentToolContext(ctx, audit.SessionID, audit.RunID, audit.ToolCallID, mutation.Kind)
	var result domain.AgentMutationResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.AgentRequestBinding{}).Where("id = ?", audit.RunID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		var run domain.AgentRequestBinding
		if err := tx.Where("id = ?", audit.RunID).First(&run).Error; err != nil {
			return err
		}
		if run.Revoked || run.DeadlineMS <= time.Now().UnixMilli() || run.EnvelopeDigest != audit.EnvelopeDigest || run.ActorID != audit.ActorID || run.SessionID != audit.SessionID {
			return ErrApprovalDenied
		}
		if err := tx.Model(&domain.AgentToolAudit{}).Where("id = ?", audit.ID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
			return err
		}
		var current domain.AgentToolAudit
		if err := tx.Where("id = ?", audit.ID).First(&current).Error; err != nil {
			return err
		}
		if current.ArgsHash != audit.ArgsHash || current.ToolName != mutation.Kind || current.Risk != audit.Risk || !current.Started {
			return ErrApprovalDenied
		}
		var effect domain.AgentToolEffect
		err := tx.Where("audit_id = ?", audit.ID).First(&effect).Error
		if err == nil {
			if effect.ArgsHash != audit.ArgsHash || sonic.UnmarshalString(effect.ResultJSON, &result) != nil {
				return ErrApprovalDenied
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if mutation.Kind == "run_backtest" {
			if current.Status != "executing" || current.Risk != "R1" {
				return ErrApprovalDenied
			}
		} else {
			if current.Status != "pending_approval" || (current.Risk != "R2" && current.Risk != "R3") || approval.ReceiptNonce == nil {
				return ErrApprovalDenied
			}
			res := tx.Model(&domain.AgentApprovalReceipt{}).Where("id = ? AND audit_id = ? AND status = ? AND receipt_nonce = ? AND args_hash = ? AND risk = ? AND envelope_digest = ? AND expires_at > ?", approval.ID, audit.ID, "approved", *approval.ReceiptNonce, audit.ArgsHash, current.Risk, audit.EnvelopeDigest, time.Now().UTC()).Update("status", "consumed")
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrApprovalDenied
			}
		}
		switch mutation.Kind {
		case "run_backtest":
			// A database mutex bounds the shared durable queue across processes.
			guard := domain.Setting{Key: "agent.backtest.queue", Value: []byte(`0`)}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&guard).Error; err != nil {
				return err
			}
			if err := tx.Model(&domain.Setting{}).Where("`key` = ?", guard.Key).UpdateColumn("value", gorm.Expr("value")).Error; err != nil {
				return err
			}
			var queued int64
			if err := tx.Model(&domain.BacktestJob{}).Where("agent_run = ? AND status = ?", true, domain.BacktestStatusPending).Count(&queued).Error; err != nil {
				return err
			}
			if queued >= 64 {
				return errors.New("agent_backtest_queue_full")
			}
			job := mutation.Backtest
			if job == nil || job.ID != 0 || !job.AgentRun || len(job.AgentSnapshot) > 65536 {
				return ErrApprovalDenied
			}
			var snapshot domain.AgentBacktestSnapshot
			if sonic.UnmarshalString(job.AgentSnapshot, &snapshot) != nil {
				return ErrApprovalDenied
			}
			var source domain.Strategy
			if err := tx.Select("version").First(&source, job.StrategyID).Error; err != nil {
				return err
			}
			if source.Version != snapshot.Strategy.Version {
				return domain.ErrResourceConflict
			}
			job.Status = domain.BacktestStatusPending
			if err := NewBacktestRepository(tx).CreateJob(ctx, job); err != nil {
				return err
			}
			result = domain.AgentMutationResult{JobID: job.ID, Status: "pending"}
		case "create_strategy":
			if mutation.Strategy == nil || mutation.Strategy.ID != 0 {
				return ErrApprovalDenied
			}
			if err := NewStrategyRepository(tx).Create(ctx, mutation.Strategy); err != nil {
				return err
			}
			result = domain.AgentMutationResult{ID: mutation.Strategy.ID, Version: mutation.Strategy.Version}
		case "update_strategy":
			if mutation.Strategy == nil || mutation.Strategy.ID < 1 || mutation.Strategy.Version < 1 {
				return ErrApprovalDenied
			}
			if err := NewStrategyRepository(tx).Update(ctx, mutation.Strategy); err != nil {
				return err
			}
			result = domain.AgentMutationResult{ID: mutation.Strategy.ID, Version: mutation.Strategy.Version}
		case "create_pipeline":
			if mutation.Pipeline == nil || mutation.Pipeline.ID != 0 || mutation.Pipeline.Status != domain.PipelineStatusDraft {
				return ErrApprovalDenied
			}
			if err := NewPipelineRepository(tx).Create(ctx, mutation.Pipeline); err != nil {
				return err
			}
			result = domain.AgentMutationResult{ID: mutation.Pipeline.ID, Version: mutation.Pipeline.Version, Status: mutation.Pipeline.Status}
		case "update_pipeline":
			if mutation.Pipeline == nil || mutation.Pipeline.ID < 1 || mutation.Pipeline.Version < 1 {
				return ErrApprovalDenied
			}
			var target domain.Pipeline
			if err := tx.First(&target, mutation.Pipeline.ID).Error; err != nil {
				return err
			}
			if target.Status != mutation.Pipeline.Status || (target.Status == domain.PipelineStatusEnabled && current.Risk != "R3") {
				return ErrApprovalDenied
			}
			if err := NewPipelineRepository(tx).Update(ctx, mutation.Pipeline); err != nil {
				return err
			}
			result = domain.AgentMutationResult{ID: mutation.Pipeline.ID, Version: mutation.Pipeline.Version, Status: mutation.Pipeline.Status}
		case "set_pipeline_status":
			p := mutation.Pipeline
			if p == nil || p.ID < 1 || p.Version < 1 || (p.Status != domain.PipelineStatusDisabled && p.Status != domain.PipelineStatusEnabled) || (p.Status == domain.PipelineStatusEnabled && current.Risk != "R3") {
				return ErrApprovalDenied
			}
			if err := NewPipelineRepository(tx).UpdateStatus(ctx, p.ID, p.Status, p.Version); err != nil {
				return err
			}
			result = domain.AgentMutationResult{ID: p.ID, Version: p.Version + 1, Status: p.Status}
		default:
			return ErrApprovalDenied
		}
		data, err := sonic.MarshalString(result)
		if err != nil || len(data) > 4096 {
			return ErrApprovalDenied
		}
		effect = domain.AgentToolEffect{AuditID: audit.ID, ArgsHash: audit.ArgsHash, ResultJSON: data}
		if err := tx.Create(&effect).Error; err != nil {
			return err
		}
		if current.Status == "executing" {
			return nil
		}
		res := tx.Model(&domain.AgentToolAudit{}).Where("id = ? AND status = ?", audit.ID, "pending_approval").Update("status", "executing")
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrApprovalDenied
		}
		return nil
	})
	return result, sessionStoreError(err)
}
