package service

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pythonexec"
)

type pythonTestExecutor struct {
	calls  int
	input  []byte
	output string
}

func TestPythonChartContextUsesCanonicalHolidayAndIncompleteWeekDates(t *testing.T) {
	s, ctx := analysisFixture(t)
	executor := &pythonTestExecutor{output: `{}`}
	s.python = executor
	// Friday is absent in the first week; the second week is unfinished.
	id := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: []*domain.Bar{
		analysisBar("2025-01-02", 10, 10), analysisBar("2025-01-06", 11, 11), analysisBar("2025-01-09", 12, 12),
	}})
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{id}, Code: "print('{}')", ChartPeriod: "1w", StartDate: "2025-01-01", EndDate: "2025-01-09"})
	if err != nil || out.Chart == nil {
		t.Fatal(err, out.Warnings)
	}
	var input struct {
		ChartContext struct {
			Available        bool     `json:"available"`
			Dates            []string `json:"dates"`
			AdjustmentStatus string   `json:"adjustment_status"`
		} `json:"chart_context"`
	}
	if err := json.Unmarshal(executor.input, &input); err != nil {
		t.Fatal(err)
	}
	if !input.ChartContext.Available || !reflect.DeepEqual(input.ChartContext.Dates, []string{"2025-01-03"}) || input.ChartContext.AdjustmentStatus != "unverified" {
		t.Fatalf("script cannot align to the renderer: %+v", input.ChartContext)
	}
	if out.Chart.Rows[0][0] != input.ChartContext.Dates[0] {
		t.Fatal("axis differs from script input")
	}
}

func TestPythonSMARejectsWindowSumsAndKeepsPrewarm(t *testing.T) {
	s, ctx := analysisFixture(t)
	bars := []*domain.Bar{}
	for i := 1; i <= 6; i++ {
		bars = append(bars, analysisBar(time.Date(2025, 1, i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), float64(i), float64(i)))
	}
	id := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars})
	executor := &pythonTestExecutor{}
	s.python = executor
	for _, tc := range []struct {
		name, output, start string
		valid               bool
	}{
		{"window sums", `{"chart":{"lines":[{"name":"MA3","points":[["2025-01-05",12],["2025-01-06",15]]}]}}`, "2025-01-05", false},
		{"shifted mean", `{"chart":{"lines":[{"name":"MA3","points":[["2025-01-05",3]]}]}}`, "2025-01-05", false},
		{"insufficient prewarm", `{"chart":{"lines":[{"name":"MA3","points":[["2025-01-02",1.5]]}]}}`, "2025-01-02", false},
		{"mean from prewarm", `{"chart":{"lines":[{"name":"MA3","points":[["2025-01-05",4],["2025-01-06",5]]}]}}`, "2025-01-05", true},
		{"explicit SMA name", `{"chart":{"lines":[{"name":"SMA_3","points":[["2025-01-05",4]]}]}}`, "2025-01-05", true},
		{"custom indicator remains unverified", `{"chart":{"lines":[{"name":"custom","points":[["2025-01-05",12]]}]}}`, "2025-01-05", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor.output = tc.output
			out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{id}, Code: "print('{}')", StartDate: tc.start, ChartPeriod: "1d"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid {
				if out.Status != "succeeded" || out.Chart == nil || out.Chart.Rows[0][0] != tc.start {
					t.Fatal(out.Status, out.Warnings)
				}
			} else if out.Status != "invalid_output" || out.Chart != nil || len(out.SourceSHA256) != 64 || !strings.Contains(strings.Join(out.Warnings, " "), "SMA") {
				t.Fatal("invalid chart was accepted or execution evidence lost", out.Status, out.Warnings)
			}
		})
	}
}

func TestPythonOversizeArtifactKeepsActionableExecutionRecord(t *testing.T) {
	s, ctx := analysisFixture(t)
	s.python = &pythonTestExecutor{output: `{"detail":"` + strings.Repeat("x", 64000) + `"}`}
	bars := []*domain.Bar{}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 600; i++ {
		b := analysisBar(start.AddDate(0, 0, i).Format("2006-01-02"), 1234567890123.123, 1234567890123.123)
		b.Volume = 1234567890123.123
		bars = append(bars, b)
	}
	left := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[:300]})
	right := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[300:]})
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{left, right}, Code: strings.Repeat("x", 16384), ChartPeriod: "1d"})
	if err != nil || out.Status != "output_limit" || out.Chart != nil || len(out.Result) != 0 || !strings.Contains(strings.Join(out.Warnings, " "), "128KiB") {
		t.Fatal(out.Status, out.Warnings, err)
	}
	body, _ := json.Marshal(out)
	if len(body) >= 128<<10 || len(out.SourceSHA256) != 64 || out.InputCount != 600 {
		t.Fatal("execution evidence lost")
	}
}

