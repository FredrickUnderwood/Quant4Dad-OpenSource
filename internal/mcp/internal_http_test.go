package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/tooljson"
	"gorm.io/gorm"
)

const internalTestToken = "fixture-internal-mcp-token-0123456789"
const gatewayCall = "01K00000000000000000000000"

type gatewayFixture struct {
	app        *application.AgentToolGatewayApplication
	audit      *service.AgentToolAuditService
	handler    http.Handler
	db         *gorm.DB
	capability string
	claims     agentrunauth.Claims
	ended      atomic.Bool
	queries    atomic.Int64
	directory  string
}

func newGatewayFixture(t *testing.T, limit int64) *gatewayFixture {
	t.Helper()
	server, db := mcpTestServer(t)
	if err := db.AutoMigrate(&domain.AgentRequestBinding{}, &domain.AgentToolAudit{}); err != nil {
		t.Fatal(err)
	}
	conn, _ := db.DB()
	conn.SetMaxOpenConns(1)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	digest := "sha256:" + strings.Repeat("a", 64)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-user"}, Agent: config.AgentConfig{Enabled: true, Bootstrap: config.AgentBootstrapConfig{Enabled: true, MCPURL: "http://api/internal/mcp", ControlToken: "fixture-control-token-0123456789", MCPToken: internalTestToken, CapabilityIssuer: "fixture-issuer", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key[32:])}}}}
	signer, err := agentrunauth.NewSigner("key-1", "fixture-issuer", key)
	if err != nil {
		t.Fatal(err)
	}
	e := agentrunauth.Envelope{RunID: strings.Repeat("r", 32), ClientRequestID: "request", ActorID: "actor", Q4DVersion: "0.0.0", AgentImageDigest: digest, AgentRuntimeVersion: "fixture-v1", BridgeProtocol: 1, DSHVersion: "0.1.2-alpha.5", AdapterVersion: "fixture-v1", ProductProfile: "research", ProfileRevision: digest, PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: server.catalog.Revision("research"), Provider: "fixture", Model: "fixture", ModelConfigRevision: "revision", Budgets: agentrunauth.Budgets{MaxTurns: 3, MaxToolCalls: limit, MaxInputTokens: 8192, MaxOutputTokens: 1024, WallTimeMS: 60000}}
	token, claims, err := signer.Issue(agentrunauth.IssueRequest{SessionID: strings.Repeat("s", 32), RequestHash: digest, Envelope: e, AllowedTools: []string{"get_instrument", "latest_bar_date", "list_instruments", "query_kline"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := sonic.Marshal(claims)
	if err = db.Create(&domain.AgentRequestBinding{ID: e.RunID, SessionID: claims.SessionID, ActorID: e.ActorID, ClientRequestKey: "fixture", RequestHash: digest, EnvelopeDigest: claims.EnvelopeDigest, ClaimsJSON: string(stored), SigningKeyID: "key-1", DeadlineMS: claims.DeadlineMillis()}).Error; err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &gatewayFixture{db: db, capability: token, claims: claims, directory: filepath.Join(parent, "artifacts")}
	artifacts, err := repository.NewAgentToolArtifactRepository(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	f.audit = service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
	f.app, err = application.NewAgentToolGatewayApplication(server.catalog, f.audit, cfg, func(_ context.Context, c agentrunauth.Claims) (bool, error) {
		a, _ := agentrunauth.CanonicalClaims(c)
		b, _ := agentrunauth.CanonicalClaims(claims)
		return !f.ended.Load() && bytes.Equal(a, b), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = InternalHTTPHandler(f.app, internalTestToken)
	if err = db.Callback().Query().Before("gorm:query").Register("fixture:count-bars", func(tx *gorm.DB) {
		if tx.Statement.Table == "bar" {
			f.queries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *gatewayFixture) key(call string) string {
	return "q4d:" + f.claims.Envelope.RunID + ":" + call
}
func (f *gatewayFixture) invoke(call, args string) ([]byte, string, error) {
	return f.app.Call(context.Background(), f.capability, call, f.key(call), "query_kline", []byte(args), func() error { return nil })
}
func TestInternalGatewaySignedReadAndDurableReplay(t *testing.T) {
	f := newGatewayFixture(t, 4)
	first, code, err := f.invoke(gatewayCall, `{"code":"sh.600519","limit":2}`)
	if err != nil || code != "" {
		t.Fatal(err, code)
	}
	if !bytes.Contains(first, []byte(`"count":2`)) || f.queries.Load() != 1 {
		t.Fatal("missing bounded query")
	}
	if err = f.db.Model(&domain.Bar{}).Where("code = ?", "sh.600519").Update("close", 999).Error; err != nil {
		t.Fatal(err)
	}
	replay, code, err := f.invoke(gatewayCall, ` {"limit":2.0,"code":"sh.600519"} `)
	if err != nil || code != "" || !bytes.Equal(first, replay) || f.queries.Load() != 1 {
		t.Fatal("replay queried changed data", err, code)
	}
	_, _, err = f.invoke(gatewayCall, `{"code":"sh.600519","limit":3}`)
	if !errors.Is(err, service.ErrAgentToolConflict) {
		t.Fatal("changed args reused identity", err)
	}
	var rows []domain.AgentToolAudit
	if err = f.db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].Status != "succeeded" {
		t.Fatal(rows, err)
	}
	// Artifact loss cannot cause a fresh execution or an invented successful result.
	if err = os.Remove(filepath.Join(f.directory, rows[0].ResultRef)); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.invoke(gatewayCall, `{"code":"sh.600519","limit":2}`)
	if !errors.Is(err, service.ErrAgentToolStore) || f.queries.Load() != 1 {
		t.Fatal("missing artifact redispatched", err)
	}
}
func TestInternalGatewayAuthorityArgumentsAndNotifications(t *testing.T) {
	f := newGatewayFixture(t, 4)
	for _, args := range []string{`{"code":"../../secret"}`, `{"code":"sh.600519","run_id":"forged"}`, `{"code":"sh.600519","limit":0}`, `{"code":"sh.600519","limit":2.5}`, `{"code":"sh.600519","limit":501}`} {
		if _, _, err := f.invoke(gatewayCall, args); !errors.Is(err, service.ErrToolInput) {
			t.Fatal("invalid args accepted", args, err)
		}
	}
	if _, _, err := f.app.Call(context.Background(), f.capability, gatewayCall, f.key(gatewayCall), "create_pipeline", []byte(`{}`), func() error { return nil }); !errors.Is(err, application.ErrToolForbidden) {
		t.Fatal(err)
	}
	if _, err := f.app.Authorize(context.Background(), f.capability+"x"); err == nil {
		t.Fatal("bad signature accepted")
	}
	f.ended.Store(true)
	if _, _, err := f.invoke(gatewayCall, `{"code":"sh.600519"}`); err == nil {
		t.Fatal("ended run accepted")
	}
	f.ended.Store(false)
	notify := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"query_kline","arguments":{"code":"sh.600519"}}}`
	w := gatewayRequest(f, f.handler, notify, nil)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if f.queries.Load() != 0 {
		t.Fatal("rejected request queried storage")
	}
	var count int64
	f.db.Model(&domain.AgentToolAudit{}).Count(&count)
	if count != 0 {
		t.Fatal("invalid/notification created audit")
	}
}
func TestInternalGatewayBudgetConcurrencyAndUncertainCalls(t *testing.T) {
	f := newGatewayFixture(t, 2)
	var successes atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			call := gatewayCall[:25] + string(rune('0'+i))
			_, code, err := f.invoke(call, `{"code":"sh.600519","limit":1}`)
			if err == nil && code == "" {
				successes.Add(1)
			} else if !errors.Is(err, service.ErrAgentToolBudget) {
				t.Error("unexpected reservation failure", err, code)
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 2 || f.queries.Load() != 2 {
		t.Fatal("concurrent calls exceeded budget", successes.Load(), f.queries.Load())
	}
	f = newGatewayFixture(t, 2)
	args := []byte(`{"code":"sh.600519","limit":1}`)
	hash, _ := tooljson.Hash(args)
	row, created, err := f.audit.Reserve(context.Background(), f.claims, gatewayCall, f.key(gatewayCall), "query_kline", hash)
	if err != nil || !created {
		t.Fatal(err)
	}
	if _, _, err = f.audit.Replay(row); !errors.Is(err, service.ErrAgentToolPending) {
		t.Fatal("executing call replayed", err)
	}
	if f.queries.Load() != 0 {
		t.Fatal("uncertain call executed")
	}
	if err = f.audit.Finish(context.Background(), row, nil, "agent_capability_rejected"); err != nil {
		t.Fatal(err)
	}
	_, code, err := f.app.Call(context.Background(), f.capability, gatewayCall, f.key(gatewayCall), "query_kline", args, func() error { t.Fatal("pre-dispatch denial invented a start during replay"); return nil })
	if err != nil || code != "agent_capability_rejected" || f.queries.Load() != 0 {
		t.Fatal("denial replay", err, code)
	}
}
func gatewayRequest(f *gatewayFixture, h http.Handler, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/internal/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+internalTestToken)
	r.Header.Set("X-Q4D-Run-Capability", f.capability)
	r.Header.Set("X-Q4D-Tool-Call-ID", gatewayCall)
	r.Header.Set("Idempotency-Key", f.key(gatewayCall))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestInternalGatewayHTTPIngressAndStartedOrdering(t *testing.T) {
	f := newGatewayFixture(t, 2)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query_kline","arguments":{"code":"sh.600519","limit":1},"_meta":{"progressToken":0}}}`
	for _, mutate := range []func(*http.Request){func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testToken) }, func(r *http.Request) { r.Header.Add("X-Q4D-Run-Capability", f.capability) }, func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, func(r *http.Request) { r.URL.RawQuery = "token=secret" }, func(r *http.Request) { r.Header.Set("X-Q4D-Approval-Receipt", "receipt") }, func(r *http.Request) { r.Header.Set("Idempotency-Key", "forged") }, func(r *http.Request) { r.Header.Set("X-Q4D-Tool-Call-ID", "model-owned") }} {
		w := gatewayRequest(f, f.handler, body, mutate)
		if w.Code < 400 {
			t.Fatal("invalid ingress accepted")
		}
	}
	if f.queries.Load() != 0 {
		t.Fatal("invalid ingress executed")
	}
	w := gatewayRequest(f, f.handler, body, nil)
	text := w.Body.String()
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") || strings.Index(text, "q4d.tool.started.v1") < 0 || strings.Index(text, "q4d.tool.started.v1") > strings.Index(text, "structuredContent") || f.queries.Load() != 1 {
		t.Fatal("missing start before result", w.Code, text)
	}
	if strings.Contains(text, f.capability) || strings.Contains(text, internalTestToken) {
		t.Fatal("credentials in result")
	}
}
