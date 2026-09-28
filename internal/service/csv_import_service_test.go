package service

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
	"github.com/quant4dad/internal/repository"
	"gorm.io/gorm"
)

const importInstrumentCSV = "code,name,asset_type\nsh.600000,Sample stock,stock\n"
const importBarsHeader = "code,period,date,open,high,low,close,volume,amount\n"
const importBarRow = "sh.600000,1d,2026-01-05,10,10,10,10,10000,100000\n"

func importFixture(t *testing.T) (*CSVImportService, *gorm.DB) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "import.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&domain.Instrument{}, &domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	return NewCSVImportService(repository.NewCSVImportRepository(db)), db
}
func importRequest(instruments, bars string) CSVImportRequest {
	r := CSVImportRequest{}
	if instruments != "" {
		r.Instruments = strings.NewReader(instruments)
	}
	if bars != "" {
		r.Bars = strings.NewReader(bars)
	}
	return r
}
func assertImportCounts(t *testing.T, db *gorm.DB, instruments, bars int64) {
	t.Helper()
	for _, tt := range []struct {
		model any
		count int64
	}{{&domain.Instrument{}, instruments}, {&domain.Bar{}, bars}} {
		var got int64
		if err := db.Model(tt.model).Count(&got).Error; err != nil || got != tt.count {
			t.Fatalf("row count for %T = %d, want %d, err %v", tt.model, got, tt.count, err)
		}
	}
}

type importRoundTripDecider struct{}

func (importRoundTripDecider) OnBar(_ *engine.SymbolData, idx int, _ *engine.Portfolio) (*engine.Signal, error) {
	if idx == 0 {
		return &engine.Signal{Side: domain.TradeSideBuy, Size: domain.SizeSpec{Shares: 100}}, nil
	}
	if idx == 2 {
		return &engine.Signal{Side: domain.TradeSideSell, Size: domain.SizeSpec{All: true}}, nil
	}
	return nil, nil
}

func TestCSVImportDryRunCommitReplayAndBacktest(t *testing.T) {
	svc, db := importFixture(t)
	barsCSV := importBarsHeader + importBarRow + "sh.600000,1d,2026-01-06,11,11,11,11,10000,110000\nsh.600000,1d,2026-01-07,12,12,12,12,10000,120000\n"
	request := func() CSVImportRequest { return importRequest(importInstrumentCSV, barsCSV) }
	r := request()
	r.DryRun = true
	out, err := svc.Import(context.Background(), r)
	if err != nil || !out.DryRun || out.Instruments.Inserted != 1 || out.Bars.Inserted != 3 {
		t.Fatalf("dry run: %+v %v", out, err)
	}
	assertImportCounts(t, db, 0, 0)
	out, err = svc.Import(context.Background(), request())
	if err != nil || out.DryRun || out.Instruments.Inserted != 1 || out.Bars.Inserted != 3 {
		t.Fatalf("commit: %+v %v", out, err)
	}
	out, err = svc.Import(context.Background(), request())
	if err != nil || out.Instruments.Unchanged != 1 || out.Bars.Unchanged != 3 || out.Bars.Inserted != 0 {
		t.Fatalf("replay: %+v %v", out, err)
	}
	assertImportCounts(t, db, 1, 3)
	market := NewInstrumentService(repository.NewInstrumentRepository(db), repository.NewGormBarRepository(db))
	item, err := market.GetByCode(context.Background(), "sh.600000")
	if err != nil || item.Name != "Sample stock" || item.Status != "active" || item.Exchange != "SSE" {
		t.Fatalf("metadata: %+v %v", item, err)
	}
	bars, err := market.RangeBars(context.Background(), item.Code, domain.Bar1d, time.Time{}, time.Time{})
	if err != nil || len(bars) != 3 || bars[0].Close != 10 || bars[2].Close != 12 {
		t.Fatalf("query: %+v %v", bars, err)
	}
	if state, source := factorProvenance("1d", bars); state != "unverified" || source != "" {
		t.Fatalf("manual factors became trusted: %s %s", state, source)
	}
	for _, bar := range bars {
		if bar.AdjSource != ManualImportSource {
			t.Fatal("missing import provenance")
		}
	}
	result, err := engine.Run(engine.RunInput{InitialCapital: 10000, Cost: &domain.Cost{}, FillAt: engine.FillAtClose, Decider: importRoundTripDecider{}, Symbols: []*engine.SymbolData{{Symbol: item.Code, Bars: bars}}})
	if err != nil || len(result.Trades) != 2 || len(result.Equity) != 3 || math.Abs(result.Result.TotalReturn-0.02) > 1e-12 {
		t.Fatalf("imported-data backtest: %+v %v", result, err)
	}
}

