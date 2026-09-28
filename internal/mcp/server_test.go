package mcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

const testToken = "fixture-external-mcp-token-0123456789"

func TestStdioNodeSchemasRemainOnePhysicalJSONLine(t *testing.T) {
	server, _ := mcpTestServer(t)
	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_pipeline_node_types","arguments":{}}}` + "\n"
	if err := server.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(out.Bytes(), []byte("\n")) != 1 || !sonic.Valid(bytes.TrimSpace(out.Bytes())) || !bytes.Contains(out.Bytes(), []byte("config_schema")) {
		t.Fatal("multiline schema broke stdio framing")
	}
}

func mcpTestServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "mcp.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn, _ := db.DB(); conn.Close() })
	if err := db.AutoMigrate(&domain.Instrument{}, &domain.Bar{}, &domain.Pipeline{}, &domain.PipelineNode{}, &domain.PipelineEdge{}, &domain.NewsSubscription{}, &domain.Event{}); err != nil {
		t.Fatal(err)
	}
	bars := make([]*domain.Bar, 600)
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range bars {
		bars[i] = &domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Close: float64(i)}
	}
	repo := repository.NewGormBarRepository(db)
	if err := repo.Upsert(context.Background(), bars); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.Pipeline{Name: "read-only", Status: "draft"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"passed", "failed"} {
		if err := db.Create(&domain.Event{EventUID: status, PipelineID: 1, Status: status, FinalPayload: []byte(`{"text":"untrusted event content"}`), ReceivedAt: start}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := NewServer("fixture", "test")
	RegisterKlineTools(s, service.NewInstrumentService(repository.NewInstrumentRepository(db), repo))
	RegisterEventTools(s, service.NewEventQueryService(repository.NewEventRepository(db)))
	RegisterPipelineTools(s, service.NewPipelineService(repository.NewPipelineRepository(db), nodes.BuildRegistry(nil, nil)))
	return s, db
}
func call(t *testing.T, s *Server, name string, args string) map[string]any {
	t.Helper()
	req, err := parseRequest([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	response := s.handle(context.Background(), req)
	raw := encodeResponse(response)
	var value map[string]any
	if sonic.Unmarshal(raw, &value) != nil {
		t.Fatal("invalid response")
	}
	return value
}
func data(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	result, ok := value["result"].(map[string]any)
	if !ok || result["isError"] != false {
		t.Fatalf("unexpected failure: %v", value)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["untrusted_data"] != true {
		t.Fatal("data lost untrusted marker")
	}
	return structured["data"].(map[string]any)
}
func TestExternalMCPCatalogAndBoundedBusinessQueries(t *testing.T) {
	s, db := mcpTestServer(t)
	tools := s.listTools()
	if len(s.catalog.Definitions("research")) != 5 {
		t.Fatal("partial research fixture must contain four market tools and the event list")
	}
	if len(tools) != 9 {
		t.Fatalf("unexpected tools: %d", len(tools))
	}
	for _, tool := range tools {
		if tool["name"] == "create_pipeline" || tool["name"] == "update_pipeline" {
			t.Fatal("write tool advertised")
		}
		if tool["outputSchema"] == nil || tool["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatal("missing schema/read-only policy")
		}
	}
	value := data(t, call(t, s, "query_kline", `{"code":"sh.600519"}`))
	if value["count"] != float64(120) || value["truncated"] != true {
		t.Fatal(value)
	}
	bars := value["bars"].([]any)
	if bars[0].(map[string]any)["close"] != float64(480) || bars[119].(map[string]any)["close"] != float64(599) {
		t.Fatal("not latest ascending bars")
	}
	value = data(t, call(t, s, "query_kline", `{"code":"sh.600519","limit":500}`))
	if value["count"] != float64(500) {
		t.Fatal(value)
	}
	latest := data(t, call(t, s, "latest_bar_date", `{"code":"sh.600519"}`))
	if latest["latest_bar"].(map[string]any)["close"] != float64(599) {
		t.Fatal(latest)
	}
	empty := data(t, call(t, s, "query_kline", `{"code":"sz.000001"}`))
	if empty["count"] != float64(0) || len(empty["bars"].([]any)) != 0 {
		t.Fatal(empty)
	}
	if data(t, call(t, s, "list_events", `{"pipeline_id":1}`))["count"] != float64(2) || data(t, call(t, s, "list_passed_events", `{"pipeline_id":1}`))["count"] != float64(1) {
		t.Fatal("alias default status")
	}
	for _, name := range []string{"create_pipeline", "update_pipeline", "dry_run_pipeline_safe", "set_pipeline_status"} {
		if call(t, s, name, `{"id":1,"status":"enabled"}`)["error"] == nil {
			t.Fatal("write accepted", name)
		}
	}
	var pipeline domain.Pipeline
	if err := db.First(&pipeline, 1).Error; err != nil || pipeline.Status != "draft" {
		t.Fatal("write tool changed storage")
	}
}
func TestExternalMCPRejectsInvalidArgumentsBeforeStorage(t *testing.T) {
	s, _ := mcpTestServer(t)
	for _, args := range []string{`{"code":"../../secret"}`, `{"code":"sh.600519","period":"1m"}`, `{"code":"sh.600519","limit":0}`, `{"code":"sh.600519","limit":501}`, `{"code":"sh.600519","start":"2024-02-30"}`, `{"code":"sh.600519","start":"2024-02-03","end":"2024-02-01"}`, `{"code":"sh.600519","actor_id":"admin"}`, `{"Code":"sh.600519"}`} {
		result := call(t, s, "query_kline", args)["result"].(map[string]any)
		if result["isError"] != true || !strings.Contains(string(encodeResponse(&rpcResponse{JSONRPC: "2.0", Result: result})), "tool_invalid_arguments") {
			t.Fatal("input accepted", args)
		}
	}
	for name, args := range map[string]string{"list_instruments": `{"size":101}`, "list_events": `{"pipeline_id":1,"limit":0}`, "list_pipelines": `{"limit":0}`, "get_pipeline_node_types": `{"run_id":"forged"}`} {
		if call(t, s, name, args)["result"].(map[string]any)["isError"] != true {
			t.Fatal(name)
		}
	}
}
func TestExternalMCPHTTPAuthenticationAndStrictProtocol(t *testing.T) {
	s, _ := mcpTestServer(t)
	handler := s.HTTPHandler(testToken)
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	request := func(body, token string) *http.Request {
		r := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	for name, mutate := range map[string]func(*http.Request){
		"duplicate credential": func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+testToken) },
		"origin":               func(r *http.Request) { r.Header.Set("Origin", "https://untrusted.example") },
		"capability":           func(r *http.Request) { r.Header.Set("X-Q4D-Run-Capability", "forged") },
		"query":                func(r *http.Request) { r.URL.RawQuery = "token=hidden" },
	} {
		t.Run(name, func(t *testing.T) {
			r := request(valid, testToken)
			mutate(r)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code < 400 {
				t.Fatal("invalid auth/context accepted")
			}
		})
	}
	for _, token := range []string{"", "login-cookie-token", "fixture-agent-mcp-token"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request(valid, token))
		if w.Code != 401 {
			t.Fatal("unauthorized client")
		}
	}
	w := httptest.NewRecorder()
	s.HTTPHandler("").ServeHTTP(w, request(valid, ""))
	if w.Code != 401 {
		t.Fatal("empty token disabled auth")
	}
	for _, body := range []string{`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"tools/call"}`, valid + valid, `[]`, `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request(body, testToken))
		if w.Code != 400 {
			t.Fatal("ambiguous JSON accepted", body)
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request(strings.Repeat(" ", maxRequestBytes+1), testToken))
	if w.Code != 413 {
		t.Fatal("unbounded body", w.Code)
	}
	w = httptest.NewRecorder()
	r := request(valid, testToken)
	r.Header.Set("Accept", "text/event-stream")
	handler.ServeHTTP(w, r)
	if w.Code != 200 || !w.Flushed || !strings.Contains(w.Body.String(), "event: message") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("SSE reply failed")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/internal/mcp", nil))
	if w.Code != 404 {
		t.Fatal("unprotected internal route exists")
	}
}
func TestExternalMCPNotificationsAndOutputErrorsDoNotExecuteOrLeak(t *testing.T) {
	s := NewServer("test", "test")
	calls := 0
	definition := application.ToolDefinition{Name: "query", Description: "test", InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}, Risk: "R0", Profiles: []string{"external"}, TimeoutMS: 1000, MaxResult: 128,
		Handler: func(context.Context, []byte) (any, error) { calls++; return nil, errors.New("private-dsn-credential") }}
	s.Register(definition)
	var out bytes.Buffer
	input := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"query","arguments":{}}}` + "\n" + `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{}}}` + "\n"
	if err := s.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Contains(out.String(), "private-dsn") || strings.Count(out.String(), "\n") != 1 {
		t.Fatal("notification or diagnostics escaped")
	}
	s = NewServer("test", "test")
	definition.Handler = func(context.Context, []byte) (any, error) { return strings.Repeat("x", 256), nil }
	s.Register(definition)
	if !strings.Contains(string(encodeResponse(s.handle(context.Background(), &rpcRequest{JSONRPC: "2.0", ID: []byte("1"), Method: "tools/call", Params: []byte(`{"name":"query","arguments":{}}`)}))), "tool_result_too_large") {
		t.Fatal("unbounded output")
	}
}
