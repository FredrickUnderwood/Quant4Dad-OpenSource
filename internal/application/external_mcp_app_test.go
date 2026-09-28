package application

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"gorm.io/gorm"
)

func externalMCPFixture(t *testing.T, budget int64) (*ExternalMCPApplication, *gorm.DB, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(root, "external.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	artifacts, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "results"))
	if err != nil {
		t.Fatal(err)
	}
	checks, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "kline"))
	if err != nil {
		t.Fatal(err)
	}
	market := service.NewInstrumentService(repository.NewInstrumentRepository(db), repository.NewGormBarRepository(db))
	strategy := service.NewStrategyService(repository.NewStrategyRepository(db), checks)
	pipeline := service.NewPipelineService(repository.NewPipelineRepository(db), nodes.BuildRegistry(nil, nil))
	mutations := service.NewAgentMutationService(repository.NewAgentMutationRepository(db), strategy, pipeline)
	cost := service.NewCostService(repository.NewCostRepository(db))
	if err := cost.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog := NewAgentP0Catalog(market, service.NewIndicatorService(), strategy, pipeline, service.NewEventQueryService(repository.NewEventRepository(db)), service.NewAgentCatalogQueryService(repository.NewAgentCatalogQueryRepository(db)), mutations, cost, service.NewAgentKlineFileService(files, catalogPythonExecutor{}))
	signer, err := agentrunauth.NewSigner("fixture", "fixture", ed25519.NewKeyFromSeed(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	audit := service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
	approval := service.NewAgentApprovalService(repository.NewAgentApprovalRepository(db), signer)
	sessions, err := service.NewExternalMCPSessionService(repository.NewExternalMCPSessionRepository(db), time.Hour, budget)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewExternalMCPApplication(catalog, audit, approval, sessions, true)
	if err != nil {
		t.Fatal(err)
	}
	return app, db, filepath.Join(root, "results")
}

func externalSession(t *testing.T, app *ExternalMCPApplication) string {
	t.Helper()
	session, err := app.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.AuthorizationMode != "token_authorized" {
		t.Fatal("authority mode missing", session)
	}
	return session.ID
}

func externalCall(t *testing.T, app *ExternalMCPApplication, session, rpc, name, args string) []byte {
	t.Helper()
	body, code, err := app.Call(context.Background(), session, []byte(rpc), name, []byte(args), nil)
	if err != nil || code != "" {
		t.Fatalf("%s: %s %v", name, code, err)
	}
	return body
}

func TestExternalMCPCompleteCatalogAndFileIsolation(t *testing.T) {
	app, db, _ := externalMCPFixture(t, 100)
	if len(app.Definitions()) != 31 {
		t.Fatalf("incomplete catalog: %d", len(app.Definitions()))
	}
	names := []string{}
	for _, profile := range []string{"research", "strategy_lab", "pipeline_builder"} {
		for _, name := range P0ProfileTools(profile) {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	for _, definition := range app.Definitions() {
		if !slices.Contains(names, definition.Name) {
			t.Fatal("unexpected tool", definition.Name)
		}
	}
	if err := db.Create(&domain.Instrument{Code: "sh.600519", Name: "fixture", AssetType: domain.AssetStock}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.Bar{Code: "sh.600519", Period: domain.Bar1d, Date: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Open: 10, High: 11, Low: 9, Close: 11}).Error; err != nil {
		t.Fatal(err)
	}
	a, b := externalSession(t, app), externalSession(t, app)
	query := externalCall(t, app, a, `1`, "query_kline", `{"code":"sh.600519"}`)
	var descriptor struct {
		Data service.KlineFileDescriptor `json:"data"`
	}
	if sonic.Unmarshal(query, &descriptor) != nil || descriptor.Data.Count != 1 || descriptor.Data.FileID == "" {
		t.Fatal(string(query))
	}
	args, _ := sonic.MarshalString(map[string]any{"file_id": descriptor.Data.FileID})
	page := externalCall(t, app, a, `2`, "read_kline_file", args)
	if !bytes.Contains(page, []byte(`"rows"`)) {
		t.Fatal(string(page))
	}
	if _, code, err := app.Call(context.Background(), b, []byte(`2`), "read_kline_file", []byte(args), nil); err != nil || code != "tool_unavailable" {
		t.Fatalf("cross-session file accepted: %s %v", code, err)
	}
	replayed := externalCall(t, app, a, `1`, "query_kline", `{"code":"sh.600519"}`)
	if !bytes.Equal(query, replayed) {
		t.Fatal("retry changed query result")
	}
	if _, _, err := app.Call(context.Background(), a, []byte(`1`), "query_kline", []byte(`{"code":"sh.600519","limit":1}`), nil); !errors.Is(err, service.ErrAgentToolConflict) {
		t.Fatal("changed RPC arguments accepted", err)
	}
	externalCall(t, app, a, `"1"`, "list_instruments", `{}`)
	if _, _, err := app.Call(context.Background(), a, []byte(`3`), "list_instruments", []byte(`{"actor_id":"local-user"}`), nil); !errors.Is(err, service.ErrToolInput) {
		t.Fatal("client supplied actor accepted", err)
	}
}

func TestExternalMCPTokenMutationReplayAndCrashRecovery(t *testing.T) {
	app, db, artifacts := externalMCPFixture(t, 100)
	session := externalSession(t, app)
	input := `{"strategy":{"name":"external-once","universe":["sh.600519"],"period":"1d","body":{"indicators":[],"rules":[],"execution":{"fill_at":"close"}}}}`
	checked := externalCall(t, app, session, `"validate"`, "validate_strategy", input)
	var report struct {
		Data service.StrategyCheckReport `json:"data"`
	}
	if sonic.Unmarshal(checked, &report) != nil || !report.Data.Valid || report.Data.ValidationID == "" {
		t.Fatal(string(checked))
	}
	var args map[string]any
	_ = sonic.UnmarshalString(input, &args)
	args["validation_id"] = report.Data.ValidationID
	raw, _ := sonic.Marshal(args)
	if err := os.Chmod(artifacts, 0500); err != nil {
		t.Fatal(err)
	}
	_, _, writeErr := app.Call(context.Background(), session, []byte(`"save"`), "create_strategy", raw, nil)
	if err := os.Chmod(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(writeErr, service.ErrAgentToolStore) {
		t.Fatal("result-store failure did not occur", writeErr)
	}
	result := externalCall(t, app, session, `"save"`, "create_strategy", string(raw))
	if !bytes.Contains(result, []byte(`"version":1`)) {
		t.Fatal(string(result))
	}
	externalCall(t, app, session, `"save"`, "create_strategy", string(raw))
	var count int64
	db.Model(&domain.Strategy{}).Where("name = ?", "external-once").Count(&count)
	if count != 1 {
		t.Fatal("replayed write duplicated", count)
	}
	var approval domain.AgentApprovalReceipt
	if err := db.Where("tool_name = ?", "create_strategy").First(&approval).Error; err != nil || approval.ActorID != domain.ExternalMCPActor || approval.Status != "consumed" {
		t.Fatal("external authorization receipt not consumed", approval, err)
	}
	var run domain.AgentRequestBinding
	if err := db.First(&run, "id = ?", approval.RunID).Error; err != nil || run.SigningKeyID != domain.ExternalMCPSigningKey || !strings.Contains(run.ClaimsJSON, `"authorization_mode":"token_authorized"`) {
		t.Fatal("authorization provenance missing", err)
	}
	other := externalSession(t, app)
	if _, code, err := app.Call(context.Background(), other, []byte(`"save"`), "create_strategy", raw, nil); err != nil || code != "strategy_validation_required" {
		t.Fatalf("cross-session validation accepted: %s %v", code, err)
	}
	if _, _, err := app.Call(context.Background(), session, []byte(`"save"`), "create_strategy", bytes.Replace(raw, []byte("external-once"), []byte("external-changed"), 1), nil); !errors.Is(err, service.ErrAgentToolConflict) {
		t.Fatal("changed write replay accepted", err)
	}
	var created struct {
		Data domain.AgentMutationResult `json:"data"`
	}
	if err := sonic.Unmarshal(result, &created); err != nil {
		t.Fatal(err)
	}
	backtestArgs, _ := sonic.MarshalString(map[string]any{"strategy_id": created.Data.ID, "expected_version": 1, "initial_capital": 10000, "start_date": "2026-09-01", "end_date": "2026-09-28"})
	queued := externalCall(t, app, session, `"backtest"`, "run_backtest", backtestArgs)
	if !bytes.Contains(queued, []byte(`"job_id"`)) {
		t.Fatal(string(queued))
	}
	externalCall(t, app, session, `"backtest"`, "run_backtest", backtestArgs)
	db.Model(&domain.BacktestJob{}).Count(&count)
	if count != 1 {
		t.Fatal("backtest retry duplicated queue", count)
	}
}

func TestExternalMCPSessionBudgetRevocationExpiryAndRuntimeIsolation(t *testing.T) {
	app, db, _ := externalMCPFixture(t, 2)
	session := externalSession(t, app)
	externalCall(t, app, session, `1`, "list_instruments", `{}`)
	externalCall(t, app, session, `2`, "list_instruments", `{}`)
	if _, _, err := app.Call(context.Background(), session, []byte(`3`), "list_instruments", []byte(`{}`), nil); !errors.Is(err, service.ErrAgentToolBudget) {
		t.Fatal("budget exceeded", err)
	}
	externalCall(t, app, session, `1`, "list_instruments", `{}`) // replay does not consume budget
	if err := app.CloseSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Session(context.Background(), session); !errors.Is(err, service.ErrExternalMCPSession) {
		t.Fatal("revoked session accepted", err)
	}
	expired := externalSession(t, app)
	if err := db.Model(&domain.AgentRequestBinding{}).Where("session_id = ?", expired).Update("deadline_ms", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Session(context.Background(), expired); !errors.Is(err, service.ErrExternalMCPSession) {
		t.Fatal("expired session accepted", err)
	}
	tampered := externalSession(t, app)
	var row domain.AgentRequestBinding
	if err := db.Where("session_id = ?", tampered).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&domain.AgentRequestBinding{}).Where("id = ?", row.ID).Update("claims_json", strings.Replace(row.ClaimsJSON, `"external_mcp"`, `"research"`, 1)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Session(context.Background(), tampered); !errors.Is(err, service.ErrExternalMCPSession) {
		t.Fatal("tampered source accepted", err)
	}
	if err := db.Create(&domain.AgentRequestBinding{ID: "internal-run", SessionID: "internal-session", ActorID: "local-user", ClientRequestKey: "internal", SigningKeyID: "agent-signing"}).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := repository.NewAgentRunRepository(db).Scan(context.Background(), "")
	if err != nil || len(rows) != 1 || rows[0].ID != "internal-run" {
		t.Fatal("external sessions entered Runtime reconciliation", rows, err)
	}
}

func TestExternalMCPPipelineMutationPendingRetryAndR3Status(t *testing.T) {
	app, db, _ := externalMCPFixture(t, 100)
	session := externalSession(t, app)
	args := `{"pipeline":{"name":"external-pipeline","nodes":[{"node_key":"filter","type":"keyword_filter","config":{"keywords":["fixture"],"list_type":"whitelist"}}],"edges":[],"sources":[]}}`
	entered, release := make(chan struct{}), make(chan struct{})
	type outcome struct {
		body []byte
		code string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		body, code, err := app.Call(context.Background(), session, []byte(`"create"`), "create_pipeline", []byte(args), func() error { close(entered); <-release; return nil })
		done <- outcome{body, code, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("mutation did not start")
	}
	_, _, retryErr := app.Call(context.Background(), session, []byte(`"create"`), "create_pipeline", []byte(args), nil)
	close(release)
	if !errors.Is(retryErr, service.ErrAgentToolPending) {
		t.Fatal("pending mutation retry admitted", retryErr)
	}
	result := <-done
	if result.err != nil || result.code != "" {
		t.Fatal(result.code, result.err)
	}
	externalCall(t, app, session, `"create"`, "create_pipeline", args)
	var created struct {
		Data domain.AgentMutationResult `json:"data"`
	}
	if sonic.Unmarshal(result.body, &created) != nil || created.Data.Status != "draft" {
		t.Fatal(string(result.body))
	}
	statusArgs, _ := sonic.MarshalString(map[string]any{"id": created.Data.ID, "expected_version": 1, "status": "enabled"})
	enabled := externalCall(t, app, session, `"enable"`, "set_pipeline_status", statusArgs)
	if !bytes.Contains(enabled, []byte(`"status":"enabled"`)) {
		t.Fatal(string(enabled))
	}
	var audit domain.AgentToolAudit
	if err := db.Where("tool_name = ?", "set_pipeline_status").First(&audit).Error; err != nil || audit.Risk != "R3" || audit.ActorID != domain.ExternalMCPActor {
		t.Fatal("R3 source/risk missing", audit, err)
	}
	var count int64
	db.Model(&domain.Pipeline{}).Where("name = ?", "external-pipeline").Count(&count)
	if count != 1 {
		t.Fatal("pipeline duplicated", count)
	}
	staleArgs, _ := sonic.MarshalString(map[string]any{"id": created.Data.ID, "expected_version": 1, "status": "disabled"})
	if _, code, err := app.Call(context.Background(), session, []byte(`"stale-disable"`), "set_pipeline_status", []byte(staleArgs), nil); err != nil || code != "resource_version_conflict" {
		t.Fatal("stale lifecycle update accepted", code, err)
	}
}

func TestExternalMCPPendingRetryDoesNotExecuteTwice(t *testing.T) {
	base, _, _ := externalMCPFixture(t, 10)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	catalog := NewToolCatalogApplication()
	catalog.Register(readTool("list_instruments", "blocked fixture", objectSchema(map[string]any{}), map[string]any{"type": "object"}, []string{"research"}, func(context.Context, []byte) (any, error) {
		calls.Add(1)
		close(entered)
		<-release
		return map[string]any{"ok": true}, nil
	}))
	app, err := NewExternalMCPApplication(catalog, base.audit, base.approval, base.sessions, true)
	if err != nil {
		t.Fatal(err)
	}
	session := externalSession(t, app)
	done := make(chan error, 1)
	go func() {
		_, _, err := app.Call(context.Background(), session, []byte(`1`), "list_instruments", []byte(`{}`), nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	_, _, retryErr := app.Call(context.Background(), session, []byte(`1`), "list_instruments", []byte(`{}`), nil)
	close(release)
	if !errors.Is(retryErr, service.ErrAgentToolPending) {
		t.Fatal("pending retry admitted", retryErr)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	externalCall(t, app, session, `1`, "list_instruments", `{}`)
	if calls.Load() != 1 {
		t.Fatal("business handler executed more than once", calls.Load())
	}
}
