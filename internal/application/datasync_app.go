package application

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/datasource"
	"github.com/quant4dad/internal/service"
)

// DataSyncApp coordinates pulling external bar data into the local bar
// repository. Tasks run in the background; progress lives on the DataSyncTask
// record.
type DataSyncApp struct {
	cfg         config.DatasourceConfig
	tasks       *service.DataSyncService
	instruments *service.InstrumentService
	bars        repository.BarRepository
	client      datasource.Client
}

func NewDataSyncApp(
	cfg config.DatasourceConfig,
	tasks *service.DataSyncService,
	inst *service.InstrumentService,
	bars repository.BarRepository,
	client datasource.Client,
) *DataSyncApp {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.InitialYears <= 0 {
		cfg.InitialYears = 10
	}
	return &DataSyncApp{cfg: cfg, tasks: tasks, instruments: inst, bars: bars, client: client}
}

// Enqueue persists the task in pending state and dispatches a background runner.
func (a *DataSyncApp) Enqueue(ctx context.Context, task *domain.DataSyncTask) (*domain.DataSyncTask, error) {
	if datasource.IsUnavailable(a.client) {
		return nil, datasource.ErrNoProvider
	}
	if task.Period == "" {
		task.Period = domain.Bar1d
	}
	if task.Mode == "" {
		task.Mode = domain.DataSyncModeIncremental
	}
	// MySQL strict mode rejects '0000-00-00', so store the default when the user leaves
	// this empty; computeRange still works off task.StartDate/EndDate afterwards.
	now := time.Now()
	if task.EndDate.IsZero() {
		task.EndDate = now
	}
	if task.StartDate.IsZero() {
		task.StartDate = now.AddDate(-a.cfg.InitialYears, 0, 0)
	}
	task.Status = domain.DataSyncStatusPending
	if err := a.tasks.Create(ctx, task); err != nil {
		return nil, err
	}
	logger.L().Info("datasync enqueued",
		zap.Int64("id", task.ID),
		zap.String("mode", string(task.Mode)),
		zap.String("period", string(task.Period)),
	)
	go a.runDetached(task.ID)
	return task, nil
}

func (a *DataSyncApp) runDetached(id int64) {
	ctx := context.Background()
	if err := a.run(ctx, id); err != nil {
		logger.L().Error("datasync run failed", zap.Int64("id", id), zap.Error(err))
	}
}

func (a *DataSyncApp) run(ctx context.Context, id int64) error {
	task, err := a.tasks.GetByID(ctx, id)
	if err != nil {
		return err
	}
	now := time.Now()
	task.Status = domain.DataSyncStatusRunning
	task.StartedAt = &now
	if err := a.tasks.Update(ctx, task); err != nil {
		return err
	}

	// 1) refresh instrument list (cheap) when no explicit codes were requested.
	codes := []string(task.Codes)
	if len(codes) == 0 {
		insts, err := a.client.ListInstruments(ctx)
		if err != nil {
			return a.failTask(ctx, task, "$catalog", err)
		}
		if err := a.instruments.Upsert(ctx, insts); err != nil {
			return a.failTask(ctx, task, "$catalog", err)
		}
		for _, it := range insts {
			if it.Status == "pending" || (it.Status == "delisted" && task.Mode != domain.DataSyncModeFull) {
				continue
			}
			// ETF collection is daily-only; stock weekly/monthly tasks keep
			// their original universe. Explicit unsupported ETF tasks fail.
			if it.AssetType == domain.AssetETF && task.Period != domain.Bar1d {
				continue
			}
			codes = append(codes, it.Code)
		}
	}

	if len(codes) == 0 {
		task.Status = domain.DataSyncStatusFailed
		task.ErrorMsg = "no instruments to sync"
		fin := time.Now()
		task.FinishedAt = &fin
		return a.tasks.Update(ctx, task)
	}

	task.Total = len(codes)
	if err := a.tasks.Update(ctx, task); err != nil {
		return err
	}

	// 2) fan out per-code fetches under a bounded worker pool.
	sem := make(chan struct{}, a.cfg.Concurrency)
	var wg sync.WaitGroup
	var doneCount, failedCount atomic.Int64
	for _, code := range codes {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := a.syncOne(ctx, task, code); err != nil {
				failedCount.Add(1)
				logger.L().Warn("datasync one failed", zap.String("code", code), zap.Error(err))
				a.recordFailure(ctx, task.ID, code, err)
				_ = a.tasks.IncrementProgress(ctx, task.ID, 0, 1)
				return
			}
			doneCount.Add(1)
			_ = a.tasks.IncrementProgress(ctx, task.ID, 1, 0)
		}(code)
	}
	wg.Wait()

	fin := time.Now()
	task.FinishedAt = &fin
	// Progress updates are best-effort while workers run. The final counters
	// come from actual worker outcomes, so a failed progress UPDATE can never
	// turn a provider failure into a successful task.
	task.Done = int(doneCount.Load())
	task.Failed = int(failedCount.Load())
	task.Status = domain.DataSyncStatusSucceed
	if task.Failed > 0 {
		task.Status = domain.DataSyncStatusFailed
		task.ErrorMsg = strconv.Itoa(task.Failed) + " instruments failed; inspect sync failures and retry"
	}
	return a.tasks.Update(ctx, task)
}