func TestPythonGroupedMarkersValidation(t *testing.T) {
	bars := []*domain.Bar{analysisBar("2025-01-02", 100, 100), analysisBar("2025-01-03", 90, 90)}
	for _, body := range []string{
		`{"lines":[{"name":"MA2","points":[["2025-01-03",95]]}],"marker_groups":[{"name":"下穿 MA2","line_name":"MA2","dates":["2025-01-03"]},{"name":"未命中","dates":[]}]}`,
		`{"markers":["2025-01-03"]}`,
	} {
		var overlays any
		if err := json.Unmarshal([]byte(body), &overlays); err != nil {
			t.Fatal(err)
		}
		chart, err := pythonChart("sh.588000", bars, PythonAnalysisInput{ChartPeriod: "1d"}, map[string]any{"chart": overlays})
		if err != nil || chart == nil || len(chart.Rows) != 2 {
			t.Fatal(chart, err)
		}
	}
	for _, bad := range []string{
		`{"marker_groups":[{"name":"hit","dates":["2025-01-04"]}]}`,
		`{"marker_groups":[{"name":"hit","dates":["2025-01-03","2025-01-03"]}]}`,
		`{"marker_groups":[{"name":"hit","line_name":"missing","dates":[]}]}`,
		`{"marker_groups":[{"name":"hit","dates":[]},{"name":"hit","dates":[]}]}`,
		`{"markers":["2025-01-03"],"marker_groups":[{"name":"hit","dates":[]}]}`,
		`{"lines":[{"name":"hit","points":[]}],"marker_groups":[{"name":"hit","dates":[]}]}`,
	} {
		var overlays any
		if err := json.Unmarshal([]byte(bad), &overlays); err != nil {
			t.Fatal(err)
		}
		if _, err := pythonChart("sh.588000", bars, PythonAnalysisInput{ChartPeriod: "1d"}, map[string]any{"chart": overlays}); err == nil {
			t.Fatalf("accepted invalid groups: %s", bad)
		}
	}
}

func TestPythonActualFourDailyMACrosses(t *testing.T) {
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
	bars, prices := []*domain.Bar{}, []int{}
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 660; i++ {
		price := []int{100, 105, 95}[i/30%3]
		prices = append(prices, price)
		bars = append(bars, analysisBar(start.AddDate(0, 0, i).Format("2006-01-02"), float64(price), float64(price)))
	}
	left := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[:330]})
	right := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[330:]})
	source, err := os.ReadFile("../../examples/research/daily_ma_cross.py")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{right, left}, Code: string(source), ChartPeriod: "1d", StartDate: bars[60].Date.Format("2006-01-02")})
	if err != nil || out.Status != "succeeded" || out.Chart == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if len(out.Chart.Rows) != 600 || len(out.Chart.Lines) != 4 || len(out.Chart.MarkerGroups) != 4 || len(out.Chart.Markers) != 0 {
		t.Fatal("missing chart categories")
	}
	// Independent integer prefix sums verify every event, including exact ties.
	prefix := []int{0}
	for _, price := range prices {
		prefix = append(prefix, prefix[len(prefix)-1]+price)
	}
	for j, n := range []int{5, 10, 20, 60} {
		want := []string{}
		for i := 60; i < len(prices); i++ {
			if prices[i-1]*n >= prefix[i]-prefix[i-n] && prices[i]*n < prefix[i+1]-prefix[i+1-n] {
				want = append(want, bars[i].Date.Format("2006-01-02"))
			}
		}
		if !reflect.DeepEqual(out.Chart.MarkerGroups[j].Dates, want) || len(out.Chart.Lines[j].Points) != 600 {
			t.Fatal(n, out.Chart.MarkerGroups[j].Dates, want)
		}
	}
	body, err := json.Marshal(out)
	if err != nil || len(body) <= 32<<10 || len(body) >= 128<<10 {
		t.Fatal("artifact size regression", len(body), err)
	}
}

func (e *pythonTestExecutor) Execute(_ context.Context, _ string, input []byte) (pythonexec.Result, error) {
	e.calls++
	e.input = input
	return pythonexec.Result{Status: "succeeded", Stdout: e.output}, nil
}

