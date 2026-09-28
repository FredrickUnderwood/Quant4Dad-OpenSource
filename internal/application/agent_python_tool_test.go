package application

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pythonexec"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type catalogPythonExecutor struct{}

func (catalogPythonExecutor) Execute(context.Context, string, []byte) (pythonexec.Result, error) {
	return pythonexec.Result{Status: "succeeded", Stdout: `{"metrics":{"count":1},"table":{"columns":["date"],"rows":[["2025-01-03"]]}}`}, nil
}

func TestPythonCatalogPolicyAndSchema(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := repository.NewAgentToolArtifactRepository(filepath.Join(dir, "kline"))
	if err != nil {
		t.Fatal(err)
	}
	files := service.NewAgentKlineFileService(repo, catalogPythonExecutor{})
	ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "session"}, domain.AgentApprovalReceipt{})
	descriptor, err := files.Save(ctx, service.QueryKlineResult{Code: "sh.510300", Period: "1d", Count: 1, Bars: []*domain.Bar{{Date: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Open: 1, High: 1, Low: 1, Close: 1, Volume: 100}}})
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewAgentP0Catalog(service.NewInstrumentService(nil, nil), nil, nil, nil, nil, nil, nil, nil, files)
	args, _ := sonic.Marshal(map[string]any{"file_ids": []string{descriptor.FileID}, "code": "print('{}')", "chart_period": "1w"})
	for _, profile := range []string{"research", "strategy_lab"} {
		body, err := catalog.Invoke(ctx, profile, "execute_python", args)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if sonic.Unmarshal(body, &decoded) != nil || !matchesToolSchema(catalog.tools["execute_python"].OutputSchema, decoded) {
			t.Fatalf("schema mismatch: %s", body)
		}
	}
	// Provider provenance must pass every internal/external market schema too.
	bar := domain.Bar{Code: "sh.510300", Period: domain.Bar1d, Date: time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC), Open: 1, High: 1, Low: 1, Close: 1, AdjFactor: 1, AdjSource: domain.AdjFactorTushareFund}
	encoded, _ := sonic.Marshal(bar)
	var barValue any
	_ = sonic.Unmarshal(encoded, &barValue)
	if !matchesToolSchema(barSchema(), barValue) {
		t.Fatal("ETF factor provenance rejected by market tools")
	}
	adjustedFile, err := files.Save(ctx, service.QueryKlineResult{Code: bar.Code, Period: "1d", Count: 1, Bars: []*domain.Bar{&bar}})
	if err != nil {
		t.Fatal(err)
	}
	adjustedArgs, _ := sonic.Marshal(map[string]any{"file_ids": []string{adjustedFile.FileID}, "code": "print('{}')", "adjustment": "qfq", "end_date": "2025-01-03", "chart_period": "1d"})
	if _, err := catalog.Invoke(ctx, "research", "execute_python", adjustedArgs); err != nil {
		t.Fatal("qfq catalog contract rejected", err)
	}
	for _, profile := range []string{"external", "pipeline_builder"} {
		if _, err := catalog.Invoke(ctx, profile, "execute_python", args); err == nil {
			t.Fatal("Python exposed outside internal research scope")
		}
	}
	for _, bad := range []string{`{"file_ids":[],"code":"x"}`, `{"file_ids":["../secret"],"code":"x"}`, `{"file_ids":["` + descriptor.FileID + `"],"code":"x","path":"/etc/passwd"}`} {
		if _, err := catalog.Invoke(ctx, "research", "execute_python", []byte(bad)); err == nil {
			t.Fatal("unbounded argument accepted")
		}
	}
	unavailable := NewAgentP0Catalog(service.NewInstrumentService(nil, nil), nil, nil, nil, nil, nil, nil, nil, service.NewAgentKlineFileService(repo))
	if _, ok := unavailable.tools["execute_python"]; ok {
		t.Fatal("unavailable Python advertised")
	}
}
