package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type TradeRepository struct {
	db *gorm.DB
}

func NewTradeRepository(db *gorm.DB) *TradeRepository {
	return &TradeRepository{db: db}
}

func (r *TradeRepository) BulkCreate(ctx context.Context, trades []*domain.Trade) error {
	if len(trades) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).CreateInBatches(&trades, 500).Error; err != nil {
		logger.L().Error("trade bulk create failed", zap.Int("count", len(trades)), zap.Error(err))
		return err
	}
	return nil
}

func (r *TradeRepository) ListByJob(ctx context.Context, jobID int64) ([]*domain.Trade, error) {
	var trades []*domain.Trade
	if err := r.db.WithContext(ctx).Where("job_id = ?", jobID).
		Order("time ASC, id ASC").Find(&trades).Error; err != nil {
		logger.L().Error("trade list failed", zap.Int64("job_id", jobID), zap.Error(err))
		return nil, err
	}
	return trades, nil
}

func (r *TradeRepository) DeleteByJob(ctx context.Context, jobID int64) error {
	if err := r.db.WithContext(ctx).Where("job_id = ?", jobID).Delete(&domain.Trade{}).Error; err != nil {
		logger.L().Error("trade delete failed", zap.Int64("job_id", jobID), zap.Error(err))
		return err
	}
	return nil
}
