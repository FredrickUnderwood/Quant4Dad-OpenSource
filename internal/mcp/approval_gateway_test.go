package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestApprovalGatewayAtomicWriteReplayAndCrashRecovery(t *testing.T) {
	_, db := mcpTestServer(t)
	if err := repository.Migrate(db, config.StorageBackendSQLite); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	ctx := context.Background()
	validationRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	validationFiles, err := repository.NewAgentToolArtifactRepository(filepath.Join(validationRoot, "checks"))
	if err != nil {
		t.Fatal(err)
	}
	st := service.NewStrategyService(repository.NewStrategyRepository(db), validationFiles)
	pl := service.NewPipelineService(repository.NewPipelineRepository(db), nodes.BuildRegistry(nil, nil))
	mutations := service.NewAgentMutationService(repository.NewAgentMutationRepository(db), st, pl)
	catalog := application.NewToolCatalogApplication()
	for _, d := range application.CatalogWriteToolDefinitions(mutations, pl) {
		catalog.Register(d)
	}
	for _, d := range application.CatalogValidationToolDefinitions(st, nil, nil, nil) {
		catalog.Register(d)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	signer, err := agentrunauth.NewSigner("key-1", "fixture-issuer", key)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-user"}, Agent: config.AgentConfig{Enabled: true, Bootstrap: config.AgentBootstrapConfig{Enabled: true, MCPURL: "http://api/internal/mcp", ControlToken: "fixture-control-token-0123456789", MCPToken: internalTestToken, CapabilityIssuer: "fixture-issuer", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key[32:])}}}}
	e := agentrunauth.Envelope{RunID: strings.Repeat("r", 32), ClientRequestID: "request", ActorID: "actor", Q4DVersion: "0.0.0", AgentImageDigest: digest, AgentRuntimeVersion: "fixture-v1", BridgeProtocol: 1, DSHVersion: "0.1.2-alpha.5", AdapterVersion: "fixture-v1", ProductProfile: "strategy_lab", ProfileRevision: digest, PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: catalog.Revision("strategy_lab"), Provider: "fixture", Model: "fixture", ModelConfigRevision: "revision", Budgets: agentrunauth.Budgets{MaxTurns: 3, MaxToolCalls: 20, MaxInputTokens: 8192, MaxOutputTokens: 1024, WallTimeMS: 60000}}
	token, claims, err := signer.Issue(agentrunauth.IssueRequest{SessionID: strings.Repeat("s", 32), RequestHash: digest, Envelope: e, AllowedTools: []string{"create_strategy", "update_strategy", "validate_strategy"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rawClaims, _ := sonic.MarshalString(claims)
	if err := db.Create(&domain.AgentRequestBinding{ID: e.RunID, ActorID: e.ActorID, SessionID: claims.SessionID, ClientRequestKey: "fixture", EnvelopeDigest: claims.EnvelopeDigest, ClaimsJSON: rawClaims, DeadlineMS: claims.DeadlineMillis()}).Error; err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "artifacts")
	artifacts, err := repository.NewAgentToolArtifactRepository(directory)
	if err != nil {
		t.Fatal(err)
	}
	audit := service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
	approval := service.NewAgentApprovalService(repository.NewAgentApprovalRepository(db), signer)
	app, err := application.NewAgentToolGatewayApplication(catalog, audit, cfg, func(_ context.Context, c agentrunauth.Claims) (bool, error) {
		a, _ := agentrunauth.CanonicalClaims(c)
		b, _ := agentrunauth.CanonicalClaims(claims)
		return bytes.Equal(a, b), nil
	}, approval)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	invoke := func(call, name, args, receipt string) ([]byte, string, error) {
		return app.Call(ctx, token, call, "q4d:"+e.RunID+":"+call, name, []byte(args), func() error { starts++; return nil }, receipt)
	}
	proposed := func(call, name, args string) (domain.AgentApprovalReceipt, string) {
		t.Helper()
		body, code, err := invoke(call, name, args, "")
		if err != nil || code != "agent_approval_required" {
			t.Fatal(code, err)
		}
		var challenge struct {
			Error struct {
				ID string `json:"approval_id"`
			}
		}
		if sonic.Unmarshal(body, &challenge) != nil {
			t.Fatal("challenge invalid")
		}
		row, receipt, err := approval.Decide(ctx, "actor", challenge.Error.ID, "allow_once")
		if err != nil {
			t.Fatal(err)
		}
		return row, receipt
	}
	checked := func(call, args string) string {
		t.Helper()
		var input map[string]any
		if err := sonic.UnmarshalString(args, &input); err != nil {
			t.Fatal(err)
		}
		validation, _ := sonic.MarshalString(map[string]any{"strategy": input["strategy"]})
		body, code, err := invoke(call, "validate_strategy", validation, "")
		var report struct {
			Data service.StrategyCheckReport `json:"data"`
		}
		if err != nil || code != "" || sonic.Unmarshal(body, &report) != nil || !report.Data.Valid || report.Data.ValidationID == "" {
			t.Fatalf("validation: %s %s %v", body, code, err)
		}
		input["validation_id"] = report.Data.ValidationID
		out, _ := sonic.MarshalString(input)
		return out
	}
	args := `{"strategy":{"name":"created-once","universe":["sh.600519"],"period":"1d","body":{"indicators":[],"rules":[],"execution":{"fill_at":"close"}}}}`
	args = checked(gatewayCall[:25]+"3", args)
	starts = 0
	row, receipt := proposed(gatewayCall, "create_strategy", args)
	if starts != 0 {
		t.Fatal("approval challenge announced execution")
	}
	if _, _, err := approval.Decide(ctx, "other-actor", row.ID, "allow_once"); err == nil {
		t.Fatal("cross actor approval")
	}
	_, repeat, err := approval.Decide(ctx, "actor", row.ID, "allow_once")
	if err != nil || repeat != receipt {
		t.Fatal("decision renewed receipt")
	}
	if _, _, err := invoke(gatewayCall, "create_strategy", args, strings.Repeat("a", 86)); err == nil || starts != 0 {
		t.Fatal("forged receipt executed")
	}
	// Commit the resource and idempotency record, then lose result publication.
	if err := os.Chmod(directory, 0500); err != nil {
		t.Fatal(err)
	}
	_, _, writeErr := invoke(gatewayCall, "create_strategy", args, receipt)
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(writeErr, service.ErrAgentToolStore) {
		t.Fatal("failure injection did not hit artifact publication", writeErr)
	}
	body, code, err := invoke(gatewayCall, "create_strategy", args, receipt)
	if err != nil || code != "" || !bytes.Contains(body, []byte(`"version":1`)) {
		t.Fatalf("effect recovery: %s %s %v", body, code, err)
	}
	var count int64
	db.Model(&domain.Strategy{}).Count(&count)
	if count != 1 {
		t.Fatal("write duplicated", count)
	}
	stored, err := approval.Get(ctx, "actor", row.ID)
	if err != nil || stored.Status != "consumed" {
		t.Fatal("receipt not consumed", err)
	}
	if _, _, err := invoke(gatewayCall, "create_strategy", strings.Replace(args, "created-once", "changed", 1), receipt); !errors.Is(err, service.ErrAgentToolConflict) {
		t.Fatal("args changed under receipt", err)
	}
	secondCall := gatewayCall[:25] + "1"
	_, _, err = invoke(secondCall, "create_strategy", strings.Replace(args, "created-once", "second", 1), receipt)
	if !errors.Is(err, service.ErrAgentApproval) {
		t.Fatal("cross-call receipt replay", err)
	}
	// Reusing a valid validation ID after changing only metadata must fail
	// even when the changed write was explicitly approved.
	tampered := strings.Replace(args, `"name":"created-once"`, `"name":"created-once","description":"added after validation"`, 1)
	mismatchCall := gatewayCall[:25] + "5"
	mismatchApproval, mismatchReceipt := proposed(mismatchCall, "create_strategy", tampered)
	_, code, err = invoke(mismatchCall, "create_strategy", tampered, mismatchReceipt)
	if err != nil || code != "strategy_validation_mismatch" {
		t.Fatal("validation mismatch not surfaced", code, err)
	}
	mismatchApproval, err = approval.Get(ctx, "actor", mismatchApproval.ID)
	if err != nil || mismatchApproval.Status != "approved" {
		t.Fatal("failed validation consumed approval", err)
	}
	// A concurrent editor change invalidates an already-approved update.
	update := `{"id":1,"expected_version":1,"strategy":{"name":"stale-update","universe":["sh.600519"],"period":"1d","body":{"indicators":[],"rules":[],"execution":{"fill_at":"close"}}}}`
	update = checked(gatewayCall[:25]+"4", update)
	thirdCall := gatewayCall[:25] + "2"
	staleApproval, staleReceipt := proposed(thirdCall, "update_strategy", update)
	if err := db.Model(&domain.Strategy{}).Where("id = ?", 1).Updates(map[string]any{"name": "editor-won", "version": 2}).Error; err != nil {
		t.Fatal(err)
	}
	_, code, err = invoke(thirdCall, "update_strategy", update, staleReceipt)
	if err != nil || code != "resource_version_conflict" {
		t.Fatal("stale write not rejected", code, err)
	}
	staleApproval, err = approval.Get(ctx, "actor", staleApproval.ID)
	if err != nil || staleApproval.Status != "approved" {
		t.Fatal("rollback consumed approval", err)
	}
	current, err := st.GetByID(ctx, 1)
	if err != nil || current.Name != "editor-won" || current.Version != 2 {
		t.Fatal("stale mutation changed resource", err)
	}
	db.Model(&domain.AgentToolEffect{}).Count(&count)
	if count != 1 {
		t.Fatal("rejected mutations left effects", count)
	}
}
