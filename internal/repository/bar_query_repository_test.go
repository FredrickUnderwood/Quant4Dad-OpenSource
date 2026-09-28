package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"gorm.io/gorm"
)

func TestBoundedBarQueriesSQLiteAndCSV(t *testing.T) {
	ctx := context.Background()
	db := modelRepositoryDB(t)
	if err := db.AutoMigrate(&domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	csv, err := NewCSVBarRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := db.Callback().Query().After("gorm:query").Register("capture-bounded-query", func(tx *gorm.DB) { sqlText = tx.Statement.SQL.String() }); err != nil {
		t.Fatal(err)
	}
	for name, repo := range map[string]BarRepository{"sqlite": NewGormBarRepository(db), "csv": csv} {
		t.Run(name, func(t *testing.T) {
			bars := make([]*domain.Bar, 800)
			start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			for i := range bars {
				bars[i] = &domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Close: float64(i)}
			}
			if err := repo.Upsert(ctx, bars); err != nil {
				t.Fatal(err)
			}
			bounded := repo.(BoundedBarRepository)
			coverage, err := repo.(BarCoverageRepository).Coverage(ctx, "sh.600519", domain.Bar1d)
			if err != nil || coverage.Count != 800 || coverage.FirstDate != bars[0].Date.Format("2006-01-02") || coverage.LastDate != bars[799].Date.Format("2006-01-02") {
				t.Fatalf("coverage: %#v %v", coverage, err)
			}
			got, err := bounded.RangeLatest(ctx, "sh.600519", domain.Bar1d, time.Time{}, time.Time{}, 121)
			if err != nil || len(got) != 121 || got[0].Close != 679 || got[120].Close != 799 {
				t.Fatalf("wrong latest result: %d %v", len(got), err)
			}
			if name == "sqlite" && (!strings.Contains(sqlText, "LIMIT 121") || !strings.Contains(sqlText, "date DESC")) {
				t.Fatal("limit not pushed into SQL", sqlText)
			}
			got, err = bounded.RangeLatest(ctx, "sh.600519", domain.Bar1d, bars[30].Date, bars[50].Date, 10)
			if err != nil || len(got) != 10 || got[0].Close != 41 || got[9].Close != 50 {
				t.Fatal("range/order boundary")
			}
			for _, limit := range []int{-1, 0, 502} {
				if _, err := bounded.RangeLatest(ctx, "sh.600519", domain.Bar1d, time.Time{}, time.Time{}, limit); err == nil {
					t.Fatal("unbounded read")
				}
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := bounded.RangeLatest(cancelled, "sh.600519", domain.Bar1d, time.Time{}, time.Time{}, 1); err == nil {
				t.Fatal("ignored cancellation")
			}
		})
	}
}
func TestMCPQueryStorageDoesNotCreateOrWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.db")
	cfg := config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: path}}
	if _, err := OpenMCPQueryStorage(cfg); err == nil {
		t.Fatal("missing storage accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("created missing DB")
	}
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.Close()
	read, err := OpenMCPQueryStorage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn, _ := read.DB(); conn.Close() })
	if err := read.Exec("CREATE TABLE unwanted (id INTEGER)").Error; err == nil {
		t.Fatal("MCP connection allowed DDL")
	}
	var count int64
	if err := read.Model(&domain.Bar{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := OpenCSVBarQueryRepository(missing); err == nil {
		t.Fatal("created CSV directory")
	}
}
func TestCSVBoundedQueryRejectsUnsortedAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	repo, err := OpenCSVBarQueryRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sh.600519.1d.csv")
	header := "date,open,high,low,close,volume,amount,adj_factor\n"
	for _, text := range []string{"wrong,header\n", header + "2020-01-02,1,1,1,1,1,1,1\n2020-01-01,1,1,1,1,1,1,1\n", header + "bad,1,1,1,1,1,1,1\n", header + "2020-01-01,invalid,1,1,1,1,1,1\n", header + "2020-01-01,NaN,1,1,1,1,1,1\n"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.(BoundedBarRepository).RangeLatest(context.Background(), "sh.600519", domain.Bar1d, time.Time{}, time.Time{}, 10); err == nil {
			t.Fatal("accepted corrupt CSV")
		}
	}
}
