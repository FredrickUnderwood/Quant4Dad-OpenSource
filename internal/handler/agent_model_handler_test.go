package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

const modelHTTPSecret = "private-model-http-fixture-key"

func modelHTTPServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "model-http.db")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Setting{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	svc := service.NewSettingService(repository.NewSettingRepository(db))
	srv := NewServer(&config.Config{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true}}, Handlers{Setting: NewSettingHandler(svc)})
	return srv, db
}

func modelHTTPRequest(t *testing.T, srv *Server, method, path, body, etag string, status int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != status {
		t.Fatalf("%s %s returned %d, want %d", method, path, w.Code, status)
	}
	if strings.Contains(w.Body.String(), modelHTTPSecret) {
		t.Fatal("HTTP response exposed credential")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("model response may be cached")
	}
	return w
}

func modelHTTPSeed(t *testing.T, srv *Server) string {
	t.Helper()
	body := `{"providers":{"fixture":{"type":"openai","base_url":"http://localhost:11434/v1","default_model":"fixture-model","api_key":"` + modelHTTPSecret + `","agent":{"enabled":true,"context_window":8192,"max_output_tokens":512}}}}`
	w := modelHTTPRequest(t, srv, "PUT", "/api/v1/settings/llm-providers", body, "", 200)
	if !modelETagPattern.MatchString(w.Header().Get("ETag")) {
		t.Fatal("missing opaque ETag")
	}
	return w.Header().Get("ETag")
}

func TestAgentModelHTTPMutationContract(t *testing.T) {
	srv, _ := modelHTTPServer(t)
	etag := modelHTTPSeed(t, srv)
	const settings = "/api/v1/settings/llm-providers"
	// The existing browser only sends the four legacy fields.
	w := modelHTTPRequest(t, srv, "PUT", settings, `{"providers":{"fixture":{"type":"openai","base_url":"http://localhost:11434/v1","default_model":"fixture-model","api_key":""}}}`, etag, 200)
	var view service.LLMProvidersView
	if err := sonic.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("ETag") != etag || !view.Providers["fixture"].HasAPIKey || view.Providers["fixture"].Agent.ContextWindow != 8192 {
		t.Fatal("legacy compatibility lost")
	}
	w = modelHTTPRequest(t, srv, "PATCH", settings+"/fixture", `{"agent":{"enabled":false},"api_key_update":{"action":"keep"}}`, etag, 200)
	changed := w.Header().Get("ETag")
	if changed == etag {
		t.Fatal("mutation did not advance revision")
	}
	modelHTTPRequest(t, srv, "PATCH", settings+"/fixture", `{"default_model":"stale-model"}`, etag, 412)
	modelHTTPRequest(t, srv, "PUT", settings, `{"providers":{}}`, etag, 412)
	modelHTTPRequest(t, srv, "DELETE", settings+"/fixture", "", etag, 412)
	w = modelHTTPRequest(t, srv, "GET", "/api/v1/agent/models", "", "", 200)
	var catalog service.AgentModelCatalog
	if err := sonic.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].Reason != "disabled" || w.Header().Get("ETag") != changed {
		t.Fatal("catalog is not current")
	}
	modelHTTPRequest(t, srv, "PATCH", settings+"/fixture", `{"api_key_update":{"action":"revoke"}}`, changed, 200)
	w = modelHTTPRequest(t, srv, "GET", settings, "", "", 200)
	if err := sonic.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Providers["fixture"].HasAPIKey || !view.Providers["fixture"].CredentialRevoked {
		t.Fatal("revoke not persisted")
	}
	modelHTTPRequest(t, srv, "DELETE", settings+"/fixture", "", w.Header().Get("ETag"), 200)
	modelHTTPRequest(t, srv, "DELETE", settings+"/fixture", "", "", 404)
	w = modelHTTPRequest(t, srv, "GET", "/api/v1/agent/models", "", "", 200)
	if !strings.Contains(w.Body.String(), `"models":[]`) {
		t.Fatal("deleted provider remains visible")
	}
}

