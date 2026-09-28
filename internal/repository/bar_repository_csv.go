package repository

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
)

// csvBarRepository stores bars as `{code}.{period}.csv` files under a base dir.
// Each file is sorted ascending by date. Upsert merges by (date) and rewrites
// the file. Suitable for personal-scale data; a single file ≈ 10 years of daily
// bars (~2500 rows) is trivial.
type csvBarRepository struct {
	baseDir string
	mu      sync.Mutex // serialize file writes; reads also take the lock for simplicity
}

const csvDateLayout = "2006-01-02"

var csvHeader = []string{"date", "open", "high", "low", "close", "volume", "amount", "adj_factor", "adj_source"}

func NewCSVBarRepository(baseDir string) (BarRepository, error) {
	if baseDir == "" {
		return nil, errors.New("csv bar repo: baseDir is empty")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, err
	}
	return &csvBarRepository{baseDir: baseDir}, nil
}

func (r *csvBarRepository) path(code string, period domain.BarPeriod) string {
	return filepath.Join(r.baseDir, code+"."+string(period)+".csv")
}

func (r *csvBarRepository) Upsert(ctx context.Context, bars []*domain.Bar) error {
	if len(bars) == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// group by (code, period)
	type key struct {
		code   string
		period domain.BarPeriod
	}
	grouped := map[key][]*domain.Bar{}
	for _, b := range bars {
		k := key{b.Code, b.Period}
		grouped[k] = append(grouped[k], b)
	}

	for k, group := range grouped {
		existing, err := r.readAll(k.code, k.period)
		if err != nil {
			return err
		}
		merged := mergeBars(existing, group)
		if err := r.writeAll(k.code, k.period, merged); err != nil {
			return err
		}
	}
	return nil
}

func (r *csvBarRepository) Range(ctx context.Context, code string, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.readAll(code, period)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Bar, 0, len(all))
	for _, b := range all {
		if !start.IsZero() && b.Date.Before(start) {
			continue
		}
		if !end.IsZero() && b.Date.After(end) {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *csvBarRepository) LatestDate(ctx context.Context, code string, period domain.BarPeriod) (time.Time, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.readAll(code, period)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(all) == 0 {
		return time.Time{}, false, nil
	}
	return all[len(all)-1].Date, true, nil
}

func (r *csvBarRepository) readAll(code string, period domain.BarPeriod) ([]*domain.Bar, error) {
	path := r.path(code, period)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		logger.L().Error("csv bar open failed", zap.String("path", path), zap.Error(err))
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	rows, err := reader.ReadAll()
	if err != nil {
		logger.L().Error("csv bar read failed", zap.String("path", path), zap.Error(err))
		return nil, err
	}
	if len(rows) <= 1 {
		return nil, nil
	}
	out := make([]*domain.Bar, 0, len(rows)-1)
	for _, row := range rows[1:] {
		b, err := parseCSVRow(code, period, row)
		if err != nil {
			logger.L().Error("csv bar parse failed", zap.String("path", path), zap.Error(err))
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (r *csvBarRepository) writeAll(code string, period domain.BarPeriod, bars []*domain.Bar) error {
	path := r.path(code, period)
	f, err := os.Create(path)
	if err != nil {
		logger.L().Error("csv bar create failed", zap.String("path", path), zap.Error(err))
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write(csvHeader); err != nil {
		logger.L().Error("csv bar write header failed", zap.String("path", path), zap.Error(err))
		return err
	}
	for _, b := range bars {
		row := []string{
			b.Date.Format(csvDateLayout),
			ftoa(b.Open), ftoa(b.High), ftoa(b.Low), ftoa(b.Close),
			ftoa(b.Volume), ftoa(b.Amount), ftoa(b.AdjFactor), b.AdjSource,
		}
		if err := w.Write(row); err != nil {
			logger.L().Error("csv bar write row failed", zap.String("path", path), zap.Error(err))
			return err
		}
	}
	return nil
}

func parseCSVRow(code string, period domain.BarPeriod, row []string) (*domain.Bar, error) {
	if len(row) < 8 {
		return nil, errors.New("csv bar: row column mismatch")
	}
	d, err := time.Parse(csvDateLayout, row[0])
	if err != nil {
		return nil, err
	}
	source := ""
	if len(row) >= 9 {
		source = row[8]
	}
	return &domain.Bar{
		Code:      code,
		Period:    period,
		Date:      d,
		Open:      atof(row[1]),
		High:      atof(row[2]),
		Low:       atof(row[3]),
		Close:     atof(row[4]),
		Volume:    atof(row[5]),
		Amount:    atof(row[6]),
		AdjFactor: atof(row[7]),
		AdjSource: source,
	}, nil
}

func mergeBars(existing, incoming []*domain.Bar) []*domain.Bar {
	idx := make(map[string]*domain.Bar, len(existing)+len(incoming))
	for _, b := range existing {
		idx[b.Date.Format(csvDateLayout)] = b
	}
	for _, b := range incoming {
		idx[b.Date.Format(csvDateLayout)] = b
	}
	out := make([]*domain.Bar, 0, len(idx))
	for _, b := range idx {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func atof(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
