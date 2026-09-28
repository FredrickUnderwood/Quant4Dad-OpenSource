package repository

import (
	"context"
	"errors"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type CostRepository struct {
	db *gorm.DB
}

func NewCostRepository(db *gorm.DB) *CostRepository {
	return &CostRepository{db: db}
}

// EnsureDefault seeds the well-known A-share fee model when no default exists.
// Called once at startup.
func (r *CostRepository) EnsureDefault(ctx context.Context) error {
	var count int64
	if err := r.db.WithContext(ctx).Model(&domain.Cost{}).Where("is_default = ?", true).Count(&count).Error; err != nil {
		logger.L().Error("cost ensure default count failed", zap.Error(err))
		return err
	}
	if count > 0 {
		return nil
	}
	seed := domain.DefaultAShareCost()
	if err := r.db.WithContext(ctx).Create(seed).Error; err != nil {
		logger.L().Error("cost ensure default create failed", zap.Error(err))
		return err
	}
	logger.L().Info("cost default seeded", zap.String("name", seed.Name))
	return nil
}

func (r *CostRepository) Create(ctx context.Context, c *domain.Cost) error {
	if err := r.db.WithContext(ctx).Create(c).Error; err != nil {
		logger.L().Error("cost create failed", zap.String("name", c.Name), zap.Error(err))
		return err
	}
	return nil
}

func (r *CostRepository) Update(ctx context.Context, c *domain.Cost) error {
	if err := r.db.WithContext(ctx).Save(c).Error; err != nil {
		logger.L().Error("cost update failed", zap.Int64("id", c.ID), zap.Error(err))
		return err
	}
	return nil
}

func (r *CostRepository) GetByID(ctx context.Context, id int64) (*domain.Cost, error) {
	var c domain.Cost
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		logger.L().Error("cost get failed", zap.Int64("id", id), zap.Error(err))
		return nil, err
	}
	return &c, nil
}

func (r *CostRepository) GetDefault(ctx context.Context) (*domain.Cost, error) {
	var c domain.Cost
	if err := r.db.WithContext(ctx).Where("is_default = ?", true).First(&c).Error; err != nil {
		logger.L().Error("cost get default failed", zap.Error(err))
		return nil, err
	}
	return &c, nil
}

func (r *CostRepository) List(ctx context.Context) ([]*domain.Cost, error) {
	var items []*domain.Cost
	if err := r.db.WithContext(ctx).Order("is_default DESC, id ASC").Find(&items).Error; err != nil {
		logger.L().Error("cost list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

// SetDefault marks the given cost as the new default and clears the flag on
// the previous one inside a single transaction.
func (r *CostRepository) SetDefault(ctx context.Context, id int64) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.Cost{}).Where("is_default = ?", true).
			Update("is_default", false).Error; err != nil {
			return err
		}
		res := tx.Model(&domain.Cost{}).Where("id = ?", id).Update("is_default", true)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errors.New("cost not found")
		}
		return nil
	})
	if err != nil {
		logger.L().Error("cost set default failed", zap.Int64("id", id), zap.Error(err))
		return err
	}
	return nil
}

func (r *CostRepository) Delete(ctx context.Context, id int64) error {
	if err := r.db.WithContext(ctx).Delete(&domain.Cost{}, id).Error; err != nil {
		logger.L().Error("cost delete failed", zap.Int64("id", id), zap.Error(err))
		return err
	}
	return nil
}