func TestPythonOwnedInputAndOutputValidation(t *testing.T) {
	s, ctx := analysisFixture(t)
	executor := &pythonTestExecutor{output: `{"metrics":{"count":1}}`}
	s.python = executor
	id := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: []*domain.Bar{analysisBar("2025-01-02", 100, 100)}})
	in := PythonAnalysisInput{FileIDs: []string{id}, Code: "print('{}')"}
	for _, other := range []context.Context{context.Background(), WithAgentExecution(ctx, domain.AgentToolAudit{ID: "audit-other", ActorID: "foreign", SessionID: "session"}, domain.AgentApprovalReceipt{}), WithAgentExecution(ctx, domain.AgentToolAudit{ID: "audit-other", ActorID: "owner", SessionID: "foreign"}, domain.AgentApprovalReceipt{})} {
		if _, err := s.ExecutePython(other, in); err == nil {
			t.Fatal("foreign snapshot accepted")
		}
	}
	if executor.calls != 0 {
		t.Fatal("unowned data reached VM")
	}
	out, err := s.ExecutePython(ctx, in)
	if err != nil || out.Status != "succeeded" || out.InputCount != 1 || len(out.InputSHA256) != 64 || len(out.SourceSHA256) != 64 {
		t.Fatalf("%+v %v", out, err)
	}
	var payload map[string]any
	if json.Unmarshal(executor.input, &payload) != nil || payload["coverage_verified"] != false {
		t.Fatal("missing input provenance")
	}
	for _, bad := range []string{`[]`, `null`, `{"x":1,"x":2}`, `{"x":NaN}`, `{} {}`, `ordinary text`} {
		executor.output = bad
		got, err := s.ExecutePython(ctx, in)
		if err != nil || got.Status != "invalid_output" {
			t.Fatalf("accepted invalid output %s: %+v %v", bad, got, err)
		}
	}
	for _, bad := range []PythonAnalysisInput{{FileIDs: []string{id, id}, Code: "x"}, {FileIDs: []string{"../../etc/passwd"}, Code: "x"}, {FileIDs: []string{id}, Code: "x", StartDate: "2025-02-30"}} {
		if _, err := s.ExecutePython(ctx, bad); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestPythonRejectsConflictingSnapshotsBeforeExecution(t *testing.T) {
	s, ctx := analysisFixture(t)
	executor := &pythonTestExecutor{}
	s.python = executor
	a := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: []*domain.Bar{analysisBar("2025-01-02", 100, 100)}})
	b := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: []*domain.Bar{analysisBar("2025-01-02", 101, 101)}})
	if _, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{a, b}, Code: "x"}); err == nil || executor.calls != 0 {
		t.Fatal("conflicting inputs accepted")
	}
}

func TestPythonActualWeeklyMA60CrossesMultipleWindows(t *testing.T) {
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
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := []*domain.Bar{}
	for week := 0; week < 80; week++ {
		price := 100.0
		if week == 60 || week >= 62 {
			price = 80
		}
		if week == 61 {
			price = 130
		}
		for day := 0; day < 5; day++ {
			bars = append(bars, analysisBar(start.AddDate(0, 0, week*7+day).Format("2006-01-02"), price, price))
		}
	}
	left := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[:300]})
	right := saveAnalysis(t, s, ctx, QueryKlineResult{Bars: bars[280:]}) // overlap must not duplicate weeks
	source, err := os.ReadFile("../../examples/research/weekly_ma_cross.py")
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{right, left}, Code: string(source), ChartPeriod: "1w"})
	if err != nil || out.Status != "succeeded" {
		t.Fatalf("%+v %v", out, err)
	}
	metrics := out.Result["metrics"].(map[string]any)
	if metrics["count"] != float64(2) || metrics["eligible_weeks"] != float64(20) || metrics["warmup_excluded_in_range"] != float64(60) || out.InputCount != 400 {
		t.Fatal(metrics, out.InputCount)
	}
	if out.Chart == nil || len(out.Chart.Rows) != 80 || len(out.Chart.Markers) != 2 || out.Chart.Markers[0] != start.AddDate(0, 0, 60*7+4).Format("2006-01-02") || out.Chart.Markers[1] != start.AddDate(0, 0, 62*7+4).Format("2006-01-02") {
		t.Fatalf("incorrect chart: %+v", out.Chart)
	}
	// An unfinished week at the explicit Thursday cutoff is excluded in both
	// Python and the canonical chart, with earlier rows retained for warmup.
	cutoff := start.AddDate(0, 0, 62*7+3).Format("2006-01-02")
	out, err = s.ExecutePython(ctx, PythonAnalysisInput{FileIDs: []string{left, right}, Code: string(source), StartDate: start.AddDate(0, 0, 60*7).Format("2006-01-02"), EndDate: cutoff, ChartPeriod: "1w"})
	if err != nil || out.Status != "succeeded" || out.Result["metrics"].(map[string]any)["count"] != float64(1) || out.Chart == nil || len(out.Chart.Rows) != 2 {
		t.Fatalf("unfinished week or warmup: %+v %v", out, err)
	}
}
