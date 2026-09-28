package application

import (
	"context"
	"errors"
	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
	"github.com/quant4dad/internal/indicator"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/script"
	"github.com/quant4dad/internal/service"
	"go.uber.org/zap"
	"sync"
	"time"
)

var (
	errAgentBacktestBarLimit = errors.New("agent_backtest_bar_limit")
	errAgentBacktestNoData   = errors.New("agent_backtest_no_data")
)

// Only fixed classifications enter logs. Provider/SQL errors, the saved DSL,
// worker nonce and market data must never be serialized here.
func agentBacktestErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "interrupted"
	case errors.Is(err, service.ErrToolInput):
		return "invalid_input"
	case errors.Is(err, service.ErrToolUnavailable):
		return "unavailable"
	case errors.Is(err, repository.ErrAgentBacktestLease):
		return "lease_lost"
	case errors.Is(err, errAgentBacktestBarLimit):
		return "bar_limit"
	case errors.Is(err, errAgentBacktestNoData):
		return "no_data"
	default:
		return "operation_failed"
	}
}

// Agent jobs use durable admission and database CAS claims. No goroutine waits
// for a worker slot, and a dead worker's job fails without restarting compute.
func (a *BacktestApp) StartAgentWorker() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(done)
		defer workers.Wait()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			batch, stop := context.WithTimeout(ctx, 3*time.Second)
			if err := a.backtest.FailExpiredAgent(batch); err != nil {
				logger.L().Info("agent backtest maintenance failed", zap.String("stage", "expire"), zap.String("error_code", agentBacktestErrorCode(err)))
			}
			select {
			case a.workerSem <- struct{}{}:
				job, err := a.backtest.ClaimAgent(batch)
				if err != nil || job == nil {
					if err != nil {
						logger.L().Info("agent backtest maintenance failed", zap.String("stage", "claim"), zap.String("error_code", agentBacktestErrorCode(err)))
					}
					<-a.workerSem
				} else {
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer func() { <-a.workerSem }()
						deadline, release := context.WithDeadline(ctx, *job.AgentLeaseUntil)
						defer release()
						err := a.runAgentJob(deadline, job)
						if err != nil {
							finish, close := context.WithTimeout(context.Background(), 2*time.Second)
							defer close()
							code := "agent_backtest_failed"
							if deadline.Err() != nil {
								code = "agent_backtest_interrupted"
							}
							if err := a.backtest.FailAgent(finish, job, code); err != nil {
								logger.L().Info("agent backtest failure persistence failed", zap.Int64("job_id", job.ID), zap.String("stage", "persist_failure"), zap.String("error_code", agentBacktestErrorCode(err)))
							} else {
								logger.L().Info("agent backtest failure persisted", zap.Int64("job_id", job.ID), zap.String("outcome", code))
							}
						} else {
							logger.L().Info("agent backtest completed", zap.Int64("job_id", job.ID))
						}
					}()
				}
			default:
			}
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (a *BacktestApp) runAgentJob(ctx context.Context, job *domain.BacktestJob) (resultErr error) {
	stage := "snapshot"
	started := time.Now()
	defer func() {
		if resultErr != nil {
			logger.L().Info("agent backtest execution failed", zap.Int64("job_id", job.ID), zap.Int64("strategy_id", job.StrategyID),
				zap.String("stage", stage), zap.String("error_code", agentBacktestErrorCode(resultErr)), zap.Duration("elapsed", time.Since(started)))
		}
	}()
	var snapshot domain.AgentBacktestSnapshot
	if !job.AgentRun || len(job.AgentSnapshot) > 65536 || sonic.UnmarshalString(job.AgentSnapshot, &snapshot) != nil {
		return service.ErrToolInput
	}
	st := snapshot.Strategy
	stage = "validate_strategy"
	var body domain.StrategyBody
	if sonic.Unmarshal(st.Body, &body) != nil || a.strategy.ValidateAgentContext(ctx, service.StrategyInput{Name: st.Name, Description: st.Description, Universe: st.Universe, Period: st.Period, Body: body}) != nil {
		return service.ErrToolInput
	}
	stage = "bar_repository"
	repo, ok := a.bars.(repository.BoundedBarRepository)
	if !ok {
		return service.ErrToolUnavailable
	}
	var decider engine.Decider
	if body.IsScript() {
		stage = "compile_script"
		program, err := script.CompileContext(ctx, body.Code)
		if err != nil {
			return service.ErrToolInput
		}
		decider = program
	} else {
		stage = "compile_rules"
		rules, err := compileRules(body.Rules)
		if err != nil {
			return service.ErrToolInput
		}
		decider = engine.RuleDecider{Rules: rules}
	}
	symbols := make([]*engine.SymbolData, 0, len(st.Universe))
	for _, code := range st.Universe {
		stage = "load_bars"
		if err := ctx.Err(); err != nil {
			return err
		}
		bars, err := repo.RangeLatest(ctx, code, st.Period, job.StartDate, job.EndDate, 501)
		if err != nil {
			return err
		}
		if len(bars) > 500 {
			return errAgentBacktestBarLimit
		}
		if len(bars) == 0 {
			continue
		}
		series := map[string]indicator.Series{}
		stage = "compute_indicators"
		for _, spec := range body.Indicators {
			if err := ctx.Err(); err != nil {
				return err
			}
			calc, err := indicator.Get(spec.Type)
			if err != nil {
				return err
			}
			values, err := calc.Compute(bars, spec.Params)
			if err != nil {
				return err
			}
			series[spec.Alias] = values
		}
		symbols = append(symbols, &engine.SymbolData{Symbol: code, Bars: bars, Indicators: series})
	}
	if len(symbols) == 0 {
		stage = "load_bars"
		return errAgentBacktestNoData
	}
	fill := body.Execution.FillAt
	if fill == "" {
		fill = engine.FillAtNextOpen
	}
	stage = "engine"
	out, err := engine.RunContext(ctx, engine.RunInput{InitialCapital: job.InitialCapital, Cost: &snapshot.Cost, FillAt: fill, Decider: decider, Symbols: symbols})
	if err != nil {
		return err
	}
	out.Result.RuleStats = buildRuleStats(out.Trades)
	stage = "persist_result"
	return a.backtest.CompleteAgent(ctx, job, out.Result, out.Trades, out.Equity)
}