func (a *DataSyncApp) failTask(ctx context.Context, task *domain.DataSyncTask, code string, cause error) error {
	a.recordFailure(ctx, task.ID, code, cause)
	fin := time.Now()
	task.Status = domain.DataSyncStatusFailed
	task.ErrorMsg = cause.Error()
	task.FinishedAt = &fin
	return a.tasks.Update(ctx, task)
}

// recordFailure persists a per-code failure together with the raw provider
// response (when available) so failed cases can be inspected from the frontend.
func (a *DataSyncApp) recordFailure(ctx context.Context, taskID int64, code string, cause error) {
	f := &domain.DataSyncFailure{
		TaskID: taskID,
		Code:   code,
		Reason: cause.Error(),
	}
	var fe *datasource.FetchError
	if errors.As(cause, &fe) {
		f.HTTPStatus = fe.StatusCode
		f.Response = fe.Body
	}
	if err := a.tasks.RecordFailure(ctx, f); err != nil {
		logger.L().Warn("datasync record failure failed",
			zap.Int64("task", taskID), zap.String("code", code), zap.Error(err))
	}
}

func (a *DataSyncApp) syncOne(ctx context.Context, task *domain.DataSyncTask, code string) error {
	start, end, err := a.computeRange(ctx, task, code)
	if err != nil {
		return err
	}
	if end.Before(start) {
		return nil
	}
	var bars []*domain.Bar
	if typed, ok := a.client.(datasource.InstrumentBarsClient); ok {
		instrument, lookupErr := a.instruments.GetByCode(ctx, code)
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			instrument = &domain.Instrument{Code: code}
		} else if lookupErr != nil {
			return lookupErr
		}
		bars, err = typed.FetchInstrumentBars(ctx, instrument, task.Period, start, end)
	} else {
		bars, err = a.client.FetchBars(ctx, code, task.Period, start, end)
	}
	if err != nil {
		return err
	}
	if len(bars) == 0 {
		return nil
	}
	return a.bars.Upsert(ctx, bars)
}

func (a *DataSyncApp) computeRange(ctx context.Context, task *domain.DataSyncTask, code string) (time.Time, time.Time, error) {
	end := task.EndDate
	if end.IsZero() {
		end = time.Now()
	}
	switch task.Mode {
	case domain.DataSyncModeFull:
		start := task.StartDate
		if start.IsZero() {
			start = time.Now().AddDate(-a.cfg.InitialYears, 0, 0)
		}
		return start, end, nil
	default: // incremental
		latest, has, err := a.bars.LatestDate(ctx, code, task.Period)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		if !has {
			start := task.StartDate
			if start.IsZero() {
				start = time.Now().AddDate(-a.cfg.InitialYears, 0, 0)
			}
			return start, end, nil
		}
		return latest.AddDate(0, 0, 1), end, nil
	}
}
