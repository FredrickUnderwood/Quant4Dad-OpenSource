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
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/repository/coldstore"
	"github.com/quant4dad/internal/repository/datasource"
	_ "github.com/quant4dad/internal/repository/datasource/providers"
	"github.com/quant4dad/internal/service"
)

func integrationHTTPFixture(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte("server:\n  addr: ':8080'\nsecurity:\n  token: fixture-login-token\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := repository.NewIntegrationSettingsRepository(filepath.Join(t.TempDir(), "private", "integrations.yaml"), config.InitialIntegrations(cfg))
	if err != nil {
		t.Fatal(err)
	}
	s, err := service.NewIntegrationSettingService(r)
	if err != nil {
		t.Fatal(err)
	}
	ds := application.NewDataSyncApp(cfg.Datasource, nil, nil, nil, datasource.Unavailable())
	auto, err := application.NewAutoSyncScheduler(ds, false, "03:00")
	if err != nil {
		t.Fatal(err)
	}
	a, err := application.NewIntegrationSettingsApplication(s, ds, auto, func(c config.ArchiveConfig, cs coldstore.ColdStore) (*application.ArchiveScheduler, error) {
		return application.NewArchiveScheduler(nil, cs, c)
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(cfg, Handlers{IntegrationSetting: NewIntegrationSettingHandler(a, cfg, false), Archive: NewArchiveHandler(a)})
}
func TestIntegrationHTTPAuthorizationStrictBodyAndSecrets(t *testing.T) {
	srv := integrationHTTPFixture(t)
	for _, path := range []string{"/api/v1/settings/integrations", "/api/v1/settings/deployment"} {
		w := httptest.NewRecorder()
		srv.engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 401 {
			t.Fatal("settings accessible without authentication")
		}
	}
	const path = "/api/v1/settings/integrations"
	const secret = "synthetic-sensitive-settings-token"
	body := `{"expected_revision":0,"market":{"provider":"tushare","initial_years":10,"concurrency":2,"rate_limit_per_min":60,"include_etf":false,"tushare":{"token":{"action":"replace","value":"` + secret + `"}},"http":{"base_url":"","token":{"action":"keep"}},"auto_sync":{"enabled":false,"daily_time":"03:00"}}}`
	w := modelHTTPRequest(t, srv, "PUT", path, body, "", 200)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("saved credential echoed")
	}
	var view service.IntegrationSettingsView
	if err := sonic.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Revision != 1 || !view.Market.Tushare.HasToken || view.BackgroundEnabled {
		t.Fatal("wrong saved settings view")
	}
	modelHTTPRequest(t, srv, "PUT", path, body, "", 409)
	for _, bad := range []string{`{"expected_revision":1,"secret":"` + secret + `"}`, `{"expected_revision":1,"market":null}`, `{"expected_revision":1,"expected_revision":1}`, `{"expected_revision":1,"market":{"tushare":{"token":{"value":"` + secret + `"}}}}`} {
		w := modelHTTPRequest(t, srv, "PUT", path, bad, "", 400)
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("parser error exposed secret")
		}
	}
	w = modelHTTPRequest(t, srv, "GET", path, "", "", 200)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("get exposed secret")
	}
	modelHTTPRequest(t, srv, "POST", path+"/test-oss", `{"expected_revision":0}`, "", 409)
	modelHTTPRequest(t, srv, "POST", path+"/test-oss", `{"expected_revision":1}`, "", 400)
	modelHTTPRequest(t, srv, "GET", "/api/v1/settings/deployment", "", "", 200)
}
