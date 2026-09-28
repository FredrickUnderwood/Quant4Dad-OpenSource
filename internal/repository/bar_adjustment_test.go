package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

func TestBarFactorProvenancePersistsAndBackfillDoesNotAdjustOHLC(t *testing.T) {
	db := modelRepositoryDB(t)
	if err := db.AutoMigrate(&domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	csv, err := NewCSVBarRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	day := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)
	for name, repo := range map[string]BarRepository{"sqlite": NewGormBarRepository(db), "csv": csv} {
		t.Run(name, func(t *testing.T) {
			if name == "csv" {
				if err := os.WriteFile(filepath.Join(dir, "sh.563020.1d.csv"), []byte("date,open,high,low,close,volume,amount,adj_factor\n2025-01-03,10,11,9,10,100,1000,1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := repo.Upsert(ctx, []*domain.Bar{{Code: "sh.563020", Period: domain.Bar1d, Date: day, Open: 10, High: 11, Low: 9, Close: 10, Volume: 100, Amount: 1000, AdjFactor: 1}}); err != nil {
					t.Fatal(err)
				}
			}
			bars, err := repo.(BoundedBarRepository).RangeLatest(ctx, "sh.563020", domain.Bar1d, day, day, 1)
			if err != nil || len(bars) != 1 || bars[0].AdjSource != "" {
				t.Fatal("legacy factors gained false provenance", err, bars)
			}
			bars[0].AdjSource, bars[0].AdjFactor = domain.AdjFactorTushareFund, 1.1099
			if err := repo.Upsert(ctx, bars); err != nil {
				t.Fatal(err)
			}
			stored, err := repo.Range(ctx, "sh.563020", domain.Bar1d, day, day)
			if err != nil || len(stored) != 1 || stored[0].AdjSource != domain.AdjFactorTushareFund || stored[0].AdjFactor != 1.1099 || stored[0].Close != 10 || stored[0].High != 11 || stored[0].Volume != 100 {
				t.Fatal(err, stored)
			}
			bounded, err := repo.(BoundedBarRepository).RangeLatest(ctx, "sh.563020", domain.Bar1d, day, day, 1)
			if err != nil || bounded[0].AdjSource != domain.AdjFactorTushareFund {
				t.Fatal("bounded read lost provenance", err)
			}
		})
	}
}

func TestBarProvenanceMigrationRequiresApplyAndPreservesLegacyRows(t *testing.T) {
	db := modelRepositoryDB(t)
	if err := Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO bar (code, period, date, close, adj_factor) VALUES (?, ?, ?, ?, ?)", "sh.563020", "1d", time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), 10, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&domain.Bar{}, "adj_source"); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasIndex(&domain.Bar{}, "idx_bar_date") {
		if err := db.Migrator().CreateIndex(&domain.Bar{}, "idx_bar_date"); err != nil {
			t.Fatal(err)
		}
	}
	if ready, err := CheckSchema(db, config.StorageBackendSQLite); err != nil || ready {
		t.Fatal("unmigrated schema admitted", ready, err)
	}
	if compatible, err := CheckReleaseCompatibility(db, config.StorageBackendSQLite); err != nil || !compatible {
		t.Fatal("additive migration rejected", compatible, err)
	}
	if err := Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	var bar domain.Bar
	if err := db.First(&bar).Error; err != nil || bar.Close != 10 || bar.AdjFactor != 1 || bar.AdjSource != "" {
		t.Fatal("migration changed or trusted old prices", bar, err)
	}
}
