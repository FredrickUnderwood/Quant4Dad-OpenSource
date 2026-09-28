package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type StrategyRepository struct {
	db *gorm.DB
}

func NewStrategyRepository(db *gorm.DB) *StrategyRepository {
	return &StrategyRepository{db: db}
}

func (r *StrategyRepository) Create(ctx context.Context, s *domain.Strategy) error {
	if err := r.db.WithContext(ctx).Create(s).Error; err != nil {
		logger.L().Error("strategy create failed", zap.String("name", s.Name), zap.Error(err))
		return err
	}
	return nil
}

func (r *StrategyRepository) Update(ctx context.Context, s *domain.Strategy) error {
	res := r.db.WithContext(ctx).Model(&domain.Strategy{}).Where("id = ? AND version = ?", s.ID, s.Version).Updates(map[string]any{
		"name": s.Name, "description": s.Description, "universe": s.Universe,
		"period": s.Period, "body": s.Body, "version": gorm.Expr("version + 1"),
	})
	if res.Error != nil {
		logger.L().Error("strategy update failed", zap.Int64("id", s.ID), zap.Error(res.Error))
		return res.Error
	}
	if res.RowsAffected != 1 {
		return domain.ErrResourceConflict
	}
	s.Version++
	return nil
}

func (r *StrategyRepository) GetByID(ctx context.Context, id int64) (*domain.Strategy, error) {
	var s domain.Strategy
	if err := r.db.WithContext(ctx).First(&s, id).Error; err != nil {
		logger.L().Error("strategy get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return &s, nil
}

func (r *StrategyRepository) Delete(ctx context.Context, id int64) error {
	if err := r.db.WithContext(ctx).Delete(&domain.Strategy{}, id).Error; err != nil {
		logger.L().Error("strategy delete failed", zap.Int64("id", id), zap.Error(err))
		return err
	}
	return nil
}

func (r *StrategyRepository) List(ctx context.Context) ([]*domain.Strategy, error) {
	var items []*domain.Strategy
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error; err != nil {
		logger.L().Error("strategy list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}
