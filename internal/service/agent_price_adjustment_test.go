package service

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pythonexec"
)

func saveAdjustedAnalysis(t *testing.T, s *AgentKlineFileService, ctx context.Context, result QueryKlineResult) string {
	t.Helper()
	if result.Code == "" {
		result.Code = "sh.600000"
	}
	if result.Period == "" {
		result.Period = "1d"
	}
	result.Count = len(result.Bars)
	for _, b := range result.Bars {
		b.Code, b.Period = result.Code, domain.BarPeriod(result.Period)
	}
	descriptor, err := s.Save(ctx, result)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.FileID
}

func adjustedFixtureBar(day string, close, factor float64) *domain.Bar {
	b := analysisBar(day, close, close)
	b.AdjFactor, b.AdjSource, b.Amount = factor, domain.AdjFactorTushareFund, 1234
	return b
}

func TestQFQUsesOneFixedAnchorForPythonDailyWeeklyAndMonthlyCharts(t *testing.T) {
	for _, period := range []string{"1d", "1w", "1mo"} {
		t.Run(period, func(t *testing.T) {
			s, ctx := analysisFixture(t)
			executor := &pythonTestExecutor{output: `{}`}
			s.python = executor
			bars := []*domain.Bar{
				adjustedFixtureBar("2025-01-27", 10, 1),
				adjustedFixtureBar("2025-01-28", 9, 10.0/9),
				adjustedFixtureBar("2025-01-31", 9.9, 10.0/9),
				// This later corporate action must not change a historical anchor.
				adjustedFixtureBar("2025-02-03", 5, 2),
			}
			left := saveAdjustedAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[:2]})
			right := saveAdjustedAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[1:]})
			out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{right, left}, Code: "print('{}')", Adjustment: "qfq", EndDate: "2025-01-31", ChartPeriod: period})
			if err != nil || out.Status != "succeeded" || out.Chart == nil || out.PriceBasis != "qfq" || out.AdjustmentStatus != "verified" {
				t.Fatal(err, out)
			}
			if out.InputCount != 3 || len(out.Adjustments) != 1 || out.Adjustments[0].AnchorDate != "2025-01-31" || out.Adjustments[0].AnchorFactor != 10.0/9 {
				t.Fatal(out)
			}
			var input struct {
				Datasets     []PythonDataset `json:"datasets"`
				PriceBasis   string          `json:"price_basis"`
				ChartContext map[string]any  `json:"chart_context"`
			}
			if err := sonic.Unmarshal(executor.input, &input); err != nil {
				t.Fatal(err)
			}
			if input.PriceBasis != "qfq" || input.ChartContext["price_basis"] != "qfq" || len(input.Datasets) != 1 {
				t.Fatal(input)
			}
			rows := input.Datasets[0].Rows
			if len(rows) != 3 || math.Abs(rows[0][4].(float64)-9) > 1e-12 || rows[1][4] != float64(9) || rows[2][4] != 9.9 || rows[0][5] != float64(100) || rows[0][6] != float64(1234) {
				t.Fatal(rows)
			}
			if period != "1d" {
				if len(out.Chart.Rows) != 1 || math.Abs(out.Chart.Rows[0][1].(float64)-9) > 1e-12 || out.Chart.Rows[0][2] != 9.9 || out.Chart.Rows[0][4] != 9.9 || out.Chart.Rows[0][5] != float64(300) {
					t.Fatal("aggregate was adjusted after grouping", out.Chart.Rows)
				}
			}
			stored, err := s.load(ctx, left)
			if err != nil || stored.Result.Bars[0].Close != 10 || bars[0].Close != 10 {
				t.Fatal("raw source mutated")
			}
			// Selecting none still delivers original OHLC after a qfq request.
			raw, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{left}, Code: "print('{}')", ChartPeriod: "1d"})
			if err != nil || raw.PriceBasis != "stored_ohlc" || raw.Chart.Rows[0][4] != float64(10) {
				t.Fatal(err, raw)
			}
		})
	}
}

