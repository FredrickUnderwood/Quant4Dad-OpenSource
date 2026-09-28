package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quant4dad/config"
)

func TestAgentRunRoutesRequireConfigurationAndLogin(t *testing.T) {
	for _, cfg := range []*config.Config{
		{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Runs: config.AgentRunsConfig{Enabled: true}}},
		{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true, Sessions: config.AgentSessionsConfig{Enabled: true}}},
		{Agent: config.AgentConfig{Enabled: true, Sessions: config.AgentSessionsConfig{Enabled: true}, Runs: config.AgentRunsConfig{Enabled: true}}},
	} {
		server := NewServer(cfg, Handlers{AgentRun: &AgentRunHandler{}})
		for _, path := range []string{"/runs/01K00000000000000000000000", "/runs/01K00000000000000000000000/events", "/options"} {
			r := httptest.NewRequest("GET", "/api/v1/agent"+path, nil)
			r.AddCookie(&http.Cookie{Name: authCookieName, Value: "fixture-login-token"})
			w := httptest.NewRecorder()
			server.engine.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("disabled Run API registered: %s %d", path, w.Code)
			}
		}
	}
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-login-token"}, Agent: config.AgentConfig{Enabled: true, Sessions: config.AgentSessionsConfig{Enabled: true}, Runs: config.AgentRunsConfig{Enabled: true}}}
	server := NewServer(cfg, Handlers{AgentRun: &AgentRunHandler{}})
	for _, path := range []string{"/options", "/runs/01K00000000000000000000000/events"} {
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/agent"+path, nil))
		if w.Code != 401 {
			t.Fatalf("unauthenticated route: %s %d", path, w.Code)
		}
	}
}
