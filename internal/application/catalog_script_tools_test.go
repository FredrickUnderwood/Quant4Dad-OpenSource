package application

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/script"
	"github.com/quant4dad/internal/service"
)

func scriptStrategyInput() service.StrategyInput {
	return service.StrategyInput{Name: "Agent script", Universe: []string{"sh.600519"}, Period: domain.Bar1d,
		Body: domain.StrategyBody{Mode: "script", Lang: "starlark", Code: "def on_bar(ctx):\n    return buy(shares=100) if not ctx.has_position else None\n", Execution: domain.ExecutionSpec{FillAt: "next_open"}}}
}

func TestAgentScriptValidationToolContract(t *testing.T) {
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogValidationToolDefinitions(service.NewStrategyService(nil), nil, nil, nil) {
		catalog.Register(d)
	}
	for _, d := range CatalogWriteToolDefinitions(&service.AgentMutationService{}, service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))) {
		catalog.Register(d)
	}
	invoke := func(in service.StrategyInput) []byte {
		t.Helper()
		raw, err := sonic.Marshal(map[string]any{"strategy": in})
		if err != nil {
			t.Fatal(err)
		}
		out, err := catalog.Invoke(context.Background(), "strategy_lab", "validate_strategy", raw)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	var result struct {
		Data struct {
			Valid    bool                          `json:"valid"`
			Errors   []service.ToolValidationIssue `json:"errors"`
			Checks   []string                      `json:"checks"`
			Warnings []string                      `json:"warnings"`
		} `json:"data"`
	}
	valid := scriptStrategyInput()
	out := invoke(valid)
	if err := sonic.Unmarshal(out, &result); err != nil || !result.Data.Valid || len(result.Data.Errors) != 0 || len(result.Data.Checks) != 3 || len(result.Data.Warnings) == 0 {
		t.Fatalf("missing script checks and limitations: %s (%v)", out, err)
	}
	for _, source := range []string{"def on_bar(ctx):\n    return ctx.history('close', 20)[19]", "def on_bar(ctx, other):\n    return None", "def on_bar(ctx):\n    return sell(pct_of_cash=0.5)"} {
		in := valid
		in.Body.Code = source
		out = invoke(in)
		if err := sonic.Unmarshal(out, &result); err != nil || result.Data.Valid || len(result.Data.Errors) != 1 || result.Data.Errors[0].Path != "body.code" {
			t.Fatalf("missing actionable script error: %s (%v)", out, err)
		}
	}
	for _, name := range []string{"validate_strategy", "create_strategy", "update_strategy"} {
		for _, mutate := range []func(*service.StrategyInput){
			func(in *service.StrategyInput) { in.Body.Lang = "python" },
			func(in *service.StrategyInput) { in.Body.Mode = "config" },
			func(in *service.StrategyInput) { in.Body.Code = strings.Repeat("#", script.MaxCodeBytes+1) },
			func(in *service.StrategyInput) {
				in.Body.Indicators = []domain.IndicatorSpec{{Alias: "ma", Type: "MA", Params: map[string]any{"period": 5}}}
			},
		} {
			in := valid
			mutate(&in)
			args := map[string]any{"strategy": in}
			if name != "validate_strategy" {
				args["validation_id"] = strings.Repeat("0", 64)
			}
			if name == "update_strategy" {
				args["id"], args["expected_version"] = 1, 1
			}
			raw, _ := sonic.Marshal(args)
			if _, err := catalog.ValidateInput("strategy_lab", name, raw); !errors.Is(err, service.ErrToolInput) {
				t.Fatalf("%s accepted invalid script structure: %v", name, err)
			}
		}
	}
}

func TestAgentValidationModeAndSuiteAdmission(t *testing.T) {
	catalog := NewToolCatalogApplication()
	for _, d := range CatalogValidationToolDefinitions(service.NewStrategyService(nil), nil, nil, nil) {
		catalog.Register(d)
	}
	for _, mode := range []string{"", "config", "script"} {
		in := scriptStrategyInput()
		if mode != "script" {
			in.Body = domain.StrategyBody{Mode: mode, Indicators: []domain.IndicatorSpec{}, Rules: []domain.RuleSpec{}, Execution: domain.ExecutionSpec{FillAt: "next_open"}}
		}
		for _, suites := range [][]string{{}, {script.DailyRoundTrip100}} {
			raw, _ := sonic.Marshal(map[string]any{"strategy": in, "behavior_tests": suites})
			_, err := catalog.ValidateInput("strategy_lab", "validate_strategy", raw)
			if mode != "script" && len(suites) > 0 {
				if !errors.Is(err, service.ErrToolInput) {
					t.Fatalf("config suite admitted: %s %v", mode, err)
				}
			} else if err != nil {
				t.Fatalf("valid mode/suite rejected: %s %v", mode, err)
			}
		}
	}
}