func TestQFQRejectsMissingProvenanceAndMixedTrustBeforePython(t *testing.T) {
	for _, scenario := range []string{"legacy_one", "mixed", "zero", "wrong_source", "weekly", "no_anchor", "future_only"} {
		t.Run(scenario, func(t *testing.T) {
			s, ctx := analysisFixture(t)
			executor := &pythonTestExecutor{output: `{}`}
			s.python = executor
			bars := []*domain.Bar{adjustedFixtureBar("2025-01-02", 10, 1), adjustedFixtureBar("2025-01-03", 9, 1.1)}
			period, end := "1d", "2025-01-03"
			switch scenario {
			case "legacy_one":
				for _, b := range bars {
					b.AdjSource, b.AdjFactor = "", 1
				}
			case "mixed":
				bars[0].AdjSource = ""
			case "zero":
				bars[0].AdjFactor = 0
			case "wrong_source":
				bars[0].AdjSource = "placeholder"
			case "weekly":
				period = "1w"
			case "no_anchor":
				end = ""
			case "future_only":
				end = "2024-12-31"
			}
			id := saveAdjustedAnalysis(t, s, ctx, QueryKlineResult{Bars: bars, Period: period})
			out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{id}, Code: "print('{}')", Adjustment: "qfq", EndDate: end, ChartPeriod: "1d"})
			if err != nil || out.Status != "invalid_output" || executor.calls != 0 || out.Chart != nil || out.AdjustmentStatus != "unverified" || len(out.InputSHA256) != 64 || !strings.Contains(out.Warnings[0], "脚本未运行") {
				t.Fatal("untrusted adjustment executed", err, out, executor.calls)
			}
		})
	}
}

func TestQFQConstantFactorsAndMultipleInstrumentAnchors(t *testing.T) {
	s, ctx := analysisFixture(t)
	executor := &pythonTestExecutor{output: `{}`}
	s.python = executor
	var ids []string
	for i, factor := range []float64{1, 2.0462} {
		code := []string{"sh.563020", "sh.512890"}[i]
		id := saveAdjustedAnalysis(t, s, ctx, QueryKlineResult{Code: code, Bars: []*domain.Bar{adjustedFixtureBar("2025-01-02", 10, factor), adjustedFixtureBar("2025-01-03", 11, factor)}})
		ids = append(ids, id)
	}
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: ids, Code: "print('{}')", Adjustment: "qfq", EndDate: "2025-01-05"})
	if err != nil || out.Status != "succeeded" || len(out.Adjustments) != 2 {
		t.Fatal(err, out)
	}
	for _, meta := range out.Adjustments {
		if meta.AnchorDate != "2025-01-03" || meta.Source != domain.AdjFactorTushareFund {
			t.Fatal(meta)
		}
	}
	var input struct {
		Datasets []PythonDataset `json:"datasets"`
	}
	if err := sonic.Unmarshal(executor.input, &input); err != nil {
		t.Fatal(err)
	}
	for _, d := range input.Datasets {
		if d.Rows[0][4] != float64(10) || d.Rows[1][4] != float64(11) || d.AdjustmentStatus != "verified" {
			t.Fatal("constant real factor rejected or applied twice", d)
		}
	}
}

func TestQFQActualPythonUsesAdjustedInputAndCanonicalSMA(t *testing.T) {
	path := os.Getenv("Q4D_PYTHON_RUNTIME_DIR")
	if path == "" {
		t.Skip("real CPython assets required")
	}
	runtime, err := pythonexec.New(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	s, ctx := analysisFixture(t)
	s.python = runtime
	id := saveAdjustedAnalysis(t, s, ctx, QueryKlineResult{Bars: []*domain.Bar{
		adjustedFixtureBar("2025-01-02", 10, 1), adjustedFixtureBar("2025-01-03", 9, 10.0/9),
	}})
	source := `import json
from decimal import Decimal
d=json.load(open('/data/input.json'))
rows=d['datasets'][0]['rows']
a,b=[Decimal(str(r[4])) for r in rows]
print(json.dumps({'metrics':{'return_pct':float((b/a-1)*100)},'chart':{'lines':[{'name':'MA2','points':[[rows[1][0],float((a+b)/2)]]}]}},allow_nan=False))`
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{id}, Code: source, Adjustment: "qfq", EndDate: "2025-01-03", ChartPeriod: "1d"})
	if err != nil || out.Status != "succeeded" || out.Chart == nil {
		t.Fatal(err, out)
	}
	metrics := out.Result["metrics"].(map[string]any)
	if math.Abs(metrics["return_pct"].(float64)) > 1e-10 || math.Abs(out.Chart.Lines[0].Points[0][1].(float64)-9) > 1e-10 {
		t.Fatal("dividend created a false loss or SMA mismatch", out)
	}
}
