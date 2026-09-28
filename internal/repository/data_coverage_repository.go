package repository

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

type DataCoverageRepository struct {
	db *gorm.DB
}

func NewDataCoverageRepository(db *gorm.DB) *DataCoverageRepository {
	return &DataCoverageRepository{db: db}
}

// List returns every coverage row, ordered lexicographically by period.
func (r *DataCoverageRepository) List(ctx context.Context) ([]*domain.DataCoverageSummary, error) {
	var items []*domain.DataCoverageSummary
	if err := r.db.WithContext(ctx).Order("period ASC").Find(&items).Error; err != nil {
		logger.L().Error("coverage list failed", zap.Error(err))
		return nil, err
	}
	return items, nil
}

func (r *DataCoverageRepository) GetByPeriod(ctx context.Context, period domain.BarPeriod) (*domain.DataCoverageSummary, error) {
	var s domain.DataCoverageSummary
	err := r.db.WithContext(ctx).Where("period = ?", period).First(&s).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		logger.L().Error("coverage get failed", zap.String("period", string(period)), zap.Error(err))
		return nil, err
	}
	return &s, nil
}

// Upsert writes the scan result, overwriting on the unique period index.
func (r *DataCoverageRepository) Upsert(ctx context.Context, s *domain.DataCoverageSummary) error {
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "period"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"total_instruments", "complete_count", "stale_count", "empty_count",
			"latest_bar_date", "earliest_bar_date", "scanned_at", "scan_duration_ms",
			"scan_status", "last_error", "updated_at",
		}),
	}).Create(s).Error
	if err != nil {
		logger.L().Error("coverage upsert failed", zap.String("period", string(s.Period)), zap.Error(err))
		return err
	}
	return nil
}

// SetScanStatus updates scan_status alone, plus last_error when given; called when a scan
// starts and finishes.
func (r *DataCoverageRepository) SetScanStatus(ctx context.Context, period domain.BarPeriod, status domain.CoverageScanStatus, lastError string) error {
	updates := map[string]any{
		"scan_status": status,
		"updated_at":  time.Now(),
	}
	if lastError != "" || status == domain.CoverageScanIdle {
		updates["last_error"] = lastError
	}
	err := r.db.WithContext(ctx).Model(&domain.DataCoverageSummary{}).
		Where("period = ?", period).Updates(updates).Error
	if err != nil {
		logger.L().Error("coverage set scan status failed", zap.String("period", string(period)), zap.Error(err))
		return err
	}
	return nil
}

// CodeAggregate is the aggregate for one instrument at one period: the bar count and the
// earliest and latest dates.
type CodeAggregate struct {
	Code     string
	BarCount int
	MinDate  time.Time
	MaxDate  time.Time
}

// SQLite drops the declared datetime type for MIN/MAX expressions and returns
// text. MySQL normally returns time.Time when parseTime is enabled. Keep this
// conversion local to the query so callers continue to work with time.Time.
type coverageDate struct {
	time.Time
}

func (d *coverageDate) Scan(value any) error {
	d.Time = time.Time{}
	if b, ok := value.([]byte); ok {
		value = string(b)
	}
	switch v := value.(type) {
	case time.Time:
		d.Time = v
		return nil
	case string:
		// Match SQLite's persisted timestamp formats, including legacy dates.
		// Zone-free values use UTC, as with the default SQLite driver.
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02T15:04:05.999999999",
			"2006-01-02 15:04",
			"2006-01-02T15:04",
			time.DateOnly,
		} {
			if parsed, err := time.Parse(layout, v); err == nil {
				d.Time = parsed
				return nil
			}
		}
		return errors.New("invalid coverage aggregate date")
	case nil:
		return errors.New("coverage aggregate date is NULL")
	default:
		return errors.New("unsupported coverage aggregate date type")
	}
}

// AggregateByCode gets (code, count, min(date), max(date)) with a single GROUP BY query.
func (r *DataCoverageRepository) AggregateByCode(ctx context.Context, period domain.BarPeriod) ([]CodeAggregate, error) {
	rows, err := r.db.WithContext(ctx).
		Table("bar").
		Select("code as code, COUNT(*) as bar_count, MIN(date) as min_date, MAX(date) as max_date").
		Where("period = ?", period).
		Group("code").
		Rows()
	if err != nil {
		logger.L().Error("coverage aggregate failed", zap.String("period", string(period)), zap.Error(err))
		return nil, err
	}
	defer rows.Close()

	var aggregates []CodeAggregate
	for rows.Next() {
		var aggregate CodeAggregate
		var minDate, maxDate coverageDate
		if err := rows.Scan(&aggregate.Code, &aggregate.BarCount, &minDate, &maxDate); err != nil {
			logger.L().Error("coverage aggregate scan failed",
				zap.String("period", string(period)), zap.String("code", aggregate.Code), zap.Error(err))
			return nil, err
		}
		aggregate.MinDate, aggregate.MaxDate = minDate.Time, maxDate.Time
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		logger.L().Error("coverage aggregate iteration failed", zap.String("period", string(period)), zap.Error(err))
		return nil, err
	}
	return aggregates, nil
}
