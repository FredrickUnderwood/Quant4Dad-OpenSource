package repository

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
)

func coverageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(config.StorageConfig{
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
	if err := db.AutoMigrate(&domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCoverageDateScan(t *testing.T) {
	when := time.Date(2026, 9, 1, 9, 30, 45, 123456789, time.FixedZone("CST", 8*3600))
	var date coverageDate
	if err := date.Scan(when); err != nil || date.Time != when {
		t.Fatalf("native MySQL time must be preserved: got %v err=%v", date.Time, err)
	}
	// Exercise the actual SQLite driver's supported formats so its timestamp
	// storage contract, including fractional seconds, remains compatible.
	for _, format := range sqlite3.SQLiteTimestampFormats {
		value := when.Format(format)
		expected, err := time.Parse(format, value)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []any{value, []byte(value)} {
			if err := date.Scan(input); err != nil || !date.Time.Equal(expected) {
				t.Errorf("scan %q (%T): got %v want %v err=%v", value, input, date.Time, expected, err)
			}
		}
	}
	if err := date.Scan("2026-09-01T01:30:45.123456789Z"); err != nil || !date.Time.Equal(when) {
		t.Fatalf("RFC3339 UTC: got %v err=%v", date.Time, err)
	}
	for _, input := range []any{nil, int64(123), "", "invalid", "2026-02-30"} {
		date.Time = when
		if err := date.Scan(input); err == nil || !date.Time.IsZero() {
			t.Errorf("invalid date %v must fail and clear previous value: got %v err=%v", input, date.Time, err)
		}
	}
}

func TestDataCoverageAggregateByCode(t *testing.T) {
	db := coverageTestDB(t)
	repo := NewDataCoverageRepository(db)
	ctx := context.Background()
	if rows, err := repo.AggregateByCode(ctx, domain.Bar1d); err != nil || len(rows) != 0 {
		t.Fatalf("empty table: rows=%v err=%v", rows, err)
	}
	start := time.Date(2026, 8, 1, 9, 30, 45, 123456789, time.FixedZone("CST", 8*3600))
	for i, period := range []domain.BarPeriod{domain.Bar1d, domain.Bar1w, domain.Bar1mo} {
		first := start.AddDate(0, 0, i)
		last := first.AddDate(0, 0, 10)
		bars := []domain.Bar{
			{Code: "stock-a", Period: period, Date: last},
			{Code: "stock-b", Period: period, Date: first},
			{Code: "stock-a", Period: period, Date: first},
		}
		if err := db.Create(&bars).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i, period := range []domain.BarPeriod{domain.Bar1d, domain.Bar1w, domain.Bar1mo} {
		t.Run(string(period), func(t *testing.T) {
			rows, err := repo.AggregateByCode(ctx, period)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("want 2 code groups, got %+v", rows)
			}
			first := start.AddDate(0, 0, i)
			want := map[string]CodeAggregate{
				"stock-a": {BarCount: 2, MinDate: first, MaxDate: first.AddDate(0, 0, 10)},
				"stock-b": {BarCount: 1, MinDate: first, MaxDate: first},
			}
			for _, row := range rows {
				expected, ok := want[row.Code]
				if !ok || row.BarCount != expected.BarCount || !row.MinDate.Equal(expected.MinDate) || !row.MaxDate.Equal(expected.MaxDate) {
					t.Errorf("unexpected aggregate: %+v; want %+v", row, expected)
				}
				delete(want, row.Code)
			}
		})
	}
	if rows, err := repo.AggregateByCode(ctx, domain.BarPeriod("unused")); err != nil || len(rows) != 0 {
		t.Fatalf("empty period: rows=%v err=%v", rows, err)
	}
}

func TestDataCoverageAggregateInvalidDate(t *testing.T) {
	for _, date := range []string{"0000-invalid", "zz-invalid"} {
		t.Run(date, func(t *testing.T) {
			db := coverageTestDB(t)
			for _, code := range []string{"stock-a", "stock-b"} {
				for _, value := range []string{"2026-09-01", date} {
					if err := db.Exec("INSERT INTO bar (code, period, date) VALUES (?, ?, ?)", code, domain.Bar1d, value).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			rows, err := NewDataCoverageRepository(db).AggregateByCode(context.Background(), domain.Bar1d)
			if err == nil || rows != nil {
				t.Fatalf("invalid dates must fail without partial results: rows=%v err=%v", rows, err)
			}
			if strings.Count(err.Error(), "Scan error") != 1 {
				t.Fatalf("expected one scan error, got %v", err)
			}
		})
	}
}