func TestCSVImportValidatesWholeInputBeforeWriting(t *testing.T) {
	svc, db := importFixture(t)
	cases := []struct{ name, instruments, bars string }{
		{"late bad number", importInstrumentCSV, importBarsHeader + importBarRow + "sh.600000,1d,2026-01-06,NaN,11,9,10,1,1\n"},
		{"invalid OHLC", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, ",10,10,10,10,", ",10,9,8,10,", 1)},
		{"negative volume", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, ",10000,", ",-1,", 1)},
		{"infinite price", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, ",10,10,10,10,", ",Inf,10,10,10,", 1)},
		{"overflow amount", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, ",100000\n", ",1e308\n", 1)},
		{"bad date", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, "2026-01-05", "2026-02-30", 1)},
		{"unknown period", importInstrumentCSV, importBarsHeader + strings.Replace(importBarRow, ",1d,", ",1h,", 1)},
		{"missing instrument", "", importBarsHeader + importBarRow},
		{"duplicate bars", importInstrumentCSV, importBarsHeader + importBarRow + importBarRow},
		{"duplicate instruments", importInstrumentCSV + "sh.600000,Other,stock\n", importBarsHeader + importBarRow},
		{"forged provenance", importInstrumentCSV, strings.TrimSuffix(importBarsHeader, "\n") + ",adj_source\n" + strings.TrimSuffix(importBarRow, "\n") + ",tushare.fund_adj\n"},
		{"unknown header", strings.Replace(importInstrumentCSV, "asset_type", "unknown", 1), ""},
		{"duplicate header", strings.Replace(importInstrumentCSV, "asset_type", "name", 1), ""},
		{"invalid identity", strings.Replace(importInstrumentCSV, "sh.600000", "../../data", 1), ""},
		{"wrong exchange", strings.TrimSuffix(strings.Split(importInstrumentCSV, "\n")[0], "\n") + ",exchange\nsh.600000,Sample,stock,SZSE\n", ""},
		{"whitespace", strings.Replace(importInstrumentCSV, "Sample stock", " Sample stock", 1), ""},
		{"malformed quoted row", "code,name,asset_type\nsh.600000,\"unfinished,stock\n", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := svc.Import(context.Background(), importRequest(tt.instruments, tt.bars))
			if err == nil {
				t.Fatal("invalid dataset accepted")
			}
			assertImportCounts(t, db, 0, 0)
		})
	}
}

