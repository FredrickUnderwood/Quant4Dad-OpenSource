package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type BacktestRepository struct {
	db *gorm.DB
}

func NewBacktestRepository(db *gorm.DB) *BacktestRepository {
	return &BacktestRepository{db: db}
}

func (r *BacktestRepository) CreateJob(ctx context.Context, j *domain.BacktestJob) error {
	if err := r.db.WithContext(ctx).Create(j).Error; err != nil {
		logger.L().Error("backtest job create failed", zap.Error(err))
		return err
	}
	return nil
}

func (r *BacktestRepository) UpdateJob(ctx context.Context, j *domain.BacktestJob) error {
	if err := r.db.WithContext(ctx).Save(j).Error; err != nil {
		logger.L().Error("backtest job update failed", zap.Int64("id", j.ID), zap.Error(err))
		return err
	}
	return nil
}

func (r *BacktestRepository) GetJob(ctx context.Context, id int64) (*domain.BacktestJob, error) {
	var j domain.BacktestJob
	if err := r.db.WithContext(ctx).First(&j, id).Error; err != nil {
		logger.L().Error("backtest job get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return &j, nil
}

func (r *BacktestRepository) ListJobs(ctx context.Context, limit int) ([]*domain.BacktestJob, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []*domain.BacktestJob
	if err := r.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&items).Error; err != nil {
		logger.L().Error("backtest job list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

func (r *BacktestRepository) SaveResult(ctx context.Context, res *domain.BacktestResult) error {
	if err := r.db.WithContext(ctx).Save(res).Error; err != nil {
		logger.L().Error("backtest result save failed", zap.Int64("job_id", res.JobID), zap.Error(err))
		return err
	}
	return nil
}

func (r *BacktestRepository) GetResult(ctx context.Context, jobID int64) (*domain.BacktestResult, error) {
	var res domain.BacktestResult
	if err := r.db.WithContext(ctx).First(&res, "job_id = ?", jobID).Error; err != nil {
		logger.L().Error("backtest result get failed", zap.Int64("job_id", jobID), zap.Error(err))
		return nil, err
	}
	return &res, nil
}
