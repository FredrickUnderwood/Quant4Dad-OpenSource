package repository

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"go.uber.org/zap"
)

// BoundedBarRepository is deliberately separate from bulk backtest reads.
// No fallback to Range is permitted for a model-facing query.
type BoundedBarRepository interface {
	RangeLatest(context.Context, string, domain.BarPeriod, time.Time, time.Time, int) ([]*domain.Bar, error)
}

var ErrBarQueryLimit = errors.New("bar_query_limit_invalid")

func (r *gormBarRepository) RangeLatest(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time, limit int) ([]*domain.Bar, error) {
	if limit < 1 || limit > 501 {
		return nil, ErrBarQueryLimit
	}
	bars := make([]*domain.Bar, 0)
	q := r.db.WithContext(ctx).Where("code = ? AND period = ?", code, period)
	if !start.IsZero() {
		q = q.Where("date >= ?", start)
	}
	if !end.IsZero() {
		q = q.Where("date <= ?", end)
	}
	if err := q.Order("date DESC").Limit(limit).Find(&bars).Error; err != nil {
		logger.L().Error("bounded bar query failed", zap.Error(err))
		return nil, err
	}
	slices.Reverse(bars)
	return bars, nil
}

// CSV files are sorted on write. Read incrementally with a fixed-size ring,
// checking ordering and cancellation instead of retaining the full file.
func (r *csvBarRepository) RangeLatest(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time, limit int) ([]*domain.Bar, error) {
	if limit < 1 || limit > 501 {
		return nil, ErrBarQueryLimit
	}
	ring := make([]*domain.Bar, limit)
	count := 0
	err := r.walkBounded(ctx, code, period, start, end, func(bar *domain.Bar) { ring[count%limit] = bar; count++ })
	if err != nil {
		return nil, err
	}
	n := min(count, limit)
	out := make([]*domain.Bar, n)
	for i := range out {
		out[i] = ring[(count-n+i)%limit]
	}
	return out, nil
}

// walkBounded shares the validated streaming reader between queries and coverage.
func (r *csvBarRepository) walkBounded(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time, visit func(*domain.Bar)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(r.path(code, period))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	// A corrupt or unexpectedly huge local file must not make a query unbounded.
	bounded := &io.LimitedReader{R: f, N: (64 << 20) + 1}
	reader := csv.NewReader(bounded)
	reader.FieldsPerRecord = 0
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil || (!slices.Equal(header, csvHeader) && !slices.Equal(header, csvHeader[:8])) {
		return errors.New("bar_csv_invalid")
	}
	var previous time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := reader.Read()
		if bounded.N <= 0 {
			return errors.New("bar_csv_too_large")
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("bar_csv_invalid")
		}
		for _, value := range row[1:8] {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return errors.New("bar_csv_invalid")
			}
		}
		bar, err := parseCSVRow(code, period, row)
		if err != nil || (!previous.IsZero() && !bar.Date.After(previous)) {
			return errors.New("bar_csv_invalid")
		}
		previous = bar.Date
		if !end.IsZero() && bar.Date.After(end) {
			break
		}
		if !start.IsZero() && bar.Date.Before(start) {
			continue
		}
		visit(bar)
	}
	return nil
}
