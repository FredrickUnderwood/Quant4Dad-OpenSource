package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrCSVImportSchema = errors.New("import requires the current instrument and bar schema; start the API or run its migration first")
var ErrCSVImportEngine = errors.New("MySQL CSV import requires InnoDB instrument and bar tables for atomic transactions")

// CSVImportRepository provides one SQL transaction for instrument metadata and
// bars. It intentionally does not support the non-transactional CSV backend.
type CSVImportRepository struct{ db *gorm.DB }

func NewCSVImportRepository(db *gorm.DB) *CSVImportRepository { return &CSVImportRepository{db: db} }

type CSVImportTransaction struct {
	db   *gorm.DB
	lock bool
}

func (r *CSVImportRepository) CheckSchema(ctx context.Context) error {
	if r.db.Dialector.Name() == "mysql" {
		var transactional int64
		if err := r.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('instrument', 'bar') AND engine = 'InnoDB'").Scan(&transactional).Error; err != nil {
			logger.L().Error("CSV import storage engine check failed", zap.Error(err))
			return err
		}
		if transactional != 2 {
			return ErrCSVImportEngine
		}
	}
	for _, model := range []any{&domain.Instrument{}, &domain.Bar{}} {
		stmt := &gorm.Statement{DB: r.db}
		if err := stmt.Parse(model); err != nil {
			return err
		}
		fields := make([]string, 0, len(stmt.Schema.DBNames))
		fields = append(fields, stmt.Schema.DBNames...)
		var rows []map[string]any
		if err := r.db.WithContext(ctx).Model(model).Select(fields).Limit(0).Find(&rows).Error; err != nil {
			logger.L().Error("CSV import schema check failed", zap.String("table", stmt.Schema.Table), zap.Error(err))
			return ErrCSVImportSchema
		}
	}
	return nil
}

func (r *CSVImportRepository) Transaction(ctx context.Context, dryRun bool, fn func(*CSVImportTransaction) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&CSVImportTransaction{db: tx, lock: !dryRun})
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
}

func (t *CSVImportTransaction) query(ctx context.Context) *gorm.DB {
	q := t.db.WithContext(ctx)
	if t.lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return q
}

func (t *CSVImportTransaction) Instruments(ctx context.Context, codes []string) ([]*domain.Instrument, error) {
	var rows []*domain.Instrument
	err := t.query(ctx).Where("code IN ?", codes).Order("code ASC").Find(&rows).Error
	if err != nil {
		logger.L().Error("CSV import instrument lookup failed", zap.Error(err))
	}
	return rows, err
}

func (t *CSVImportTransaction) Bars(ctx context.Context, code string, period domain.BarPeriod, dates []time.Time) ([]*domain.Bar, error) {
	var rows []*domain.Bar
	err := t.query(ctx).Where("code = ? AND period = ? AND date IN ?", code, period, dates).Order("date ASC").Find(&rows).Error
	if err != nil {
		logger.L().Error("CSV import bar lookup failed", zap.Error(err))
	}
	return rows, err
}

// New keys use plain INSERT, so a concurrent writer cannot be overwritten even
// if it created the same key after validation. Any duplicate rolls back the batch.
func (t *CSVImportTransaction) CreateInstruments(ctx context.Context, rows []*domain.Instrument) error {
	if len(rows) == 0 {
		return nil
	}
	err := t.db.WithContext(ctx).CreateInBatches(rows, 40).Error
	if err != nil {
		logger.L().Error("CSV import instrument insert failed", zap.Error(err))
	}
	return err
}

func (t *CSVImportTransaction) CreateBars(ctx context.Context, rows []*domain.Bar) error {
	if len(rows) == 0 {
		return nil
	}
	err := t.db.WithContext(ctx).CreateInBatches(rows, 80).Error
	if err != nil {
		logger.L().Error("CSV import bar insert failed", zap.Error(err))
	}
	return err
}

// Updates are only used after the service has authorized explicit replacement
// and read the current rows under the same transaction's write locks.
func (t *CSVImportTransaction) ReplaceInstruments(ctx context.Context, rows []*domain.Instrument) error {
	return NewInstrumentRepository(t.db).Upsert(ctx, rows)
}

func (t *CSVImportTransaction) ReplaceBars(ctx context.Context, rows []*domain.Bar) error {
	return NewGormBarRepository(t.db).Upsert(ctx, rows)
}
