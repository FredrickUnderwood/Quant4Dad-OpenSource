package application

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type etfWorkflowSource struct{ etfSyncFixture }

func (c *etfWorkflowSource) FetchInstrumentBars(_ context.Context, it *domain.Instrument, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	var bars []*domain.Bar
	for i, price := range []float64{5, 5.2, 4.992, 4.992, 5.2416} {
		day := time.Date(2026, 9, 14+i, 0, 0, 0, 0, time.UTC)
		if !day.Before(start) && !day.After(end) {
			bars = append(bars, &domain.Bar{Code: it.Code, Period: period, Date: day, Open: price, High: price, Low: price, Close: price, Volume: 10000, AdjFactor: 1})
		}
	}
	return bars, nil
}

// Exercise persisted ETF data through both Agent profiles and both backtest
// workers. The hand ledger is independent of the engine: buy 100 at 5.2,
// sell at 4.992, pay 1 commission each way and no stamp duty => -22.8.
func TestETFIngestionResearchAndStrategyWorkflow(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(directory, "etf.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	bars := repository.NewGormBarRepository(db)
	market := service.NewInstrumentService(repository.NewInstrumentRepository(db), bars)
	tasks := service.NewDataSyncService(repository.NewDataSyncRepository(db))
	source := &etfWorkflowSource{etfSyncFixture{items: []*domain.Instrument{
		{Code: "sh.510300", Name: "沪深300ETF", AssetType: domain.AssetETF, Status: "active"},
		{Code: "sz.159915", Name: "创业板ETF", AssetType: domain.AssetETF, Status: "active"},
		{Code: "sh.600000", Name: "stock fixture", AssetType: domain.AssetStock, Status: "active"},
	}}}
	syncApp := NewDataSyncApp(config.DatasourceConfig{Concurrency: 1}, tasks, market, bars, source)
	start := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	task := runETFTask(t, syncApp, tasks, &domain.DataSyncTask{Mode: domain.DataSyncModeIncremental, Period: domain.Bar1d, StartDate: start, EndDate: start.AddDate(0, 0, 4)})
	if task.Status != domain.DataSyncStatusSucceed || task.Total != 3 || task.Done != 3 {
		t.Fatalf("sync: %+v", task)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(directory, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	strategies := service.NewStrategyService(repository.NewStrategyRepository(db), files)
	catalog := NewAgentP0Catalog(market, service.NewIndicatorService(), strategies, nil, nil, nil, nil, nil, service.NewAgentKlineFileService(files))
	ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "etf-test"}, domain.AgentApprovalReceipt{})
	invoke := func(profile, name string, input, target any) {
		t.Helper()
		raw, err := sonic.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		toolCtx := service.WithAgentExecution(ctx, domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "etf-test", ToolName: name}, domain.AgentApprovalReceipt{})
		out, err := catalog.Invoke(toolCtx, profile, name, raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var envelope struct {
			Data sonic.NoCopyRawMessage `json:"data"`
		}
		if err := sonic.Unmarshal(out, &envelope); err != nil {
			t.Fatal(err)
		}
		if err := sonic.Unmarshal(envelope.Data, target); err != nil {
			t.Fatal(err)
		}
	}
	for _, profile := range []string{"research", "strategy_lab"} {
		var listed struct {
			Total int
			Items []*domain.Instrument
		}
		invoke(profile, "list_instruments", map[string]any{"asset_type": "etf"}, &listed)
		if listed.Total != 2 || len(listed.Items) != 2 {
			t.Fatalf("ETF filter: %+v", listed)
		}
		for _, item := range listed.Items {
			if item.AssetType != domain.AssetETF {
				t.Fatal("stock escaped ETF filter")
			}
			var descriptor struct {
				FileID string `json:"file_id"`
				Count  int
			}
			invoke(profile, "query_kline", map[string]any{"code": item.Code, "period": "1d", "start": "2026-09-15", "end": "2026-09-18", "limit": 500}, &descriptor)
			if descriptor.Count != 4 {
				t.Fatalf("query count: %d", descriptor.Count)
			}
			for _, tc := range []struct {
				comparison string
				threshold  float64
				dates      []string
			}{
				{"gte", 4, []string{"2026-09-15", "2026-09-18"}}, {"gt", 4, []string{"2026-09-18"}},
				{"lte", -4, []string{"2026-09-16"}}, {"lt", -4, nil},
			} {
				var result service.KlineAnalysis
				invoke(profile, "analyze_kline", map[string]any{"file_id": descriptor.FileID, "threshold_pct": tc.threshold, "comparison": tc.comparison}, &result)
				if result.Code != item.Code || result.EligibleCount != 4 || result.ExcludedCount != 0 || result.MatchedCount != len(tc.dates) || len(result.Chart.Rows) != 4 {
					t.Fatalf("%s: %+v", tc.comparison, result)
				}
				for i, date := range tc.dates {
					if result.Matches.Rows[i][0] != date {
						t.Fatalf("wrong hit: %+v", result.Matches.Rows)
					}
				}
			}
		}
	}
	btRepo := repository.NewBacktestRepository(db)
	btService := service.NewBacktestService(btRepo, repository.NewTradeRepository(db), repository.NewEquityRepository(db))
	costService := service.NewCostService(repository.NewCostRepository(db))
	cost := domain.Cost{Name: "ETF fixture", CommissionRate: 0.0003, MinCommission: 1, StampDutyRate: 0}
	if err := costService.Create(ctx, &cost); err != nil {
		t.Fatal(err)
	}
	app := NewBacktestApp(btService, strategies, costService, bars, 1)
	for _, mode := range []string{"config", "script"} {
		var body domain.StrategyBody
		if err := sonic.UnmarshalString(`{"indicators":[],"rules":[{"name":"sell","when":{"all":[{"has_position":null},{"gte":[{"days_held":null},1]}]},"then":{"action":"sell","size":"all"}},{"name":"buy","when":{"not":{"has_position":null}},"then":{"action":"buy","size":{"shares":100}}}],"execution":{"fill_at":"next_open"}}`, &body); err != nil {
			t.Fatal(err)
		}
		if mode == "script" {
			body = domain.StrategyBody{Mode: "script", Lang: "starlark", Execution: domain.ExecutionSpec{FillAt: "next_open"}, Code: "def on_bar(ctx):\n    if ctx.has_position and ctx.days_held >= 1:\n        return sell(\"all\")\n    if not ctx.has_position:\n        return buy(shares=100)\n    return None\n"}
		}
		input := service.StrategyInput{Name: "ETF " + mode, Universe: []string{"sh.510300"}, Period: domain.Bar1d, Body: body}
		var validation struct{ Valid bool }
		invoke("strategy_lab", "validate_strategy", map[string]any{"strategy": input}, &validation)
		if !validation.Valid {
			t.Fatal("ETF strategy validation failed")
		}
		strategy, err := strategies.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		for _, agent := range []bool{false, true} {
			job := &domain.BacktestJob{StrategyID: strategy.ID, CostID: cost.ID, InitialCapital: 10000, StartDate: start, EndDate: start.AddDate(0, 0, 3), Status: domain.BacktestStatusPending, AgentRun: agent}
			if agent {
				job.AgentSnapshot, err = sonic.MarshalString(domain.AgentBacktestSnapshot{Strategy: *strategy, Cost: cost})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := btRepo.CreateJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			if agent {
				claimed, err := btService.ClaimAgent(ctx)
				if err != nil || claimed == nil {
					t.Fatalf("claim: %v", err)
				}
				err = app.runAgentJob(ctx, claimed)
				if err != nil {
					t.Fatal(err)
				}
			} else if err := app.Run(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			var trades []domain.Trade
			var result domain.BacktestResult
			if err := db.Where("job_id = ?", job.ID).Order("id").Find(&trades).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&result, "job_id = ?", job.ID).Error; err != nil {
				t.Fatal(err)
			}
			if len(trades) != 2 || trades[0].Code != "sh.510300" || trades[1].StampDuty != 0 || math.Abs(trades[1].RealizedPnL+22.8) > 1e-9 || math.Abs(result.TotalReturn+0.00228) > 1e-9 {
				t.Fatalf("%s agent=%v ledger: trades=%+v result=%+v", mode, agent, trades, result)
			}
		}
	}
}
