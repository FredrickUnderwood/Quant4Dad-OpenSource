package application

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

type klineFileBars struct {
	repository.BarRepository
	bars []*domain.Bar
}

func (r klineFileBars) RangeLatest(context.Context, string, domain.BarPeriod, time.Time, time.Time, int) ([]*domain.Bar, error) {
	return r.bars, nil
}

func TestAgentKlineCatalogReturnsFileThenBoundedPages(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(directory, "tmp", "kline"))
	if err != nil {
		t.Fatal(err)
	}
	bars := klineFileBars{}
	for i := 0; i < 493; i++ {
		bars.bars = append(bars.bars, &domain.Bar{Code: "sh.600809", Period: domain.Bar1d, Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC), Close: float64(i)})
	}
	catalog := NewAgentP0Catalog(service.NewInstrumentService(nil, bars), nil, nil, nil, nil, nil, nil, nil, service.NewAgentKlineFileService(files))
	ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "session"}, domain.AgentApprovalReceipt{})
	body, err := catalog.Invoke(ctx, "research", "query_kline", []byte(`{"code":"sh.600809","limit":500}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if sonic.Unmarshal(body, &result) != nil {
		t.Fatal("invalid result")
	}
	if result.Data["bars"] != nil || result.Data["count"] != float64(493) || len(body) > 1500 {
		t.Fatal("raw K-lines escaped into initial result")
	}
	fileID := result.Data["file_id"]
	raw, _ := sonic.Marshal(map[string]any{"file_id": fileID, "offset": 450, "limit": 100})
	page, err := catalog.Invoke(ctx, "research", "read_kline_file", raw)
	if err != nil {
		t.Fatal(err)
	}
	if sonic.Unmarshal(page, &result) != nil || result.Data["count"] != float64(43) || result.Data["has_more"] != false || result.Data["next_offset"] != float64(493) {
		t.Fatalf("bad last page: %s", page)
	}
	if _, err := catalog.Invoke(ctx, "external", "read_kline_file", raw); err == nil {
		t.Fatal("temporary files exposed to external MCP")
	}
	for _, profile := range []string{"research", "strategy_lab"} {
		found := false
		for _, d := range catalog.Definitions(profile) {
			if d.Name == "read_kline_file" {
				found = true
			}
		}
		if !found {
			t.Fatal("missing file reader in profile", profile)
		}
	}
}

func TestAnalyzeKlineCatalogBoundedSchemaAndInternalProfile(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo, err := repository.NewAgentToolArtifactRepository(filepath.Join(directory, "kline"))
	if err != nil {
		t.Fatal(err)
	}
	files := service.NewAgentKlineFileService(repo)
	ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "audit", ActorID: "owner", SessionID: "session"}, domain.AgentApprovalReceipt{})
	data := service.QueryKlineResult{Code: "sh.600000", Period: "1d", Count: 500, Bars: []*domain.Bar{}}
	for i := 0; i < 500; i++ {
		data.Bars = append(data.Bars, &domain.Bar{Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC), Open: 100.1234, Close: 100.1234, High: 101.1234, Low: 99.1234, Volume: 12345678})
	}
	descriptor, err := files.Save(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	catalog := NewAgentP0Catalog(service.NewInstrumentService(nil, klineFileBars{}), nil, nil, nil, nil, nil, nil, nil, files)
	args, _ := sonic.Marshal(map[string]any{"file_id": descriptor.FileID, "threshold_pct": -100})
	body, err := catalog.Invoke(ctx, "research", "analyze_kline", args)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if sonic.Unmarshal(body, &decoded) != nil {
		t.Fatal("invalid result")
	}
	definition := catalog.tools["analyze_kline"]
	if !matchesToolSchema(definition.OutputSchema, decoded) {
		t.Fatal("analysis violates declared schema")
	}
	var result struct {
		Data service.KlineAnalysis `json:"data"`
	}
	if sonic.Unmarshal(body, &result) != nil || result.Data.BarCount != 500 || result.Data.MatchedCount != 499 || len(body) > ToolMaxResultBytes {
		t.Fatalf("unbounded or incomplete result: %d", len(body))
	}
	if _, err := catalog.Invoke(ctx, "external", "analyze_kline", args); err == nil {
		t.Fatal("analysis exposed to external profile")
	}
	for _, raw := range []string{`{"file_id":"` + descriptor.FileID + `","comparison":"eq"}`, `{"file_id":"` + descriptor.FileID + `","threshold_pct":1001}`, `{"file_id":"` + descriptor.FileID + `","chart":{"html":"<script>"}}`} {
		if _, err := catalog.Invoke(ctx, "research", "analyze_kline", []byte(raw)); err == nil {
			t.Fatal("invalid analysis input accepted")
		}
	}
}
