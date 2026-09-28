package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestAgentSessionRoutesRequireConfigurationAndLogin(t *testing.T) {
	for _, cfg := range []*config.Config{
		{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Sessions: config.AgentSessionsConfig{Enabled: true}}},
		{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true}},
		{Agent: config.AgentConfig{Enabled: true, Sessions: config.AgentSessionsConfig{Enabled: true}}},
	} {
		server := NewServer(cfg, Handlers{AgentSession: NewAgentSessionHandler(nil)})
		r := httptest.NewRequest("GET", "/api/v1/agent/sessions", nil)
		r.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("disabled/unauthenticated session API registered: %d", w.Code)
		}
	}
}

func TestAgentSessionMetadataOwnershipAndRuntimeIndependence(t *testing.T) {
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "metadata.db")}})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	if err := db.AutoMigrate(&domain.AgentSessionBinding{}); err != nil {
		t.Fatal(err)
	}
	const own = "01K00000000000000000000001"
	const other = "01K00000000000000000000002"
	for id, actor := range map[string]string{own: agentLocalActor, other: "other-owner"} {
		if err := db.Create(&domain.AgentSessionBinding{ID: id, ActorID: actor, ProvisionRequestKey: id, Status: domain.AgentSessionActive, Title: "会话主题", TitleGeneration: 2, TitleSettledGeneration: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	app, err := application.NewAgentSessionApplication(service.NewSettingService(repository.NewSettingRepository(db)),
		service.NewAgentSessionService(repository.NewAgentSessionRepository(db)), service.NewAgentSessionRuntimeService(nil), map[string]string{"text_only": "sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-login"}, Agent: config.AgentConfig{Enabled: true, Sessions: config.AgentSessionsConfig{Enabled: true}}}
	server := NewServer(cfg, Handlers{AgentSession: NewAgentSessionHandler(app)})
	for _, tc := range []struct {
		id    string
		login bool
		code  int
	}{{own, true, 200}, {own, false, 401}, {other, true, 404}, {own + "?title=override", true, 400}} {
		req := httptest.NewRequest("GET", "/api/v1/agent/sessions/"+strings.Split(tc.id, "?")[0]+"/metadata", nil)
		if strings.Contains(tc.id, "?") {
			req.URL.RawQuery = strings.SplitN(tc.id, "?", 2)[1]
		}
		if tc.login {
			req.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login"})
		}
		res := httptest.NewRecorder()
		server.engine.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatalf("metadata: got %d, want %d: %s", res.Code, tc.code, res.Body.String())
		}
		if tc.code == 200 {
			var value service.AgentSessionView
			if err := sonic.Unmarshal(res.Body.Bytes(), &value); err != nil || value.Title != "会话主题" || !value.TitlePending || value.TitleRevision != 3 || strings.Contains(res.Body.String(), "transcript") {
				t.Fatal(value, err)
			}
		}
	}
}
