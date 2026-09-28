package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

const bootstrapHTTPControl = "fixture-bootstrap-control-0123456789-abcdef"
const bootstrapHTTPMCP = "fixture-bootstrap-mcp-0123456789-abcdef"

func bootstrapHTTPServer(t *testing.T, configure ...func(*config.Config)) (*Server, *gorm.DB) {
	t.Helper()
	_, db := modelHTTPServer(t)
	svc := service.NewSettingService(repository.NewSettingRepository(db))
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true,
		Bootstrap: config.AgentBootstrapConfig{Enabled: true, ControlToken: bootstrapHTTPControl, MCPToken: bootstrapHTTPMCP,
			MCPURL: "http://mcp:8081/internal/mcp", CapabilityIssuer: "fixture-issuer",
			CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key)}}}}
	for _, customize := range configure {
		customize(cfg)
	}
	app, err := application.NewAgentBootstrapApplication(svc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg, Handlers{Setting: NewSettingHandler(svc), AgentBootstrap: NewAgentBootstrapHandler(app, bootstrapHTTPControl)})
	modelHTTPSeed(t, srv)
	return srv, db
}

func bootstrapHTTPRequest(t *testing.T, srv *Server, query string, auth []string, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", agentBootstrapPath, strings.NewReader(body))
	req.URL.RawQuery = query
	if auth != nil {
		req.Header["Authorization"] = auth
	}
	// A valid login Cookie is deliberately present on every request. It never
	// grants access to bootstrap without the independent control credential.
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
	req.Header.Set("User-Agent", bootstrapHTTPControl)
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("bootstrap status %d, want %d", w.Code, status)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("bootstrap lacks private response headers")
	}
	for _, secret := range []string{bootstrapHTTPControl, "fixture-login-token"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("bootstrap leaked a control or user credential")
		}
	}
	if status != 200 {
		for _, secret := range []string{bootstrapHTTPMCP, modelHTTPSecret} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("non-success response exposed a secret")
			}
		}
	}
	return w
}

func TestAgentBootstrapHTTPAuthentication(t *testing.T) {
	srv, _ := bootstrapHTTPServer(t)
	for _, auth := range [][]string{nil, {""}, {"Bearer "}, {"Bearer fixture-login-token"}, {"Bearer " + bootstrapHTTPMCP},
		{"Basic " + bootstrapHTTPControl}, {"bearer " + bootstrapHTTPControl}, {"Bearer  " + bootstrapHTTPControl},
		{"Bearer " + bootstrapHTTPControl + " "}, {"Bearer " + bootstrapHTTPControl, "Bearer " + bootstrapHTTPControl},
		{"Bearer " + strings.Repeat("x", 257)}} {
		w := bootstrapHTTPRequest(t, srv, "", auth, "", 401)
		if w.Header().Get("WWW-Authenticate") != "Bearer" || w.Header().Get("ETag") != "" {
			t.Fatal("invalid unauthorized response")
		}
	}
	bootstrapHTTPRequest(t, srv, "token="+bootstrapHTTPControl, nil, "", 401)
	bootstrapHTTPRequest(t, srv, "", nil, bootstrapHTTPControl, 401)
	bootstrapHTTPRequest(t, srv, "", []string{"Bearer " + bootstrapHTTPControl}, "", 200)
	// The Runtime has no browser session. Control authentication must stand alone.
	control := httptest.NewRequest("GET", agentBootstrapPath, nil)
	control.Header.Set("Authorization", "Bearer "+bootstrapHTTPControl)
	controlResponse := httptest.NewRecorder()
	srv.engine.ServeHTTP(controlResponse, control)
	if controlResponse.Code != 200 {
		t.Fatal("bootstrap requires an unrelated browser cookie")
	}
	// The external browser API does not accept the service credential as login.
	req := httptest.NewRequest("GET", "/api/v1/agent/models", nil)
	req.Header.Set("Authorization", "Bearer "+bootstrapHTTPControl)
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("service token became browser authentication")
	}
}

