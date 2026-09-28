package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
	"github.com/quant4dad/internal/expr"
	"github.com/quant4dad/internal/indicator"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/script"
	"github.com/quant4dad/internal/service"
)

// BacktestApp orchestrates a single backtest job: load → compute → engine → persist.
// Jobs run inside a bounded worker pool to avoid runaway concurrency.
type BacktestApp struct {
	backtest  *service.BacktestService
	strategy  *service.StrategyService
	cost      *service.CostService
	bars      repository.BarRepository
	workerSem chan struct{}
}

func NewBacktestApp(
	bt *service.BacktestService,
	st *service.StrategyService,
	cost *service.CostService,
	bars repository.BarRepository,
	workerPool int,
) *BacktestApp {
	if workerPool <= 0 {
		workerPool = 4
	}
	return &BacktestApp{
		backtest:  bt,
		strategy:  st,
		cost:      cost,
		bars:      bars,
		workerSem: make(chan struct{}, workerPool),
	}
}

// Enqueue creates a job in pending status and dispatches a goroutine to run it.
// Returns the persisted job (with ID set) without waiting for completion.
func (a *BacktestApp) Enqueue(ctx context.Context, job *domain.BacktestJob) (*domain.BacktestJob, error) {
	job.Status = domain.BacktestStatusPending
	if err := a.backtest.CreateJob(ctx, job); err != nil {
		return nil, err
	}
	logger.L().Info("backtest enqueued",
		zap.Int64("id", job.ID),
		zap.Int64("strategy_id", job.StrategyID),
		zap.Float64("initial_capital", job.InitialCapital),
	)
	go a.runDetached(job.ID)
	return job, nil
}

func (a *BacktestApp) runDetached(jobID int64) {
	a.workerSem <- struct{}{}
	defer func() { <-a.workerSem }()
	ctx := context.Background()
	if err := a.Run(ctx, jobID); err != nil {
		logger.L().Error("backtest run failed", zap.Int64("id", jobID), zap.Error(err))
	}
}

// Run executes the backtest synchronously and persists results.
func (a *BacktestApp) Run(ctx context.Context, jobID int64) error {
	job, err := a.backtest.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	now := time.Now()
	job.Status = domain.BacktestStatusRunning
	job.StartedAt = &now
	if err := a.backtest.UpdateJob(ctx, job); err != nil {
		return err
	}

	if runErr := a.execute(ctx, job); runErr != nil {
		fin := time.Now()
		job.Status = domain.BacktestStatusFailed
		job.ErrorMsg = runErr.Error()
		job.FinishedAt = &fin
		_ = a.backtest.UpdateJob(ctx, job)
		return runErr
	}

	fin := time.Now()
	job.Status = domain.BacktestStatusSucceed
	job.FinishedAt = &fin
	return a.backtest.UpdateJob(ctx, job)
}

func (a *BacktestApp) execute(ctx context.Context, job *domain.BacktestJob) error {
	strategy, err := a.strategy.GetByID(ctx, job.StrategyID)
	if err != nil {
		return err
	}
	var body domain.StrategyBody
	if err := json.Unmarshal(strategy.Body, &body); err != nil {
		return err
	}

	cost, err := a.resolveCost(ctx, job.CostID)
	if err != nil {
		return err
	}

	// Pick the decider by mode. Script strategies compute their own indicators
	// from bar history, so they need no indicator specs; config strategies
	// compile rules and pre-compute declared indicators.
	var (
		decider engine.Decider
		specs   []domain.IndicatorSpec
	)
	if body.IsScript() {
		prog, err := script.Compile(body.Code)
		if err != nil {
			return err
		}
		decider = prog
	} else {
		rules, err := compileRules(body.Rules)
		if err != nil {
			return err
		}
		decider = engine.RuleDecider{Rules: rules}
		specs = body.Indicators
	}

	symbols, err := a.loadSymbols(ctx, strategy.Universe, strategy.Period, specs, job.StartDate, job.EndDate)
	if err != nil {
		return err
	}
	if len(symbols) == 0 {
		return errors.New("no bars available for the requested universe & date range")
	}

	fillAt := body.Execution.FillAt
	if fillAt == "" {
		fillAt = engine.FillAtNextOpen
	}

	out, err := engine.Run(engine.RunInput{
		InitialCapital: job.InitialCapital,
		Cost:           cost,
		FillAt:         fillAt,
		Decider:        decider,
		Symbols:        symbols,
	})
	if err != nil {
		return err
	}

	if err := a.backtest.SaveTrades(ctx, job.ID, out.Trades); err != nil {
		return err
	}
	if err := a.backtest.SaveEquity(ctx, job.ID, out.Equity); err != nil {
		return err
	}
	if out.Result != nil {
		out.Result.JobID = job.ID
		out.Result.RuleStats = buildRuleStats(out.Trades)
		if err := a.backtest.SaveResult(ctx, out.Result); err != nil {
			return err
		}
	}
	logger.L().Info("backtest finished",
		zap.Int64("id", job.ID),
		zap.Int("trades", len(out.Trades)),
		zap.Float64("total_return", out.Result.TotalReturn),
	)
	return nil
}

func (a *BacktestApp) resolveCost(ctx context.Context, id int64) (*domain.Cost, error) {
	if id != 0 {
		return a.cost.GetByID(ctx, id)
	}
	return a.cost.GetDefault(ctx)
}

func compileRules(rules []domain.RuleSpec) ([]engine.CompiledRule, error) {
	out := make([]engine.CompiledRule, 0, len(rules))
	for _, r := range rules {
		node, err := expr.Parse(r.When)
		if err != nil {
			return nil, err
		}
		if err := expr.ValidateCondition(node); err != nil {
			return nil, err
		}
		out = append(out, engine.CompiledRule{Name: r.Name, When: node, Action: r.Then})
	}
	return out, nil
}

func (a *BacktestApp) loadSymbols(
	ctx context.Context,
	universe []string,
	period domain.BarPeriod,
	specs []domain.IndicatorSpec,
	start, end time.Time,
) ([]*engine.SymbolData, error) {
	out := make([]*engine.SymbolData, 0, len(universe))
	for _, code := range universe {
		bars, err := a.bars.Range(ctx, code, period, start, end)
		if err != nil {
			return nil, err
		}
		if len(bars) == 0 {
			logger.L().Warn("symbol has no bars in range", zap.String("code", code))
			continue
		}
		ind := map[string]indicator.Series{}
		for _, spec := range specs {
			calc, err := indicator.Get(spec.Type)
			if err != nil {
				return nil, err
			}
			series, err := calc.Compute(bars, spec.Params)
			if err != nil {
				return nil, err
			}
			ind[spec.Alias] = series
		}
		out = append(out, &engine.SymbolData{Symbol: code, Bars: bars, Indicators: ind})
	}
	return out, nil
}

// buildRuleStats aggregates trigger counts and realized PnL by rule name.
func buildRuleStats(trades []*domain.Trade) json.RawMessage {
	type stat struct {
		TriggerCount int     `json:"trigger_count"`
		WinCount     int     `json:"win_count"`
		TotalPnL     float64 `json:"total_pnl"`
	}
	agg := map[string]*stat{}
	for _, t := range trades {
		s, ok := agg[t.TriggeredRule]
		if !ok {
			s = &stat{}
			agg[t.TriggeredRule] = s
		}
		s.TriggerCount++
		if t.Side == domain.TradeSideSell {
			s.TotalPnL += t.RealizedPnL
			if t.RealizedPnL > 0 {
				s.WinCount++
			}
		}
	}
	raw, _ := json.Marshal(agg)
	return raw
}
