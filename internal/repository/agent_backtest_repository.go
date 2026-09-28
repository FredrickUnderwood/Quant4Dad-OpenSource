package repository

import (
	"context"
	"errors"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
	"time"
)

var ErrAgentBacktestLease = errors.New("agent_backtest_lease_lost")

func (r *BacktestRepository) ClaimAgent(ctx context.Context, nonce string) (*domain.BacktestJob, error) {
	var row domain.BacktestJob
	err := r.db.WithContext(ctx).Where("agent_run = ? AND status = ?", true, domain.BacktestStatusPending).Order("id ASC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	until := now.Add(2 * time.Minute)
	res := r.db.WithContext(ctx).Model(&domain.BacktestJob{}).Where("id = ? AND status = ? AND agent_run = ?", row.ID, domain.BacktestStatusPending, true).Updates(map[string]any{"status": domain.BacktestStatusRunning, "started_at": now, "agent_worker_nonce": nonce, "agent_lease_until": until})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, nil
	}
	row.Status = domain.BacktestStatusRunning
	row.StartedAt = &now
	row.AgentLeaseUntil = &until
	row.AgentWorkerNonce = nonce
	return &row, nil
}
func (r *BacktestRepository) FailExpiredAgent(ctx context.Context) error {
	var ids []int64
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Model(&domain.BacktestJob{}).Where("agent_run = ? AND status = ? AND agent_lease_until < ?", true, domain.BacktestStatusRunning, now.Add(-15*time.Second)).Order("id ASC").Limit(100).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&domain.BacktestJob{}).Where("id IN ? AND status = ?", ids, domain.BacktestStatusRunning).Updates(map[string]any{"status": domain.BacktestStatusFailed, "error_msg": "agent_backtest_interrupted", "finished_at": now}).Error
}
func (r *BacktestRepository) FailAgent(ctx context.Context, job *domain.BacktestJob, code string) error {
	res := r.db.WithContext(ctx).Model(&domain.BacktestJob{}).Where("id = ? AND agent_run = ? AND status = ? AND agent_worker_nonce = ?", job.ID, true, domain.BacktestStatusRunning, job.AgentWorkerNonce).Updates(map[string]any{"status": domain.BacktestStatusFailed, "error_msg": code, "finished_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrAgentBacktestLease
	}
	return nil
}
func (r *BacktestRepository) CompleteAgent(ctx context.Context, job *domain.BacktestJob, result *domain.BacktestResult, trades []*domain.Trade, equity []*domain.EquityPoint) error {
	if result == nil || len(trades) > 20000 || len(equity) > 10000 {
		return ErrAgentBacktestLease
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Claim the outcome before inserting any result rows; rollback includes all.
		res := tx.Model(&domain.BacktestJob{}).Where("id = ? AND agent_run = ? AND status = ? AND agent_worker_nonce = ? AND agent_lease_until > ?", job.ID, true, domain.BacktestStatusRunning, job.AgentWorkerNonce, time.Now().UTC()).Updates(map[string]any{"status": domain.BacktestStatusSucceed, "finished_at": time.Now().UTC(), "error_msg": ""})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrAgentBacktestLease
		}
		for _, row := range trades {
			row.JobID = job.ID
			row.ID = 0
		}
		for _, row := range equity {
			row.JobID = job.ID
		}
		result.JobID = job.ID
		if err := NewTradeRepository(tx).BulkCreate(ctx, trades); err != nil {
			return err
		}
		if err := NewEquityRepository(tx).BulkCreate(ctx, equity); err != nil {
			return err
		}
		return tx.Create(result).Error
	})
}