func TestAgentModelHTTPRejectsInvalidBodies(t *testing.T) {
	srv, _ := modelHTTPServer(t)
	etag := modelHTTPSeed(t, srv)
	const path = "/api/v1/settings/llm-providers/fixture"
	for i, body := range []string{
		`null`, `[]`, `{"agent":null}`, `{"agent":{"enabled":null}}`,
		`{"api_key_update":{"action":"revoke","value":"` + modelHTTPSecret + `"}}`,
		`{"api_key_update":{"action":"replace","value":123}}`,
		`{"api_key_update":{"action":"replace","value":"` + modelHTTPSecret + `"}`, // syntax failure
		`{"api_key":"` + modelHTTPSecret + `"}`, `{"credential_revoked":false}`, `{"Agent":{}}`,
		`{"agent":{"unknown":true}}`, `{} {}`, `{"default_model":"\ud800"}`,
		`{"default_model":"\udc00"}`, `{"default_model":"\ud800\u0041"}`,
	} {
		t.Run(strconv.Itoa(i), func(t *testing.T) { modelHTTPRequest(t, srv, "PATCH", path, body, "", 400) })
	}
	for _, body := range []string{`{}`, `{"providers":null}`, `{"providers":{"fixture":null}}`} {
		modelHTTPRequest(t, srv, "PUT", "/api/v1/settings/llm-providers", body, "", 400)
	}
	modelHTTPRequest(t, srv, "PATCH", path, `{}`, `W/`+etag, 400)
	modelHTTPRequest(t, srv, "PATCH", path, strings.Repeat("x", (256<<10)+1), "", 413)
	w := modelHTTPRequest(t, srv, "GET", "/api/v1/settings/llm-providers", "", "", 200)
	if w.Header().Get("ETag") != etag {
		t.Fatal("invalid request mutated revision")
	}
	req := httptest.NewRequest("PATCH", path, strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
	w = httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal("wrong content type accepted")
	}
	// Valid surrogate pairs, raw Unicode and an escaped literal backslash survive
	// replacement unchanged; the credential value is never returned in the view.
	for _, body := range []string{
		`{"api_key_update":{"action":"replace","value":"\ud83d\ude00"}}`,
		`{"api_key_update":{"action":"replace","value":"\u4e2d\u6587"}}`,
		`{"api_key_update":{"action":"replace","value":"\\ud800"}}`,
	} {
		modelHTTPRequest(t, srv, "PATCH", path, body, "", 200)
	}
}

func TestAgentModelHTTPRequiresExistingLogin(t *testing.T) {
	srv, _ := modelHTTPServer(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/agent/models"}, {"GET", "/api/v1/settings/llm-providers"},
		{"PATCH", "/api/v1/settings/llm-providers/fixture"}, {"DELETE", "/api/v1/settings/llm-providers/fixture"},
	} {
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
		if w.Code != 401 {
			t.Fatal("model route bypassed login")
		}
	}
}

func TestAgentModelHTTPDisabledByDefault(t *testing.T) {
	_, db := modelHTTPServer(t)
	svc := service.NewSettingService(repository.NewSettingRepository(db))
	srv := NewServer(&config.Config{Security: config.SecurityConfig{Token: "fixture-login-token"}}, Handlers{Setting: NewSettingHandler(svc)})
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/v1/agent/models"},
		{"PATCH", "/api/v1/settings/llm-providers/fixture"}, {"DELETE", "/api/v1/settings/llm-providers/fixture"},
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(route.method, route.path, nil)
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
		srv.engine.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatal("new Agent route enabled by default")
		}
	}
	// Upgrading preserves the existing UI contract with Agent disabled.
	modelHTTPSeed(t, srv)
	modelHTTPRequest(t, srv, "GET", "/api/v1/settings/llm-providers", "", "", 200)
}

func TestAgentModelHTTPErrorAndLogRedaction(t *testing.T) {
	if os.Getenv("Q4D_MODEL_LOG_TEST_CHILD") == "1" {
		if err := logger.Init(logger.Config{Level: "info"}); err != nil {
			t.Fatal(err)
		}
		defer logger.Shutdown()
		srv, db := modelHTTPServer(t)
		modelHTTPSeed(t, srv)
		// A driver error can contain a credential; neither the repository log nor
		// the public error response may print it.
		if err := db.Exec("CREATE TRIGGER reject_model_update BEFORE INSERT ON setting WHEN NEW.key = 'agent.model.revision' BEGIN SELECT RAISE(ABORT, '" + modelHTTPSecret + "'); END").Error; err != nil {
			t.Fatal("failed to install fault")
		}
		modelHTTPRequest(t, srv, "PATCH", "/api/v1/settings/llm-providers/fixture", `{"default_model":"changed"}`, "", 500)
		modelHTTPRequest(t, srv, "PATCH", "/api/v1/settings/llm-providers/fixture", `{"api_key_update":{"action":"replace","value":"`+modelHTTPSecret+`"}`, "", 400)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAgentModelHTTPErrorAndLogRedaction$")
	cmd.Env = append(os.Environ(), "Q4D_MODEL_LOG_TEST_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("model log subprocess failed: %v", err)
	}
	for _, logs := range [][]byte{output} {
		if bytes.Contains(logs, []byte(modelHTTPSecret)) || bytes.Contains(logs, []byte(`"api_key"`)) {
			t.Fatal("logs exposed credential or full provider JSON")
		}
		if !bytes.Contains(logs, []byte("model settings transaction failed")) || !bytes.Contains(logs, []byte("llm providers updated")) {
			t.Fatal("expected operation evidence missing")
		}
	}
}
