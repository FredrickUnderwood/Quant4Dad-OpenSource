package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"go.uber.org/zap"
)

const (
	MaxCSVImportBytes       = 32 << 20 // per input file; both inputs are validated before writing
	MaxCSVImportInstruments = 10000
	MaxCSVImportBars        = 100000
	ManualImportSource      = "manual_import"
)

var importCodePattern = regexp.MustCompile(`^(sh|sz|bj)\.[0-9]{6}$`)

type CSVImportRequest struct {
	Instruments io.Reader
	Bars        io.Reader
	DryRun      bool
	Replace     bool
}

type CSVImportCounts struct {
	Inserted  int `json:"inserted"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

type CSVImportResult struct {
	DryRun      bool            `json:"dry_run"`
	Instruments CSVImportCounts `json:"instruments"`
	Bars        CSVImportCounts `json:"bars"`
}

// CSVImportError includes only a fixed input label, position and validation
// reason. Raw cell contents are never echoed to a terminal or log.
type CSVImportError struct {
	File   string
	Row    int
	Field  string
	Reason string
}

func (e *CSVImportError) Error() string {
	return fmt.Sprintf("%s row %d %s: %s", e.File, e.Row, e.Field, e.Reason)
}

type CSVImportConflictError struct {
	Kind string
	Key  string
}

func (e *CSVImportConflictError) Error() string {
	return e.Kind + " " + e.Key + " differs from stored data; review the input and use -replace to overwrite"
}

type CSVImportService struct {
	repo *repository.CSVImportRepository
}

func NewCSVImportService(repo *repository.CSVImportRepository) *CSVImportService {
	return &CSVImportService{repo: repo}
}

type importedInstrument struct {
	item   *domain.Instrument
	fields map[string]int
}
type importedCSV struct {
	instruments []importedInstrument
	bars        []*domain.Bar
	codes       []string
}

// Import validates the entire input before opening a transaction. It then
// checks every existing key and reference before issuing any data writes.
func (s *CSVImportService) Import(ctx context.Context, req CSVImportRequest) (CSVImportResult, error) {
	out := CSVImportResult{DryRun: req.DryRun}
	data, err := parseImportCSV(ctx, req)
	if err != nil {
		return out, err
	}
	if err := s.repo.CheckSchema(ctx); err != nil {
		return out, err
	}
	err = s.repo.Transaction(ctx, req.DryRun, func(tx *repository.CSVImportTransaction) error {
		known := make(map[string]*domain.Instrument, len(data.codes))
		for start := 0; start < len(data.codes); start += 400 {
			end := min(start+400, len(data.codes))
			rows, err := tx.Instruments(ctx, data.codes[start:end])
			if err != nil {
				return err
			}
			for _, row := range rows {
				known[row.Code] = row
			}
		}
		var createInstruments, replaceInstruments []*domain.Instrument
		for _, row := range data.instruments {
			old, exists := known[row.item.Code]
			if !exists {
				createInstruments = append(createInstruments, row.item)
				known[row.item.Code] = row.item
				out.Instruments.Inserted++
				continue
			}
			merged, changed := mergeImportedInstrument(old, row)
			if !changed {
				out.Instruments.Unchanged++
				continue
			}
			if !req.Replace {
				return &CSVImportConflictError{Kind: "instrument", Key: row.item.Code}
			}
			merged.UpdatedAt = time.Now().UTC()
			replaceInstruments = append(replaceInstruments, merged)
			out.Instruments.Updated++
		}
		for _, code := range data.codes {
			if known[code] == nil {
				return &CSVImportError{File: "bars.csv", Field: "code", Reason: "instrument " + code + " does not exist; include it in instruments.csv"}
			}
		}
		var createBars, replaceBars []*domain.Bar
		for start := 0; start < len(data.bars); {
			first := data.bars[start]
			end := start
			var dates []time.Time
			for end < len(data.bars) && end-start < 400 && data.bars[end].Code == first.Code && data.bars[end].Period == first.Period {
				dates = append(dates, data.bars[end].Date)
				end++
			}
			rows, err := tx.Bars(ctx, first.Code, first.Period, dates)
			if err != nil {
				return err
			}
			stored := make(map[string]*domain.Bar, len(rows))
			for _, row := range rows {
				stored[importTimestampKey(row.Date)] = row
			}
			for _, row := range data.bars[start:end] {
				old := stored[importTimestampKey(row.Date)]
				if old == nil {
					createBars = append(createBars, row)
					out.Bars.Inserted++
					continue
				}
				if equalImportedBar(old, row) {
					out.Bars.Unchanged++
					continue
				}
				if !req.Replace {
					return &CSVImportConflictError{Kind: "bar", Key: importBarKey(row)}
				}
				replaceBars = append(replaceBars, row)
				out.Bars.Updated++
			}
			start = end
		}
		if req.DryRun {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tx.CreateInstruments(ctx, createInstruments); err != nil {
			return err
		}
		if err := tx.ReplaceInstruments(ctx, replaceInstruments); err != nil {
			return err
		}
		if err := tx.CreateBars(ctx, createBars); err != nil {
			return err
		}
		return tx.ReplaceBars(ctx, replaceBars)
	})
	if err != nil {
		return CSVImportResult{DryRun: req.DryRun}, err
	}
	if !req.DryRun {
		logger.L().Info("CSV import committed", zap.Int("instruments_inserted", out.Instruments.Inserted), zap.Int("instruments_updated", out.Instruments.Updated), zap.Int("bars_inserted", out.Bars.Inserted), zap.Int("bars_updated", out.Bars.Updated))
	}
	return out, nil
}

func mergeImportedInstrument(old *domain.Instrument, row importedInstrument) (*domain.Instrument, bool) {
	out := *old
	out.Name, out.AssetType = row.item.Name, row.item.AssetType
	if _, ok := row.fields["industry"]; ok {
		out.Industry = row.item.Industry
	}
	if _, ok := row.fields["status"]; ok {
		out.Status = row.item.Status
	}
	if _, ok := row.fields["exchange"]; ok {
		out.Exchange = row.item.Exchange
	}
	if _, ok := row.fields["listed_date"]; ok {
		out.ListedDate = row.item.ListedDate
	}
	dateEqual := (old.ListedDate == nil && out.ListedDate == nil) || (old.ListedDate != nil && out.ListedDate != nil && old.ListedDate.Equal(*out.ListedDate))
	changed := old.Name != out.Name || old.AssetType != out.AssetType || old.Industry != out.Industry || old.Status != out.Status || old.Exchange != out.Exchange || !dateEqual
	return &out, changed
}

func equalImportedBar(a, b *domain.Bar) bool {
	// Provenance is part of the content. Replacing provider-verified rows with
	// manual data requires the same explicit -replace as a changed price.
	return a.Open == b.Open && a.High == b.High && a.Low == b.Low && a.Close == b.Close && a.Volume == b.Volume && a.Amount == b.Amount && a.AdjFactor == b.AdjFactor && a.AdjSource == b.AdjSource
}
func importBarKey(b *domain.Bar) string {
	return b.Code + "/" + string(b.Period) + "/" + b.Date.UTC().Format("2006-01-02")
}

// MySQL returns DATETIME values in the configured driver location. Match the
// stored instant, not its local calendar date, to preserve replay and replacement
// when UTC midnight is represented on the previous day in a negative offset.
func importTimestampKey(date time.Time) string {
	return date.UTC().Format(time.RFC3339Nano)
}

func parseImportCSV(ctx context.Context, req CSVImportRequest) (*importedCSV, error) {
	out := &importedCSV{}
	codes := make(map[string]bool)
	seenInstruments, seenBars := make(map[string]bool), make(map[string]bool)
	if req.Instruments != nil {
		err := readImportCSV(ctx, "instruments.csv", req.Instruments, MaxCSVImportInstruments,
			[]string{"code", "name", "asset_type"}, []string{"industry", "status", "exchange", "listed_date"},
			func(row int, fields map[string]int, cells []string) error {
				get := func(key string) string {
					i, ok := fields[key]
					if !ok {
						return ""
					}
					return cells[i]
				}
				fail := func(field, reason string) error {
					return &CSVImportError{File: "instruments.csv", Row: row, Field: field, Reason: reason}
				}
				code := get("code")
				if !importCodePattern.MatchString(code) {
					return fail("code", "expected sh., sz. or bj. followed by six digits")
				}
				if seenInstruments[code] {
					return fail("code", "duplicate instrument in input")
				}
				seenInstruments[code] = true
				name := get("name")
				if name == "" || utf8.RuneCountInString(name) > 64 {
					return fail("name", "must contain 1 to 64 characters")
				}
				asset := domain.AssetType(get("asset_type"))
				if asset != domain.AssetStock && asset != domain.AssetETF {
					return fail("asset_type", "expected stock or etf")
				}
				status := "active"
				if _, ok := fields["status"]; ok {
					status = get("status")
				}
				if status != "active" && status != "delisted" && status != "pending" {
					return fail("status", "expected active, delisted or pending")
				}
				if utf8.RuneCountInString(get("industry")) > 64 {
					return fail("industry", "maximum 64 characters")
				}
				exchange := map[string]string{"sh": "SSE", "sz": "SZSE", "bj": "BSE"}[code[:2]]
				if _, ok := fields["exchange"]; ok && get("exchange") != exchange {
					return fail("exchange", "expected SSE, SZSE or BSE matching code")
				}
				item := &domain.Instrument{Code: code, Name: name, AssetType: asset, Status: status, Industry: get("industry"), Exchange: exchange}
				if get("listed_date") != "" {
					d, err := importDate(get("listed_date"))
					if err != nil {
						return fail("listed_date", err.Error())
					}
					item.ListedDate = &d
				}
				out.instruments = append(out.instruments, importedInstrument{item: item, fields: fields})
				codes[code] = true
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	if req.Bars != nil {
		err := readImportCSV(ctx, "bars.csv", req.Bars, MaxCSVImportBars,
			[]string{"code", "period", "date", "open", "high", "low", "close", "volume", "amount"}, []string{"adj_factor"},
			func(row int, fields map[string]int, cells []string) error {
				get := func(key string) string {
					i, ok := fields[key]
					if !ok {
						return ""
					}
					return cells[i]
				}
				fail := func(field, reason string) error {
					return &CSVImportError{File: "bars.csv", Row: row, Field: field, Reason: reason}
				}
				code := get("code")
				if !importCodePattern.MatchString(code) {
					return fail("code", "expected sh., sz. or bj. followed by six digits")
				}
				period := domain.BarPeriod(get("period"))
				if period != domain.Bar1d && period != domain.Bar1w && period != domain.Bar1mo {
					return fail("period", "expected 1d, 1w or 1mo")
				}
				date, err := importDate(get("date"))
				if err != nil {
					return fail("date", err.Error())
				}
				b := &domain.Bar{Code: code, Period: period, Date: date, AdjFactor: 1, AdjSource: ManualImportSource}
				for _, field := range []struct {
					name   string
					dest   *float64
					zeroOK bool
				}{{"open", &b.Open, false}, {"high", &b.High, false}, {"low", &b.Low, false}, {"close", &b.Close, false}, {"volume", &b.Volume, true}, {"amount", &b.Amount, true}} {
					n, err := importNumber(get(field.name), field.zeroOK)
					if err != nil {
						return fail(field.name, err.Error())
					}
					*field.dest = n
				}
				if _, ok := fields["adj_factor"]; ok {
					n, err := importNumber(get("adj_factor"), false)
					if err != nil {
						return fail("adj_factor", err.Error())
					}
					b.AdjFactor = n
				}
				if b.Low > b.High || b.Open < b.Low || b.Open > b.High || b.Close < b.Low || b.Close > b.High {
					return fail("OHLC", "low <= open/close <= high is required")
				}
				key := importBarKey(b)
				if seenBars[key] {
					return fail("date", "duplicate code/period/date in input")
				}
				seenBars[key] = true
				out.bars = append(out.bars, b)
				codes[code] = true
				return nil
			})
		if err != nil {
			return nil, err
		}
	}
	if len(out.instruments) == 0 && len(out.bars) == 0 {
		return nil, &CSVImportError{File: "input", Field: "file", Reason: "provide at least one non-empty instruments or bars CSV"}
	}
	if len(codes) > MaxCSVImportInstruments {
		return nil, &CSVImportError{File: "input", Field: "code", Reason: "import exceeds 10000 distinct instrument codes"}
	}
	for code := range codes {
		out.codes = append(out.codes, code)
	}
	sort.Strings(out.codes)
	sort.Slice(out.instruments, func(i, j int) bool { return out.instruments[i].item.Code < out.instruments[j].item.Code })
	sort.Slice(out.bars, func(i, j int) bool { return importBarKey(out.bars[i]) < importBarKey(out.bars[j]) })
	return out, nil
}

func importDate(value string) (time.Time, error) {
	date, err := time.Parse("2006-01-02", value)
	if err != nil || date.Year() < 1900 || date.Year() > 2100 || date.Format("2006-01-02") != value {
		return time.Time{}, errors.New("expected YYYY-MM-DD between 1900 and 2100")
	}
	return date, nil
}
func importNumber(value string, zeroOK bool) (float64, error) {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || n < 0 || (!zeroOK && n == 0) || n > 1e15 {
		return 0, errors.New("expected a finite number in the permitted range (positive prices/factors, nonnegative volume/amount, maximum 1e15)")
	}
	return n, nil
}

func readImportCSV(ctx context.Context, label string, input io.Reader, maxRows int, required, optional []string, visit func(int, map[string]int, []string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxCSVImportBytes+1))
	if err != nil {
		return err
	}
	fail := func(row int, field, reason string) error {
		return &CSVImportError{File: label, Row: row, Field: field, Reason: reason}
	}
	if len(data) > MaxCSVImportBytes {
		return fail(0, "file", "maximum 32 MiB per input file")
	}
	if !utf8.Valid(data) {
		return fail(0, "file", "UTF-8 input required")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	r := csv.NewReader(bytes.NewReader(data))
	header, err := r.Read()
	if err != nil {
		return fail(1, "header", "missing or invalid CSV header")
	}
	allowed := make(map[string]bool)
	for _, key := range append(append([]string{}, required...), optional...) {
		allowed[key] = true
	}
	fields := make(map[string]int)
	for i, name := range header {
		if !allowed[name] {
			return fail(1, "header", "unknown column; use only documented columns (adj_source is not accepted)")
		}
		if _, exists := fields[name]; exists {
			return fail(1, "header", "duplicate column")
		}
		fields[name] = i
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fail(1, key, "required column missing")
		}
	}
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cells, err := r.Read()
		if err == io.EOF {
			if count == 0 {
				return fail(2, "file", "at least one data row is required")
			}
			return nil
		}
		if err != nil {
			return fail(count+2, "CSV", "invalid quoting or inconsistent number of fields")
		}
		row, _ := r.FieldPos(0)
		if count >= maxRows {
			return fail(row, "file", "row limit exceeded")
		}
		for _, cell := range cells {
			if len(cell) > 4096 || strings.TrimSpace(cell) != cell || strings.IndexFunc(cell, unicode.IsControl) >= 0 {
				return fail(row, "cell", "no control characters, surrounding whitespace or values over 4096 bytes")
			}
		}
		if err := visit(row, fields, cells); err != nil {
			return err
		}
	}
}