func TestAgentBootstrapHTTPConditionalAndRevoke(t *testing.T) {
	srv, _ := bootstrapHTTPServer(t)
	auth := []string{"Bearer " + bootstrapHTTPControl}
	w := bootstrapHTTPRequest(t, srv, "", auth, "", 200)
	var first domain.AgentBootstrap
	if sonic.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Providers) != 1 || first.Providers[0].APIKey != modelHTTPSecret || first.MCP.RuntimeToken != bootstrapHTTPMCP {
		t.Fatal("authorized bootstrap did not deliver expected credentials")
	}
	if first.ProtocolVersion != application.AgentBootstrapProtocol || w.Header().Get("ETag") != `"`+first.Revision+`"` {
		t.Fatal("incorrect bootstrap version/ETag")
	}
	w = bootstrapHTTPRequest(t, srv, "revision="+first.Revision, auth, "", 304)
	if w.Body.Len() != 0 || w.Header().Get("ETag") != `"`+first.Revision+`"` {
		t.Fatal("304 must have no response body and preserve revision")
	}
	modelHTTPRequest(t, srv, "PATCH", "/api/v1/settings/llm-providers/fixture", `{"api_key_update":{"action":"revoke"}}`, "", 200)
	w = bootstrapHTTPRequest(t, srv, "revision="+first.Revision, auth, "", 200)
	var revoked domain.AgentBootstrap
	if sonic.Unmarshal(w.Body.Bytes(), &revoked) != nil || revoked.Revision == first.Revision || revoked.ModelConfigRevision == first.ModelConfigRevision || len(revoked.Providers) != 0 || strings.Contains(w.Body.String(), modelHTTPSecret) {
		t.Fatal("revoked provider retained or conditional cache stale")
	}
	bootstrapHTTPRequest(t, srv, "revision="+revoked.Revision, auth, "", 304)
}

func TestAgentBootstrapHTTPRejectsInvalidRequests(t *testing.T) {
	srv, _ := bootstrapHTTPServer(t)
	auth := []string{"Bearer " + bootstrapHTTPControl}
	valid := strings.Repeat("a", 32) + "." + strings.Repeat("b", 32)
	for _, query := range []string{"revision=", "revision=bad", "revision=%FF", "revision=%", "revision=" + valid + "&revision=" + valid,
		"revision=" + valid + "&secret=value", "token=" + bootstrapHTTPControl, "revision=" + valid + ";ignored=1", "revision=" + valid + "%0A", strings.Repeat("x", 129)} {
		w := bootstrapHTTPRequest(t, srv, query, auth, "", 400)
		if w.Header().Get("ETag") != "" {
			t.Fatal("invalid request returned a success revision")
		}
	}
	bootstrapHTTPRequest(t, srv, "", auth, `{"revision":"ignored"}`, 400)
	bootstrapHTTPRequest(t, srv, "revision="+valid, auth, "", 200)
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		req := httptest.NewRequest(method, agentBootstrapPath, nil)
		req.Header.Set("Authorization", auth[0])
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, req)
		if w.Code != 404 || strings.Contains(w.Body.String(), modelHTTPSecret) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("unexpected control method handling")
		}
	}
}

func TestAgentBootstrapHTTPGatesAndStorageFailure(t *testing.T) {
	srv, db := bootstrapHTTPServer(t)
	auth := []string{"Bearer " + bootstrapHTTPControl}
	first := bootstrapHTTPRequest(t, srv, "", auth, "", 200)
	revision := strings.Trim(first.Header().Get("ETag"), `"`)
	// A once-known revision is not authority if the current database is corrupt.
	if err := repository.NewSettingRepository(db).Upsert(context.Background(), domain.SettingKeyLLMProviders, []byte(`{"secret":"`+modelHTTPSecret+`"`)); err != nil {
		t.Fatal(err)
	}
	w := bootstrapHTTPRequest(t, srv, "revision="+revision, auth, "", 500)
	if !strings.Contains(w.Body.String(), "model_config_invalid") || w.Header().Get("ETag") != "" {
		t.Fatal("corrupt storage did not fail closed")
	}
	sql, _ := db.DB()
	if err := sql.Close(); err != nil {
		t.Fatal(err)
	}
	w = bootstrapHTTPRequest(t, srv, "revision="+revision, auth, "", 500)
	if !strings.Contains(w.Body.String(), "internal server error") {
		t.Fatal("driver error not sanitized")
	}
	for _, cfg := range []*config.Config{{}, {Agent: config.AgentConfig{Enabled: true}}, {Agent: config.AgentConfig{Enabled: true, Bootstrap: config.AgentBootstrapConfig{Enabled: true}}}} {
		disabled := NewServer(cfg, Handlers{})
		bootstrapHTTPRequest(t, disabled, "", auth, "", 404)
	}
	// Both gates must suppress even a fully wired handler. A missing handler alone
	// would not catch accidental unconditional registration in Server.
	app, err := application.NewAgentBootstrapApplication(service.NewSettingService(repository.NewSettingRepository(db)), srv.cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][2]bool{{false, false}, {false, true}, {true, false}} {
		cfg := *srv.cfg
		cfg.Agent.Enabled, cfg.Agent.Bootstrap.Enabled = flags[0], flags[1]
		disabled := NewServer(&cfg, Handlers{AgentBootstrap: NewAgentBootstrapHandler(app, bootstrapHTTPControl)})
		bootstrapHTTPRequest(t, disabled, "", auth, "", 404)
	}
	if NewAgentBootstrapHandler(nil, bootstrapHTTPControl) != nil {
		t.Fatal("nil application registered")
	}
}