func TestCSVImportReplacementPreservesOmittedMetadataAndRequiresProvenanceConsent(t *testing.T) {
	svc, db := importFixture(t)
	old := &domain.Instrument{Code: "sh.600000", Name: "Sample stock", AssetType: domain.AssetStock, Status: "delisted", Exchange: "SSE", FullName: "Retained full name", IndexName: "Retained index", Industry: "Retained industry"}
	if err := db.Create(old).Error; err != nil {
		t.Fatal(err)
	}
	day, _ := time.Parse("2006-01-02", "2026-01-05")
	bar := &domain.Bar{Code: old.Code, Period: domain.Bar1d, Date: day, Open: 10, High: 10, Low: 10, Close: 10, Volume: 10000, Amount: 100000, AdjFactor: 1, AdjSource: domain.AdjFactorTushareFund}
	if err := db.Create(bar).Error; err != nil {
		t.Fatal(err)
	}
	changedInstruments := strings.Replace(importInstrumentCSV, "Sample stock", "Changed name", 1)
	r := importRequest(changedInstruments, importBarsHeader+importBarRow)
	_, err := svc.Import(context.Background(), r)
	var conflict *CSVImportConflictError
	if !errors.As(err, &conflict) || conflict.Kind != "instrument" {
		t.Fatalf("metadata conflict missing: %v", err)
	}
	r = importRequest(importInstrumentCSV, importBarsHeader+importBarRow)
	_, err = svc.Import(context.Background(), r)
	if !errors.As(err, &conflict) || conflict.Kind != "bar" {
		t.Fatalf("provenance conflict missing: %v", err)
	}
	r = importRequest(changedInstruments, importBarsHeader+importBarRow)
	r.Replace = true
	r.DryRun = true
	out, err := svc.Import(context.Background(), r)
	if err != nil || out.Instruments.Updated != 1 || out.Bars.Updated != 1 {
		t.Fatalf("replacement preview: %+v %v", out, err)
	}
	var stored domain.Bar
	if err := db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AdjSource != domain.AdjFactorTushareFund {
		t.Fatal("dry-run replaced provenance")
	}
	r = importRequest(changedInstruments, importBarsHeader+importBarRow)
	r.Replace = true
	if _, err = svc.Import(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var item domain.Instrument
	if err := db.First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Name != "Changed name" || item.FullName != old.FullName || item.IndexName != old.IndexName || item.Industry != old.Industry || item.Status != "delisted" {
		t.Fatalf("omitted metadata lost: %+v", item)
	}
	if err := db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.AdjSource != ManualImportSource {
		t.Fatal("replace retained false trusted provenance")
	}
}

func TestCSVImportLateDatabaseFailureRollsBackMetadataAndBars(t *testing.T) {
	svc, db := importFixture(t)
	if _, err := svc.Import(context.Background(), importRequest(importInstrumentCSV, importBarsHeader+importBarRow)); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_import_bar BEFORE INSERT ON bar WHEN NEW.code = 'sh.600001' BEGIN SELECT RAISE(ABORT, 'forced bar write failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	instruments := strings.Replace(importInstrumentCSV, "Sample stock", "Must roll back", 1) + "sh.600001,New stock,stock\n"
	bars := importBarsHeader + strings.Replace(importBarRow, "sh.600000", "sh.600001", 1)
	r := importRequest(instruments, bars)
	r.Replace = true
	if _, err := svc.Import(context.Background(), r); err == nil {
		t.Fatal("expected trigger failure")
	}
	assertImportCounts(t, db, 1, 1)
	var original domain.Instrument
	if err := db.First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if original.Name != "Sample stock" {
		t.Fatal("metadata update escaped rollback")
	}
}

func TestCSVImportEncodingLimitsCancellationAndSchema(t *testing.T) {
	svc, db := importFixture(t)
	bomCRLF := "\xef\xbb\xbf" + strings.ReplaceAll(importInstrumentCSV, "\n", "\r\n")
	if _, err := svc.Import(context.Background(), importRequest(bomCRLF, "")); err != nil {
		t.Fatalf("BOM/CRLF: %v", err)
	}
	for _, raw := range []string{strings.Repeat("x", MaxCSVImportBytes+1), "code,name,asset_type\nsh.600001,\xff,stock\n"} {
		if _, err := svc.Import(context.Background(), importRequest(raw, "")); err == nil {
			t.Fatal("unbounded or invalid encoding accepted")
		}
	}
	err := readImportCSV(context.Background(), "test.csv", strings.NewReader("code\na\nb\n"), 1, []string{"code"}, nil, func(int, map[string]int, []string) error { return nil })
	if err == nil {
		t.Fatal("row limit not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.Import(ctx, importRequest(importInstrumentCSV, "")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	assertImportCounts(t, db, 1, 0)
	if err := db.Migrator().DropTable(&domain.Bar{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Import(context.Background(), importRequest(importInstrumentCSV, "")); !errors.Is(err, repository.ErrCSVImportSchema) {
		t.Fatalf("schema error: %v", err)
	}
}

func TestCSVImportReturnedDatesMatchAcrossTimezones(t *testing.T) {
	for _, zone := range []struct {
		name   string
		offset int
	}{{"negative_UTC_offset", -8 * 60 * 60}, {"positive_UTC_offset", 8 * 60 * 60}} {
		t.Run(zone.name, func(t *testing.T) {
			svc, db := importFixture(t)
			instruments := "code,name,asset_type,listed_date\nsh.600000,Sample stock,stock,2026-01-02\n"
			bars := importBarsHeader + importBarRow
			if _, err := svc.Import(context.Background(), importRequest(instruments, bars)); err != nil {
				t.Fatal(err)
			}
			location := time.FixedZone(zone.name, zone.offset)
			barReads, instrumentReads := 0, 0
			// Simulate the MySQL driver's loc decoding after a successful SQL
			// lookup. The returned instant is unchanged, but a negative offset
			// represents UTC midnight on the previous local calendar date.
			if err := db.Callback().Query().After("gorm:query").Register("test:driver_location", func(tx *gorm.DB) {
				switch rows := tx.Statement.Dest.(type) {
				case *[]*domain.Bar:
					for _, row := range *rows {
						row.Date = row.Date.In(location)
						barReads++
					}
				case *[]*domain.Instrument:
					for _, row := range *rows {
						if row.ListedDate != nil {
							date := row.ListedDate.In(location)
							row.ListedDate = &date
							instrumentReads++
						}
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			for _, dryRun := range []bool{true, false} {
				// Check bars separately so a listed_date mismatch cannot mask the
				// independent bar-key replay regression.
				r := importRequest("", bars)
				r.DryRun = dryRun
				out, err := svc.Import(context.Background(), r)
				if err != nil || out.Bars.Unchanged != 1 || out.Bars.Inserted != 0 || out.Bars.Updated != 0 {
					t.Fatalf("bar replay dry=%v: %+v %v", dryRun, out, err)
				}
				r = importRequest(instruments, "")
				r.DryRun = dryRun
				out, err = svc.Import(context.Background(), r)
				if err != nil || out.Instruments.Unchanged != 1 || out.Instruments.Inserted != 0 || out.Instruments.Updated != 0 {
					t.Fatalf("metadata replay dry=%v: %+v %v", dryRun, out, err)
				}
			}
			changedInstruments := strings.Replace(instruments, "2026-01-02", "2026-01-03", 1)
			changedBars := strings.Replace(bars, ",10,10,10,10,", ",11,11,11,11,", 1)
			r := importRequest(changedInstruments, changedBars)
			if _, err := svc.Import(context.Background(), r); err == nil {
				t.Fatal("different instants/prices bypassed replacement consent")
			}
			for _, dryRun := range []bool{true, false} {
				r = importRequest(changedInstruments, changedBars)
				r.Replace = true
				r.DryRun = dryRun
				out, err := svc.Import(context.Background(), r)
				if err != nil || out.Instruments.Updated != 1 || out.Bars.Updated != 1 || out.Bars.Inserted != 0 {
					t.Fatalf("replacement dry=%v: %+v %v", dryRun, out, err)
				}
			}
			out, err := svc.Import(context.Background(), importRequest(changedInstruments, changedBars))
			if err != nil || out.Instruments.Unchanged != 1 || out.Bars.Unchanged != 1 {
				t.Fatalf("replacement replay: %+v %v", out, err)
			}
			assertImportCounts(t, db, 1, 1)
			if barReads == 0 || instrumentReads == 0 {
				t.Fatal("timezone decoding fixture was not exercised")
			}
		})
	}
}
