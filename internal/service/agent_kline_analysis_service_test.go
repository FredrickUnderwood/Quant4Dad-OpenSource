package service

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func analysisFixture(t *testing.T) (*AgentKlineFileService, context.Context) {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := repository.NewAgentToolArtifactRepository(filepath.Join(directory, "kline"))
	if err != nil {
		t.Fatal(err)
	}
	return NewAgentKlineFileService(repo), WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "session"}, domain.AgentApprovalReceipt{})
}
func analysisBar(date string, close, high float64) *domain.Bar {
	d, _ := time.Parse("2006-01-02", date)
	return &domain.Bar{Code: "sh.600000", Period: domain.Bar1d, Date: d, Open: close, High: high, Low: close, Close: close, Volume: 100, AdjFactor: 1}
}
func saveAnalysis(t *testing.T, svc *AgentKlineFileService, ctx context.Context, result QueryKlineResult) string {
	t.Helper()
	result.Code, result.Period, result.Count = "sh.600000", "1d", len(result.Bars)
	if len(result.Bars) > 0 {
		result.DataAsOf = result.Bars[len(result.Bars)-1].Date.Format("2006-01-02")
	}
	descriptor, err := svc.Save(ctx, result)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor.FileID
}

func TestKlineAnalysisExactThresholdPrecedingCloseAndMetrics(t *testing.T) {
	svc, ctx := analysisFixture(t)
	id := saveAnalysis(t, svc, ctx, QueryKlineResult{PreviousBar: analysisBar("2025-01-01", 100, 100), Bars: []*domain.Bar{analysisBar("2025-01-02", 104, 105), analysisBar("2025-01-03", 104, 110)}})
	for _, tc := range []struct {
		comparison, metric string
		matches            int
	}{{"gte", "close_return", 1}, {"gt", "close_return", 0}, {"gte", "high_return", 2}} {
		out, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: id, Comparison: tc.comparison, Metric: tc.metric})
		if err != nil {
			t.Fatal(err)
		}
		if out.MatchedCount != tc.matches || out.EligibleCount != 2 || out.ExcludedCount != 0 || out.BarCount != 2 || len(out.Chart.Rows) != 2 || out.CoverageVerified || out.AdjustmentStatus != "unverified" {
			t.Fatalf("bad analysis: %+v", out)
		}
		if tc.metric == "close_return" && tc.matches == 1 && out.Matches.Rows[0][4] != float64(4) {
			t.Fatal("threshold was rounded or float-drifted", out.Matches)
		}
	}
	// The analysis subset retains the previous row but does not chart or count it.
	out, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: id, StartDate: "2025-01-03", EndDate: "2025-01-03", Metric: "high_return"})
	if err != nil || out.BarCount != 1 || out.EligibleCount != 1 || out.MatchedCount != 1 || out.Matches.Rows[0][1] != "2025-01-02" {
		t.Fatalf("subset: %+v %v", out, err)
	}
}

func TestKlineAnalysisMissingBaselineEmptyTruncatedAndOwnership(t *testing.T) {
	svc, ctx := analysisFixture(t)
	id := saveAnalysis(t, svc, ctx, QueryKlineResult{Truncated: true, RequestedStart: "2025-01-01", RequestedEnd: "2025-01-06", Bars: []*domain.Bar{analysisBar("2025-01-02", 100, 100), analysisBar("2025-01-06", 105, 105)}})
	out, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: id})
	if err != nil || out.EligibleCount != 1 || out.ExcludedCount != 1 || out.MatchedCount != 1 || !out.Truncated || out.CoverageVerified || len(out.Warnings) < 5 || *out.MatchRatePct != 100 || out.RequestedStart != "2025-01-01" {
		t.Fatalf("partial coverage: %+v %v", out, err)
	}
	if out.Matches.Rows[0][1] != "2025-01-02" {
		t.Fatal("gap must preserve actual preceding date")
	}
	out, err = svc.Analyze(ctx, AnalyzeKlineInput{FileID: id, StartDate: "2026-01-01", EndDate: "2026-01-02"})
	if err != nil || out.BarCount != 0 || out.MatchRatePct != nil || out.Matches.Rows == nil || out.Chart.Rows == nil {
		t.Fatalf("empty interval: %+v %v", out, err)
	}
	for _, other := range []context.Context{context.Background(), WithAgentExecution(ctx, domain.AgentToolAudit{ID: "other-audit", ActorID: "other", SessionID: "session"}, domain.AgentApprovalReceipt{}), WithAgentExecution(ctx, domain.AgentToolAudit{ID: "other-audit", ActorID: "owner", SessionID: "other"}, domain.AgentApprovalReceipt{})} {
		if _, err := svc.Analyze(other, AnalyzeKlineInput{FileID: id}); err == nil {
			t.Fatal("unowned file accepted")
		}
	}
	stored, err := svc.load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored.ExpiresAt = time.Now().Add(-time.Minute)
	expired, err := sonic.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := svc.files.Put(expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: ref[:64]}); err == nil {
		t.Fatal("expired analysis accepted")
	}
	for _, input := range []AnalyzeKlineInput{{FileID: "../private"}, {FileID: id, StartDate: "2025-02-30"}, {FileID: id, StartDate: "2025-01-07", EndDate: "2025-01-01"}, {FileID: id, Metric: "official_return"}, {FileID: id, Comparison: "eq"}} {
		if _, err := svc.Analyze(ctx, input); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestKlineAnalysisRejectsCorruptMarketData(t *testing.T) {
	for _, mutate := range []func([]*domain.Bar){
		func(b []*domain.Bar) { b[1].Date = b[0].Date },
		func(b []*domain.Bar) { b[1].Date = b[0].Date.Add(time.Hour) },
		func(b []*domain.Bar) { b[1].Date = b[0].Date.AddDate(0, 0, -1) },
		func(b []*domain.Bar) { b[1].High = 99 },
		func(b []*domain.Bar) { b[1].Low = 120 },
		func(b []*domain.Bar) { b[1].Close = 0 },
		func(b []*domain.Bar) { b[1].Volume = -1 },
	} {
		svc, ctx := analysisFixture(t)
		bars := []*domain.Bar{analysisBar("2025-01-01", 100, 100), analysisBar("2025-01-02", 104, 104)}
		mutate(bars)
		id := saveAnalysis(t, svc, ctx, QueryKlineResult{Bars: bars})
		if _, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: id}); err == nil {
			t.Fatal("corrupt candles accepted")
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		bar := analysisBar("2025-01-01", value, value)
		if validAnalysisBar(bar) {
			t.Fatal("nonfinite candle accepted")
		}
		svc, ctx := analysisFixture(t)
		if _, err := svc.Analyze(ctx, AnalyzeKlineInput{ThresholdPct: &value}); err == nil {
			t.Fatal("nonfinite threshold accepted")
		}
	}
}

