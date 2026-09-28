package repository

import (
	"context"
	"encoding/json"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type SettingRepository struct {
	db *gorm.DB
}

func NewSettingRepository(db *gorm.DB) *SettingRepository {
	return &SettingRepository{db: db}
}

// Get returns the setting for a key, or gorm.ErrRecordNotFound when there is none.
func (r *SettingRepository) Get(ctx context.Context, key string) (*domain.Setting, error) {
	var s domain.Setting
	if err := r.db.WithContext(ctx).First(&s, "`key` = ?", key).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// Upsert inserts or updates one setting, updating on a primary-key conflict; portable
// across sqlite and mysql.
func (r *SettingRepository) Upsert(ctx context.Context, key string, value json.RawMessage) error {
	s := domain.Setting{Key: key, Value: value}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&s).Error
	if err != nil {
		logger.L().Error("setting upsert failed", zap.String("key", key), zap.Error(err))
	}
	return err
}

func (r *SettingRepository) List(ctx context.Context) ([]*domain.Setting, error) {
	var items []*domain.Setting
	if err := r.db.WithContext(ctx).Order("`key` ASC").Find(&items).Error; err != nil {
		logger.L().Error("setting list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

func (r *SettingRepository) Delete(ctx context.Context, key string) error {
	if err := r.db.WithContext(ctx).Delete(&domain.Setting{}, "`key` = ?", key).Error; err != nil {
		logger.L().Error("setting delete failed", zap.String("key", key), zap.Error(err))
		return err
	}
	return nil
}
