package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type EquityRepository struct {
	db *gorm.DB
}

func NewEquityRepository(db *gorm.DB) *EquityRepository {
	return &EquityRepository{db: db}
}

func (r *EquityRepository) BulkCreate(ctx context.Context, points []*domain.EquityPoint) error {
	if len(points) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).CreateInBatches(&points, 500).Error; err != nil {
		logger.L().Error("equity bulk create failed", zap.Int("count", len(points)), zap.Error(err))
		return err
	}
	return nil
}

func (r *EquityRepository) ListByJob(ctx context.Context, jobID int64) ([]*domain.EquityPoint, error) {
	var items []*domain.EquityPoint
	if err := r.db.WithContext(ctx).Where("job_id = ?", jobID).
		Order("date ASC").Find(&items).Error; err != nil {
		logger.L().Error("equity list failed", zap.Int64("job_id", jobID), zap.Error(err))
		return nil, err
	}
	return items, nil
}

func (r *EquityRepository) DeleteByJob(ctx context.Context, jobID int64) error {
	if err := r.db.WithContext(ctx).Where("job_id = ?", jobID).Delete(&domain.EquityPoint{}).Error; err != nil {
		logger.L().Error("equity delete failed", zap.Int64("job_id", jobID), zap.Error(err))
		return err
	}
	return nil
}
