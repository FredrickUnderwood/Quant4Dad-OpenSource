package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type DataSyncRepository struct {
	db *gorm.DB
}

func NewDataSyncRepository(db *gorm.DB) *DataSyncRepository {
	return &DataSyncRepository{db: db}
}

func (r *DataSyncRepository) Create(ctx context.Context, t *domain.DataSyncTask) error {
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		logger.L().Error("datasync create failed", zap.Error(err))
		return err
	}
	return nil
}

func (r *DataSyncRepository) Update(ctx context.Context, t *domain.DataSyncTask) error {
	if err := r.db.WithContext(ctx).Save(t).Error; err != nil {
		logger.L().Error("datasync update failed", zap.Int64("id", t.ID), zap.Error(err))
		return err
	}
	return nil
}

func (r *DataSyncRepository) GetByID(ctx context.Context, id int64) (*domain.DataSyncTask, error) {
	var t domain.DataSyncTask
	if err := r.db.WithContext(ctx).First(&t, id).Error; err != nil {
		logger.L().Error("datasync get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return &t, nil
}

func (r *DataSyncRepository) List(ctx context.Context, limit int) ([]*domain.DataSyncTask, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []*domain.DataSyncTask
	if err := r.db.WithContext(ctx).Order("id DESC").Limit(limit).Find(&items).Error; err != nil {
		logger.L().Error("datasync list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// RecordFailure persists a single per-code failure for later analysis.
func (r *DataSyncRepository) RecordFailure(ctx context.Context, f *domain.DataSyncFailure) error {
	if err := r.db.WithContext(ctx).Create(f).Error; err != nil {
		logger.L().Error("datasync failure record failed", zap.Int64("task", f.TaskID), zap.String("code", f.Code), zap.Error(err))
		return err
	}
	return nil
}

// ListFailures returns the earliest failures of a task (top N for the frontend).
func (r *DataSyncRepository) ListFailures(ctx context.Context, taskID int64, limit int) ([]*domain.DataSyncFailure, error) {
	if limit <= 0 {
		limit = 5
	}
	var items []*domain.DataSyncFailure
	if err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("id ASC").Limit(limit).Find(&items).Error; err != nil {
		logger.L().Error("datasync failures list failed", zap.Int64("task", taskID), zap.Error(err))
		return nil, err
	}
	return items, nil
}

// IncrementProgress atomically bumps done/failed counters; used during sync.
func (r *DataSyncRepository) IncrementProgress(ctx context.Context, id int64, doneDelta, failedDelta int) error {
	updates := map[string]any{}
	if doneDelta != 0 {
		updates["done"] = gorm.Expr("done + ?", doneDelta)
	}
	if failedDelta != 0 {
		updates["failed"] = gorm.Expr("failed + ?", failedDelta)
	}
	if len(updates) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).Model(&domain.DataSyncTask{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		logger.L().Error("datasync progress update failed", zap.Int64("id", id), zap.Error(err))
		return err
	}
	return nil
}
