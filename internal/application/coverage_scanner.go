package application

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
)

// Defaults: staleDays = the newest bar lagging today by 7 calendar days counts as stale.
const (
	defaultStaleDays     = 7
	defaultScanInterval  = 6 * time.Hour
	scannedPeriodsConfig = "1d,1w,1mo" // default list of periods to scan when unconfigured
)

// CoverageScanner scans the bar table, computes a coverage summary per period, and
// writes it to data_coverage_summary. Only one scan runs at a time per process.
type CoverageScanner struct {
	repo        *repository.DataCoverageRepository
	instruments *repository.InstrumentRepository
	periods     []domain.BarPeriod
	staleDays   int
	interval    time.Duration

	running atomic.Bool
}

func NewCoverageScanner(
	repo *repository.DataCoverageRepository,
	instruments *repository.InstrumentRepository,
) *CoverageScanner {
	return &CoverageScanner{
		repo:        repo,
		instruments: instruments,
		periods:     []domain.BarPeriod{domain.Bar1d, domain.Bar1w, domain.Bar1mo},
		staleDays:   defaultStaleDays,
		interval:    defaultScanInterval,
	}
}

// List returns the most recent scan result for each period.
func (s *CoverageScanner) List(ctx context.Context) ([]*domain.DataCoverageSummary, error) {
	return s.repo.List(ctx)
}

// IsRunning tells the handler whether a scan is currently executing.
func (s *CoverageScanner) IsRunning() bool { return s.running.Load() }

// TriggerAsync starts a scan asynchronously, returning ErrCoverageScanBusy if one is
// already running.
func (s *CoverageScanner) TriggerAsync() error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrCoverageScanBusy
	}
	go func() {
		defer s.running.Store(false)
		ctx := context.Background()
		if err := s.scanAll(ctx); err != nil {
			logger.L().Error("coverage scan failed", zap.Error(err))
		}
	}()
	return nil
}

// ErrCoverageScanBusy is returned when a scan is already running; the handler maps it
// to 409.
var ErrCoverageScanBusy = errors.New("coverage scan already running")

// StartCron runs once at startup and then every interval. It returns a cancel for
// graceful shutdown.
func (s *CoverageScanner) StartCron(interval time.Duration) context.CancelFunc {
	if interval <= 0 {
		interval = s.interval
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Run once at startup so a fresh deployment does not leave the UI showing "no data"
		// for a long time.
		_ = s.TriggerAsync()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.TriggerAsync(); err != nil && !errors.Is(err, ErrCoverageScanBusy) {
					logger.L().Warn("coverage cron trigger failed", zap.Error(err))
				}
			}
		}
	}()
	return cancel
}

func (s *CoverageScanner) scanAll(ctx context.Context) error {
	var scanErr error
	for _, p := range s.periods {
		scanErr = errors.Join(scanErr, s.scanOne(ctx, p))
	}
	return scanErr
}

func (s *CoverageScanner) scanOne(ctx context.Context, period domain.BarPeriod) error {
	started := time.Now()
	// Write a running status first, so the UI knows a scan is in progress.
	summary, err := s.repo.GetByPeriod(ctx, period)
	if err != nil {
		return err
	}
	// Get + Upsert rather than SetScanStatus directly, because on the first run the row
	// does not exist yet.
	if summary == nil {
		summary = &domain.DataCoverageSummary{Period: period}
	}
	summary.ScanStatus = domain.CoverageScanRunning
	summary.LastError = ""
	summary.UpdatedAt = started
	if err := s.repo.Upsert(ctx, summary); err != nil {
		return err
	}
	logger.L().Info("coverage scan started", zap.String("period", string(period)))

	codes, err := s.instruments.ListAllCodes(ctx)
	if err != nil {
		return s.finishWithError(ctx, summary, err, started)
	}
	aggs, err := s.repo.AggregateByCode(ctx, period)
	if err != nil {
		return s.finishWithError(ctx, summary, err, started)
	}

	summary = classify(codes, aggs, time.Now(), s.staleDays)
	summary.Period = period
	finished := time.Now()
	summary.ScannedAt = &finished
	summary.ScanDurationMs = finished.Sub(started).Milliseconds()
	summary.ScanStatus = domain.CoverageScanIdle
	summary.LastError = ""
	summary.UpdatedAt = finished
	if err := s.repo.Upsert(ctx, summary); err != nil {
		return err
	}
	logger.L().Info("coverage scan completed",
		zap.String("period", string(period)), zap.Int("instruments", summary.TotalInstruments),
		zap.Int64("duration_ms", summary.ScanDurationMs))
	return nil
}

func (s *CoverageScanner) finishWithError(ctx context.Context, summary *domain.DataCoverageSummary, scanErr error, started time.Time) error {
	finished := time.Now()
	// Preserve the last successful counts and date range when this scan fails.
	summary.ScannedAt = &finished
	summary.ScanDurationMs = finished.Sub(started).Milliseconds()
	summary.ScanStatus = domain.CoverageScanIdle
	summary.LastError = scanErr.Error()
	summary.UpdatedAt = finished
	return errors.Join(scanErr, s.repo.Upsert(ctx, summary))
}

// classify buckets the aggregate results into the summary. Extracted to be unit-testable.
func classify(
	allCodes []string,
	aggs []repository.CodeAggregate,
	now time.Time,
	staleDays int,
) *domain.DataCoverageSummary {
	aggMap := make(map[string]repository.CodeAggregate, len(aggs))
	for _, a := range aggs {
		aggMap[a.Code] = a
	}
	out := &domain.DataCoverageSummary{TotalInstruments: len(allCodes)}
	staleCutoff := now.AddDate(0, 0, -staleDays)

	var globalMin, globalMax time.Time
	for _, code := range allCodes {
		a, has := aggMap[code]
		if !has || a.BarCount == 0 {
			out.EmptyCount++
			continue
		}
		if globalMin.IsZero() || a.MinDate.Before(globalMin) {
			globalMin = a.MinDate
		}
		if a.MaxDate.After(globalMax) {
			globalMax = a.MaxDate
		}
		if a.MaxDate.Before(staleCutoff) {
			out.StaleCount++
			continue
		}
		out.CompleteCount++
	}
	if !globalMin.IsZero() {
		out.EarliestBarDate = &globalMin
	}
	if !globalMax.IsZero() {
		out.LatestBarDate = &globalMax
	}
	return out
}
