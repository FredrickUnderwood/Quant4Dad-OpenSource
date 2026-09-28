package application

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

type etfSyncFixture struct {
	items       []*domain.Instrument
	listErr     error
	fetchErr    error
	fetched     []*domain.Instrument
	starts      []time.Time
	progressErr bool
}

func (*etfSyncFixture) Name() string { return "etf-fixture" }
func (c *etfSyncFixture) ListInstruments(context.Context) ([]*domain.Instrument, error) {
	return c.items, c.listErr
}
func (*etfSyncFixture) FetchBars(context.Context, string, domain.BarPeriod, time.Time, time.Time) ([]*domain.Bar, error) {
	return nil, errors.New("untyped route must not be used")
}
func (c *etfSyncFixture) FetchInstrumentBars(_ context.Context, it *domain.Instrument, period domain.BarPeriod, start, end time.Time) ([]*domain.Bar, error) {
	c.fetched = append(c.fetched, it)
	c.starts = append(c.starts, start)
	if c.fetchErr != nil {
		return nil, c.fetchErr
	}
	return []*domain.Bar{{Code: it.Code, Period: period, Date: end, Open: 1, High: 2, Low: 1, Close: 2, AdjFactor: 1}}, nil
}

func etfSyncApp(t *testing.T, client *etfSyncFixture) (*DataSyncApp, *service.DataSyncService) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "sync.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.Instrument{}, &domain.Bar{}, &domain.DataSyncTask{}, &domain.DataSyncFailure{}); err != nil {
		t.Fatal(err)
	}
	if client.progressErr {
		if err := db.Callback().Update().Before("gorm:update").Register("test:fail_sync_progress", func(tx *gorm.DB) {
			if tx.Statement.Table == "data_sync_task" {
				if _, progress := tx.Statement.Dest.(map[string]any); progress {
					tx.AddError(errors.New("simulated progress persistence failure"))
				}
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	bars := repository.NewGormBarRepository(db)
	tasks := service.NewDataSyncService(repository.NewDataSyncRepository(db))
	inst := service.NewInstrumentService(repository.NewInstrumentRepository(db), bars)
	return NewDataSyncApp(config.DatasourceConfig{Concurrency: 1, InitialYears: 10}, tasks, inst, bars, client), tasks
}

func runETFTask(t *testing.T, app *DataSyncApp, tasks *service.DataSyncService, task *domain.DataSyncTask) *domain.DataSyncTask {
	t.Helper()
	if err := tasks.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := app.run(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	out, err := tasks.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDataSyncETFCatalogPersistenceAndIncrementalDailyRouting(t *testing.T) {
	c := &etfSyncFixture{items: []*domain.Instrument{
		{Code: "sh.510300", Name: "沪深300ETF", Status: "active", AssetType: domain.AssetETF, IndexCode: "000300.SH"},
		{Code: "sz.159999", Name: "待上市ETF", Status: "pending", AssetType: domain.AssetETF},
		{Code: "sh.500001", Name: "已退市ETF", Status: "delisted", AssetType: domain.AssetETF},
	}}
	app, tasks := etfSyncApp(t, c)
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	out := runETFTask(t, app, tasks, &domain.DataSyncTask{Mode: domain.DataSyncModeIncremental, Period: domain.Bar1d, StartDate: day.AddDate(-10, 0, 0), EndDate: day})
	if out.Status != domain.DataSyncStatusSucceed || out.Total != 1 || out.Done != 1 || len(c.fetched) != 1 || c.fetched[0].AssetType != domain.AssetETF {
		t.Fatalf("daily task failed: %+v / %+v", out, c.fetched)
	}
	stored, err := app.instruments.GetByCode(context.Background(), "sz.159999")
	if err != nil || stored.ListedDate != nil || stored.Status != "pending" {
		t.Fatalf("pending catalog row missing: %+v %v", stored, err)
	}
	// An inclusive single-day incremental update must not be skipped. Running
	// it again must be idempotent and must not fetch the twenty-year history.
	next := day.AddDate(0, 0, 1)
	out = runETFTask(t, app, tasks, &domain.DataSyncTask{Mode: domain.DataSyncModeIncremental, Period: domain.Bar1d, StartDate: day.AddDate(-10, 0, 0), EndDate: next})
	if out.Status != domain.DataSyncStatusSucceed || len(c.starts) != 2 || !c.starts[1].Equal(next) {
		t.Fatalf("incremental range incorrect: %+v", c.starts)
	}
	_ = runETFTask(t, app, tasks, &domain.DataSyncTask{Mode: domain.DataSyncModeIncremental, Period: domain.Bar1d, StartDate: day, EndDate: next})
	if len(c.starts) != 2 {
		t.Fatal("already synchronized date fetched again")
	}
}

func TestDataSyncCatalogAndBarFailuresAreVisible(t *testing.T) {
	for _, failureCase := range []string{"catalog", "bars", "progress"} {
		t.Run(failureCase, func(t *testing.T) {
			c := &etfSyncFixture{items: []*domain.Instrument{{Code: "sh.510300", Name: "ETF", Status: "active", AssetType: domain.AssetETF}}}
			if failureCase == "catalog" {
				c.listErr = errors.New("etf_basic permission denied")
			} else {
				c.fetchErr = errors.New("fund_daily permission denied")
			}
			c.progressErr = failureCase == "progress"
			app, tasks := etfSyncApp(t, c)
			day := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
			out := runETFTask(t, app, tasks, &domain.DataSyncTask{Mode: domain.DataSyncModeFull, Period: domain.Bar1d, StartDate: day, EndDate: day})
			if out.Status != domain.DataSyncStatusFailed || out.ErrorMsg == "" || out.FinishedAt == nil {
				t.Fatalf("failure hidden: %+v", out)
			}
			if failureCase != "catalog" && out.Failed != 1 {
				t.Fatalf("provider failure lost when progress persistence failed: %+v", out)
			}
			failures, err := tasks.ListFailures(context.Background(), out.ID, 10)
			if err != nil || len(failures) != 1 {
				t.Fatalf("failure detail missing: %+v %v", failures, err)
			}
		})
	}
}