func TestAgentBootstrapHTTPLogRedaction(t *testing.T) {
	if os.Getenv("Q4D_BOOTSTRAP_LOG_TEST_CHILD") == "1" {
		if err := logger.Init(logger.Config{Level: "info"}); err != nil {
			t.Fatal(err)
		}
		defer logger.Shutdown()
		srv, _ := bootstrapHTTPServer(t)
		auth := []string{"Bearer " + bootstrapHTTPControl}
		first := bootstrapHTTPRequest(t, srv, "", auth, "", 200)
		bootstrapHTTPRequest(t, srv, "revision="+strings.Trim(first.Header().Get("ETag"), `"`), auth, "", 304)
		bootstrapHTTPRequest(t, srv, "token="+bootstrapHTTPControl, nil, "", 401)
		bootstrapHTTPRequest(t, srv, "revision="+modelHTTPSecret, auth, "", 400)
		bootstrapHTTPRequest(t, srv, "", auth, bootstrapHTTPMCP, 400)
		req := httptest.NewRequest("GET", agentBootstrapPath+"/"+bootstrapHTTPControl, nil)
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatal("unexpected path accepted")
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentBootstrapHTTPLogRedaction$")
	cmd.Env = append(os.Environ(), "Q4D_BOOTSTRAP_LOG_TEST_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap log subprocess failed: %v", err)
	}
	for _, logs := range [][]byte{output} {
		for _, secret := range []string{bootstrapHTTPControl, bootstrapHTTPMCP, modelHTTPSecret, "fixture-login-token", `"api_key"`, `"public_keys"`} {
			if bytes.Contains(logs, []byte(secret)) {
				t.Fatal("logs exposed bootstrap credentials, raw input or full snapshot")
			}
		}
		if !bytes.Contains(logs, []byte("agent control request")) || !bytes.Contains(logs, []byte(`"status":401`)) || !bytes.Contains(logs, []byte(`"status":200`)) {
			t.Fatal("sanitized control operation evidence missing")
		}
	}
}

func TestAgentBootstrapHTTPResponseBound(t *testing.T) {
	srv, db := bootstrapHTTPServer(t)
	providers := map[string]service.LLMProvider{}
	for i := 0; i < 64; i++ {
		providers["fixture-"+strconv.Itoa(i)] = service.LLMProvider{Type: "openai", DefaultModel: "fixture-model", APIKey: strings.Repeat("汉", 8192),
			Agent: &domain.AgentModelOptions{ContextWindow: 8192, MaxOutputTokens: 512}}
	}
	// Simulate a valid but oversized historical store, bypassing only the HTTP
	// request-size limit. No successful partial credential response is permitted.
	svc := service.NewSettingService(repository.NewSettingRepository(db))
	if err := svc.SetLLMProviders(context.Background(), providers); err != nil {
		t.Fatal(err)
	}
	w := bootstrapHTTPRequest(t, srv, "", []string{"Bearer " + bootstrapHTTPControl}, "", 500)
	if w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "汉") || !strings.Contains(w.Body.String(), "model_config_invalid") {
		t.Fatal("oversized private response was partially committed")
	}
}

func TestAgentBootstrapProfileMetadataNegotiation(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	srv, _ := bootstrapHTTPServer(t, func(cfg *config.Config) {
		cfg.Agent.Sessions = config.AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": digest}}
		cfg.Agent.Runs = config.AgentRunsConfig{Enabled: true, Profiles: map[string]config.AgentRunProfile{
			"text_only": {PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: digest},
		}}
	})
	for _, negotiate := range []bool{false, true} {
		req := httptest.NewRequest("GET", agentBootstrapPath, nil)
		req.Header.Set("Authorization", "Bearer "+bootstrapHTTPControl)
		if negotiate {
			req.Header.Set("X-Q4D-Bootstrap-Profiles", "1")
		}
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		var snapshot domain.AgentBootstrap
		if sonic.Unmarshal(w.Body.Bytes(), &snapshot) != nil {
			t.Fatal("invalid bootstrap")
		}
		if negotiate {
			if len(snapshot.Profiles) != 1 || snapshot.Profiles[0].ID != "text_only" || snapshot.Profiles[0].PromptBundleDigest != digest {
				t.Fatal("profile identity missing")
			}
		} else if snapshot.Profiles != nil || strings.Contains(w.Body.String(), `"profiles"`) {
			t.Fatal("old strict Runtime received new metadata")
		}
	}
}