type analysisQueryBars struct {
	repository.BarRepository
	bars []*domain.Bar
}

func (r analysisQueryBars) RangeLatest(_ context.Context, _ string, _ domain.BarPeriod, start, end time.Time, limit int) ([]*domain.Bar, error) {
	out := []*domain.Bar{}
	for _, bar := range r.bars {
		if (start.IsZero() || !bar.Date.Before(start)) && (end.IsZero() || !bar.Date.After(end)) {
			out = append(out, bar)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func TestQueryKlineRetainsOnlyOnePrecedingBarForAnalysis(t *testing.T) {
	svc := NewInstrumentService(nil, analysisQueryBars{bars: []*domain.Bar{analysisBar("2024-12-31", 100, 100), analysisBar("2025-01-02", 104, 104), analysisBar("2025-01-03", 105, 105)}})
	limit := 2
	for _, start := range []string{"", "2025-01-01"} {
		result, err := svc.QueryKline(context.Background(), QueryKlineInput{Code: "sh.600000", Start: start, Limit: &limit})
		if err != nil || result.Count != 2 || result.PreviousBar == nil || result.PreviousBar.Close != 100 || result.Bars[0].Close != 104 || result.Truncated != (start == "") {
			t.Fatalf("baseline: %+v %v", result, err)
		}
	}
}

// Entirely synthetic prices repeat a four-day cycle. Calendar dates and values
// are generated here; this test contains no captured market or user research data.
func TestKlineAnalysisSyntheticRangeAndMetricBoundaries(t *testing.T) {
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]*domain.Bar, 37)
	for i := range bars {
		close, high := 100.0, 100.0
		switch i % 4 {
		case 1:
			close, high = 105, 110
		case 2:
			close, high = 100, 105
		case 3:
			close, high = 95, 106
		}
		bars[i] = analysisBar(start.AddDate(0, 0, i).Format("2006-01-02"), close, high)
	}
	svc, ctx := analysisFixture(t)
	id := saveAnalysis(t, svc, ctx, QueryKlineResult{Bars: bars})
	// The selected 20 days contain five complete cycles. Close returns in each
	// cycle are +5%, -100/21%, -5%, +100/19%; high returns are +10%, 0%, +6%,
	// +100/19%. Expected counts below follow from that hand calculation.
	for _, tc := range []struct {
		metric, comparison string
		threshold          float64
		matches            int
		firstMatch         string
	}{
		{"close_return", "gte", 5, 10, "2030-01-06"},
		{"close_return", "gt", 5, 5, "2030-01-09"},
		{"high_return", "gte", 6, 10, "2030-01-06"},
		{"high_return", "gt", 6, 5, "2030-01-06"},
		{"close_return", "lte", -5, 5, "2030-01-08"},
		{"close_return", "lt", -5, 0, ""},
	} {
		t.Run(tc.metric+"/"+tc.comparison, func(t *testing.T) {
			out, err := svc.Analyze(ctx, AnalyzeKlineInput{FileID: id, StartDate: "2030-01-06", EndDate: "2030-01-25", Metric: tc.metric, Comparison: tc.comparison, ThresholdPct: &tc.threshold})
			if err != nil {
				t.Fatal(err)
			}
			if out.EligibleCount != 20 || out.BarCount != 20 || out.ExcludedCount != 0 || out.MatchedCount != tc.matches || len(out.Chart.Rows) != 20 || out.FirstDate != "2030-01-06" || out.LastDate != "2030-01-25" || out.CoverageVerified || out.AdjustmentStatus != "unverified" {
				t.Fatalf("synthetic interval analysis: %+v", out)
			}
			if out.MatchRatePct == nil || *out.MatchRatePct != float64(tc.matches)*5 {
				t.Fatalf("wrong denominator or rate: %+v", out.MatchRatePct)
			}
			if tc.matches > 0 && out.Matches.Rows[0][0] != tc.firstMatch {
				t.Fatalf("threshold boundary moved: %+v", out.Matches.Rows)
			}
			if tc.firstMatch == "2030-01-06" && out.Matches.Rows[0][1] != "2030-01-05" {
				t.Fatal("preceding close outside the selected interval was lost")
			}
		})
	}
}
