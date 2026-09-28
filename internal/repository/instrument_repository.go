package repository

import (
	"context"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type InstrumentRepository struct {
	db *gorm.DB
}

func NewInstrumentRepository(db *gorm.DB) *InstrumentRepository {
	return &InstrumentRepository{db: db}
}

type ListInstrumentsFilter struct {
	Keyword   string
	AssetType domain.AssetType
	Page      int
	Size      int
}

func (r *InstrumentRepository) Upsert(ctx context.Context, items []*domain.Instrument) error {
	if len(items) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}},
		DoUpdates: clause.AssignmentColumns([]string{"name", "industry", "listed_date", "status", "asset_type", "exchange", "full_name", "index_code", "index_name", "manager", "custodian", "etf_type", "management_fee", "setup_date", "updated_at"}),
	}).CreateInBatches(&items, 500).Error
	if err != nil {
		logger.L().Error("instrument upsert failed", zap.Int("count", len(items)), zap.Error(err))
		return err
	}
	return nil
}

func (r *InstrumentRepository) GetByCode(ctx context.Context, code string) (*domain.Instrument, error) {
	var it domain.Instrument
	if err := r.db.WithContext(ctx).First(&it, "code = ?", code).Error; err != nil {
		logger.L().Error("instrument get failed", zap.String("code", code), zap.Error(err))
		return nil, err
	}
	return &it, nil
}

func (r *InstrumentRepository) ListAllCodes(ctx context.Context) ([]string, error) {
	var codes []string
	if err := r.db.WithContext(ctx).Model(&domain.Instrument{}).
		Where("status = ?", "active").
		Pluck("code", &codes).Error; err != nil {
		logger.L().Error("instrument list codes failed", zap.Error(err))
		return nil, err
	}
	return codes, nil
}

func (r *InstrumentRepository) List(ctx context.Context, f ListInstrumentsFilter) ([]*domain.Instrument, int64, error) {
	var items []*domain.Instrument
	var total int64
	q := r.db.WithContext(ctx).Model(&domain.Instrument{})
	if f.AssetType != "" {
		q = q.Where("asset_type = ?", f.AssetType)
	}
	if f.Keyword != "" {
		kw := "%" + f.Keyword + "%"
		q = q.Where("code LIKE ? OR name LIKE ?", kw, kw)
	}
	if err := q.Count(&total).Error; err != nil {
		logger.L().Error("instrument count failed", zap.Error(err))
		return nil, 0, err
	}
	if f.Size <= 0 {
		f.Size = 50
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	offset := (f.Page - 1) * f.Size
	if err := q.Order("code ASC").Offset(offset).Limit(f.Size).Find(&items).Error; err != nil {
		logger.L().Error("instrument list failed", zap.Error(err))
		return nil, 0, err
	}
	return items, total, nil
}
