package repository

import (
	"context"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// BarRepository abstracts the bar (OHLCV) storage so the runtime can swap
// between sqlite/mysql/csv backends. Methods are time-range based.
type BarRepository interface {
	Upsert(ctx context.Context, bars []*domain.Bar) error
	Range(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error)
	LatestDate(ctx context.Context, code string, period domain.BarPeriod) (time.Time, bool, error)
}

// gormBarRepository persists bars in a single `bar` table; used for sqlite & mysql.
type gormBarRepository struct {
	db *gorm.DB
}

func NewGormBarRepository(db *gorm.DB) BarRepository {
	return &gormBarRepository{db: db}
}

func (r *gormBarRepository) Upsert(ctx context.Context, bars []*domain.Bar) error {
	if len(bars) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "code"}, {Name: "period"}, {Name: "date"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"open", "high", "low", "close", "volume", "amount", "adj_factor", "adj_source",
		}),
	}).CreateInBatches(&bars, 500).Error
	if err != nil {
		logger.L().Error("bar upsert failed", zap.Int("count", len(bars)), zap.Error(err))
		return err
	}
	return nil
}

func (r *gormBarRepository) Range(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	var bars []*domain.Bar
	q := r.db.WithContext(ctx).Where("code = ? AND period = ?", code, period)
	if !start.IsZero() {
		q = q.Where("date >= ?", start)
	}
	if !end.IsZero() {
		q = q.Where("date <= ?", end)
	}
	if err := q.Order("date ASC").Find(&bars).Error; err != nil {
		logger.L().Error("bar range query failed",
			zap.String("code", code), zap.String("period", string(period)), zap.Error(err))
		return nil, err
	}
	return bars, nil
}

func (r *gormBarRepository) LatestDate(ctx context.Context, code string, period domain.BarPeriod) (time.Time, bool, error) {
	var b domain.Bar
	err := r.db.WithContext(ctx).Where("code = ? AND period = ?", code, period).
		Order("date DESC").Limit(1).First(&b).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return time.Time{}, false, nil
		}
		logger.L().Error("bar latest date failed",
			zap.String("code", code), zap.String("period", string(period)), zap.Error(err))
		return time.Time{}, false, err
	}
	return b.Date, true, nil
}