func TestAgentScriptCreateUpdateAndBacktestAdmission(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "script.db")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := repository.NewAgentToolArtifactRepository(filepath.Join(root, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	strategy := service.NewStrategyService(repository.NewStrategyRepository(db), files)
	pipeline := service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))
	mutations := service.NewAgentMutationService(repository.NewAgentMutationRepository(db), strategy, pipeline)
	cost := service.NewCostService(repository.NewCostRepository(db))
	if err := cost.EnsureDefault(context.Background()); err != nil {
		t.Fatal(err)
	}
	catalog := NewAgentP0Catalog(nil, nil, strategy, pipeline, nil, nil, mutations, cost)
	run := domain.AgentRequestBinding{ID: "script-run", ActorID: "script-actor", SessionID: "script-session", ClientRequestKey: "script-request", EnvelopeDigest: "script-envelope", DeadlineMS: time.Now().Add(time.Minute).UnixMilli()}
	if err := db.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	sequence := 0
	invoke := func(name string, args map[string]any, approved bool) (domain.AgentMutationResult, error) {
		t.Helper()
		sequence++
		id := "script-call-" + strconv.Itoa(sequence)
		risk, status := "R2", "pending_approval"
		if name == "run_backtest" {
			risk, status = "R1", "executing"
		}
		audit := domain.AgentToolAudit{ID: id, ActorID: run.ActorID, SessionID: run.SessionID, RunID: run.ID, ToolCallID: id, ToolName: name, IdempotencyKey: id, ArgsHash: id, EnvelopeDigest: run.EnvelopeDigest, Risk: risk, Status: status, Started: true}
		if err := db.Create(&audit).Error; err != nil {
			t.Fatal(err)
		}
		receipt := domain.AgentApprovalReceipt{}
		if approved && risk == "R2" {
			receipt = domain.AgentApprovalReceipt{ID: id, AuditID: id, ArgsHash: audit.ArgsHash, Risk: risk, EnvelopeDigest: run.EnvelopeDigest, Status: "approved", ReceiptNonce: &id, ExpiresAt: time.Now().Add(time.Minute)}
			if err := db.Create(&receipt).Error; err != nil {
				t.Fatal(err)
			}
		}
		ctx := service.WithAgentExecution(context.Background(), audit, receipt)
		raw, err := sonic.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		out, err := catalog.Invoke(ctx, "strategy_lab", name, raw)
		var result struct {
			Data domain.AgentMutationResult `json:"data"`
		}
		if err == nil {
			err = sonic.Unmarshal(out, &result)
		}
		return result.Data, err
	}
	validate := func(in service.StrategyInput) string {
		t.Helper()
		ctx := service.WithAgentExecution(context.Background(), domain.AgentToolAudit{ID: "validation", ActorID: run.ActorID, SessionID: run.SessionID, ToolName: "validate_strategy"}, domain.AgentApprovalReceipt{})
		raw, _ := sonic.Marshal(map[string]any{"strategy": in})
		out, err := catalog.Invoke(ctx, "strategy_lab", "validate_strategy", raw)
		var r struct {
			Data service.StrategyCheckReport `json:"data"`
		}
		if err != nil || sonic.Unmarshal(out, &r) != nil || !r.Data.Valid || r.Data.ValidationID == "" {
			t.Fatalf("check failed: %s %v", out, err)
		}
		return r.Data.ValidationID
	}
	in := scriptStrategyInput()
	proof := validate(in)
	if _, err := invoke("create_strategy", map[string]any{"strategy": in, "validation_id": proof}, false); err == nil {
		t.Fatal("script write bypassed approval")
	}
	invalid := in
	invalid.Body.Code = "def on_bar(ctx):\n    return {'action': 'buy'}"
	if _, err := invoke("create_strategy", map[string]any{"strategy": invalid, "validation_id": proof}, true); !errors.Is(err, service.ErrStrategyValidationMismatch) {
		t.Fatal("script persistence bypassed source binding", err)
	}
	changedDescription := in
	changedDescription.Description = "Added after validation"
	if _, err := invoke("create_strategy", map[string]any{"strategy": changedDescription, "validation_id": proof}, true); !errors.Is(err, service.ErrStrategyValidationMismatch) {
		t.Fatal("description mutation admitted", err)
	}
	if _, err := invoke("create_strategy", map[string]any{"strategy": in, "validation_id": strings.Repeat("0", 64)}, true); !errors.Is(err, service.ErrStrategyValidationRequired) {
		t.Fatal("forged validation admitted", err)
	}
	var count int64
	if err := db.Model(&domain.Strategy{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("rejected writes persisted", count, err)
	}
	created, err := invoke("create_strategy", map[string]any{"strategy": in, "validation_id": proof}, true)
	if err != nil || created.ID == 0 || created.Version != 1 {
		t.Fatal("script create failed", created, err)
	}
	in.Body.Code = strings.Replace(in.Body.Code, "shares=100", "shares=200", 1)
	proof = validate(in)
	updated, err := invoke("update_strategy", map[string]any{"id": created.ID, "expected_version": 1, "strategy": in, "validation_id": proof}, true)
	if err != nil || updated.Version != 2 {
		t.Fatal("script update failed", updated, err)
	}
	if _, err := invoke("update_strategy", map[string]any{"id": created.ID, "expected_version": 1, "strategy": in, "validation_id": proof}, true); !errors.Is(err, domain.ErrResourceConflict) {
		t.Fatal("script update bypassed version check", err)
	}
	stored, err := strategy.GetByID(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body domain.StrategyBody
	if err := sonic.Unmarshal(stored.Body, &body); err != nil || body.Code != in.Body.Code || !body.IsScript() || stored.Version != 2 {
		t.Fatal("script source/version not preserved", err)
	}
	job, err := invoke("run_backtest", map[string]any{"strategy_id": created.ID, "expected_version": 2, "initial_capital": 100000, "start_date": "2026-01-01", "end_date": "2026-02-01"}, false)
	if err != nil || job.JobID == 0 {
		t.Fatal("script backtest admission failed", job, err)
	}
	var queued domain.BacktestJob
	if err := db.First(&queued, job.JobID).Error; err != nil {
		t.Fatal(err)
	}
	var snapshot domain.AgentBacktestSnapshot
	if err := sonic.UnmarshalString(queued.AgentSnapshot, &snapshot); err != nil || string(snapshot.Strategy.Body) != string(stored.Body) || snapshot.Strategy.Version != 2 {
		t.Fatal("script backtest lost approved snapshot", err)
	}
}
