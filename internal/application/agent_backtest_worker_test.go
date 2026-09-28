package application

import (
	"context"
	"errors"
	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentBacktestUsesFixedSnapshotAndBoundsCompute(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "backtest.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := db.Create(&domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Close: float64(100 + i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := domain.AgentBacktestSnapshot{Strategy: domain.Strategy{Name: "fixed snapshot", Universe: domain.StringSlice{"sh.600519"}, Period: domain.Bar1d, Body: []byte(`{"indicators":[{"type":"MA","alias":"average","params":{"period":2}}],"rules":[],"execution":{"fill_at":"close"}}`)}}
	raw, err := sonic.MarshalString(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewBacktestRepository(db)
	svc := service.NewBacktestService(repo, repository.NewTradeRepository(db), repository.NewEquityRepository(db))
	app := NewBacktestApp(svc, service.NewStrategyService(nil), nil, repository.NewGormBarRepository(db), 1)
	job := &domain.BacktestJob{Status: domain.BacktestStatusPending, AgentRun: true, AgentSnapshot: raw, InitialCapital: 10000, StartDate: start, EndDate: start.AddDate(0, 0, 9)}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimAgent(ctx)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	// There is deliberately no Strategy/Cost service repository to reread.
	if err := app.runAgentJob(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	var result domain.BacktestResult
	if err := db.First(&result, "job_id = ?", job.ID).Error; err != nil || result.TotalReturn != 0 {
		t.Fatal("snapshot outcome", err)
	}
	for i := 10; i < 501; i++ {
		if err := db.Create(&domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Close: 100}).Error; err != nil {
			t.Fatal(err)
		}
	}
	job.ID = 0
	job.Status = domain.BacktestStatusPending
	job.EndDate = start.AddDate(0, 0, 500)
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err = svc.ClaimAgent(ctx)
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if err := app.runAgentJob(ctx, claimed); err == nil || err.Error() != "agent_backtest_bar_limit" {
		t.Fatal("bar cap not enforced", err)
	}
	aborted, cancel := context.WithCancel(ctx)
	cancel()
	if err := app.runAgentJob(aborted, claimed); err == nil {
		t.Fatal("cancelled compute continued")
	}
}

func TestAgentBacktestPersistsNonzeroGoldenRoundTrip(t *testing.T) {
	t.Run("config", func(t *testing.T) { assertAgentBacktestGoldenRoundTrip(t, false) })
	t.Run("script", func(t *testing.T) { assertAgentBacktestGoldenRoundTrip(t, true) })
}

func assertAgentBacktestGoldenRoundTrip(t *testing.T, scriptMode bool) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "golden.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	for i, close := range []float64{10, 12, 9, 11} {
		open := 10.0
		if i == 3 {
			open = 11
		}
		if err := db.Create(&domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Open: open, High: 12, Low: 9, Close: close}).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := domain.AgentBacktestSnapshot{
		Cost: domain.Cost{CommissionRate: 0.001, MinCommission: 1, StampDutyRate: 0.002, SlippageBps: 100},
		Strategy: domain.Strategy{Name: "nonzero golden", Universe: domain.StringSlice{"sh.600519"}, Period: domain.Bar1d,
			Body: []byte(`{"indicators":[],"rules":[{"name":"sell","when":{"all":[{"has_position":null},{"gte":[{"days_held":null},1]}]},"then":{"action":"sell","size":"all"}},{"name":"buy","when":{"not":{"has_position":null}},"then":{"action":"buy","size":{"shares":100}}}],"execution":{"fill_at":"next_open"}}`)},
	}
	if scriptMode {
		body, err := sonic.Marshal(domain.StrategyBody{Mode: "script", Lang: "starlark", Code: `def on_bar(ctx):
    if ctx.has_position and ctx.days_held >= 1:
        return sell("all")
    if not ctx.has_position:
        return buy(shares=100)
    return None
`, Execution: domain.ExecutionSpec{FillAt: "next_open"}})
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Strategy.Body = body
	}
	raw, err := sonic.MarshalString(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewBacktestRepository(db)
	svc := service.NewBacktestService(repo, repository.NewTradeRepository(db), repository.NewEquityRepository(db))
	app := NewBacktestApp(svc, service.NewStrategyService(nil), nil, repository.NewGormBarRepository(db), 1)
	job := &domain.BacktestJob{Status: domain.BacktestStatusPending, AgentRun: true, AgentSnapshot: raw, InitialCapital: 10000, StartDate: start, EndDate: start.AddDate(0, 0, 3)}
	if err := repo.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.ClaimAgent(ctx)
	if err != nil || claimed == nil {
		t.Fatal("claim failed", err)
	}
	if err := app.runAgentJob(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	var trades []domain.Trade
	var equity []domain.EquityPoint
	var result domain.BacktestResult
	if err := db.Where("job_id = ?", job.ID).Order("id").Find(&trades).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("job_id = ?", job.ID).Order("date").Find(&equity).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&result, "job_id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(trades) != 2 || len(equity) != 4 || result.TradeCount != 2 {
		t.Fatalf("incomplete persisted results: %+v", result)
	}
	if math.Abs(trades[1].RealizedPnL-74.723) > 1e-8 || math.Abs(equity[3].TotalValue-10074.723) > 1e-8 ||
		math.Abs(result.TotalReturn-0.0074723) > 1e-8 || math.Abs(result.MaxDrawdown-300.0/10188.99) > 1e-8 {
		t.Fatalf("persisted report disagrees with hand ledger: trade=%+v equity=%+v result=%+v", trades[1], equity[3], result)
	}
}

func TestAgentBacktestRejectsPreviouslySavedInvalidConditionBeforeCompute(t *testing.T) {
	snapshot := domain.AgentBacktestSnapshot{Strategy: domain.Strategy{Name: "legacy-invalid", Universe: domain.StringSlice{"sh.600519"}, Period: domain.Bar1d,
		Body: []byte(`{"indicators":[],"rules":[{"name":"exit","when":{"all":[{"has_position":null},{"days_held":null}]},"then":{"action":"sell","size":"all"}}],"execution":{"fill_at":"next_open"}}`)}}
	raw, err := sonic.MarshalString(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	app := NewBacktestApp(nil, service.NewStrategyService(nil), nil, nil, 1)
	if err := app.runAgentJob(context.Background(), &domain.BacktestJob{AgentRun: true, AgentSnapshot: raw}); !errors.Is(err, service.ErrToolInput) {
		t.Fatal("legacy snapshot escaped validation", err)
	}
}
