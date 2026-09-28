package application

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func TestCoverageScannerPreservesSummaryOnFailure(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{
		Backend: config.StorageBackendSQLite,
		SQLite:  config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "coverage.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&domain.Bar{}, &domain.Instrument{}, &domain.DataCoverageSummary{}); err != nil {
		t.Fatal(err)
	}
	instruments := repository.NewInstrumentRepository(db)
	repo := repository.NewDataCoverageRepository(db)
	scanner := NewCoverageScanner(repo, instruments)
	ctx := context.Background()
	if err := instruments.Upsert(ctx, []*domain.Instrument{
		{Code: "complete", Name: "Complete"},
		{Code: "stale", Name: "Stale"},
		{Code: "empty", Name: "Empty"},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	old := now.AddDate(0, 0, -30)
	if err := repository.NewGormBarRepository(db).Upsert(ctx, []*domain.Bar{
		{Code: "complete", Period: domain.Bar1d, Date: old},
		{Code: "complete", Period: domain.Bar1d, Date: now},
		{Code: "stale", Period: domain.Bar1d, Date: old},
	}); err != nil {
		t.Fatal(err)
	}
	if err := scanner.scanOne(ctx, domain.Bar1d); err != nil {
		t.Fatal(err)
	}
	assertSummary := func(wantError bool) {
		t.Helper()
		summary, err := repo.GetByPeriod(ctx, domain.Bar1d)
		if err != nil || summary == nil {
			t.Fatalf("get summary: summary=%v err=%v", summary, err)
		}
		if summary.TotalInstruments != 3 || summary.CompleteCount != 1 || summary.StaleCount != 1 || summary.EmptyCount != 1 {
			t.Fatalf("unexpected counts: %+v", summary)
		}
		if summary.EarliestBarDate == nil || !summary.EarliestBarDate.Equal(old) || summary.LatestBarDate == nil || !summary.LatestBarDate.Equal(now) {
			t.Fatalf("unexpected date range: %+v", summary)
		}
		if summary.ScanStatus != domain.CoverageScanIdle || summary.ScannedAt == nil || (summary.LastError != "") != wantError {
			t.Fatalf("unexpected scan status: %+v", summary)
		}
	}
	assertSummary(false)
	if err := db.Exec("INSERT INTO bar (code, period, date) VALUES (?, ?, ?)", "complete", domain.Bar1d, "zz-invalid").Error; err != nil {
		t.Fatal(err)
	}
	// A failed period must be reported to the caller while the remaining periods
	// still finish, and its previous successful summary must survive.
	if err := scanner.scanAll(ctx); err == nil {
		t.Fatal("scan failure must not be swallowed after persisting last_error")
	}
	assertSummary(true)
	for _, period := range []domain.BarPeriod{domain.Bar1w, domain.Bar1mo} {
		summary, err := repo.GetByPeriod(ctx, period)
		if err != nil || summary == nil || summary.EmptyCount != 3 || summary.LastError != "" {
			t.Fatalf("remaining period %s must finish: summary=%v err=%v", period, summary, err)
		}
	}
	if err := db.Exec("DELETE FROM bar WHERE date = ?", "zz-invalid").Error; err != nil {
		t.Fatal(err)
	}
	if err := scanner.scanOne(ctx, domain.Bar1d); err != nil {
		t.Fatal(err)
	}
	assertSummary(false)
}
