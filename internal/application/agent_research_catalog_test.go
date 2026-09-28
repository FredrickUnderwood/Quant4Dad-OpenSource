package application

import (
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/service"
)

func TestResearchProductCatalogGrantAndCurrentScope(t *testing.T) {
	base, _, settings, runtime, db, id := runAppFixture(t)
	ctx := context.Background()
	cfg := runTestConfig()
	cfg.Agent.Sessions.Profiles = map[string]string{"research": sessionTestDigest}
	p := cfg.Agent.Runs.Profiles["text_only"]
	p.Budgets.MaxToolCalls = 2
	p.ToolCatalogRevision = "" // Derived from server definitions, never YAML.
	cfg.Agent.Runs.Profiles = map[string]config.AgentRunProfile{"research": p}
	cfg.Agent.Gateway = config.AgentGatewayConfig{Enabled: true, ResultDirectory: filepath.Join(t.TempDir(), "results")}
	if err := db.Model(&domain.AgentSessionBinding{}).Where("id = ?", id).Update("profile", "research").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentRunApplication(settings, base.sessions, base.runs, base.runtime, cfg); err == nil {
		t.Fatal("missing catalog accepted")
	}
	catalog := NewToolCatalogApplication()
	for _, tool := range MarketToolDefinitions(service.NewInstrumentService(nil, nil)) {
		catalog.Register(tool)
	}
	app, err := NewAgentRunApplication(settings, base.sessions, base.runs, base.runtime, cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	runtime.unavailable = true
	sent, err := app.Send(ctx, "local-user", id, runTestMessage())
	if !errors.Is(err, service.ErrAgentRunUnavailable) || sent.RunID == "" {
		t.Fatal("missing prepared grant", err)
	}
	claims, err := app.Authorization(ctx, sent.RunID)
	if err != nil || !slices.Equal(claims.AllowedTools, []string{"get_instrument", "latest_bar_date", "list_instruments", "query_kline"}) || claims.Envelope.ToolCatalogRevision != catalog.Revision("research") || app.TextOnly() {
		t.Fatal("incorrect research authority", err)
	}
	bootstrap, err := NewAgentBootstrapApplication(settings, cfg, catalog)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := bootstrap.Read(ctx, "")
	if err != nil || snapshot.ToolCatalog.Revision != claims.Envelope.ToolCatalogRevision {
		t.Fatal("catalog snapshots diverged", err)
	}
	snapshot.ToolCatalog.Tools[0].InputSchema["type"] = "array"
	again, _, err := bootstrap.Read(ctx, "")
	if err != nil || again.ToolCatalog.Tools[0].InputSchema["type"] != "object" {
		t.Fatal("mutable bootstrap metadata")
	}
	claims.AllowedTools = []string{"query_kline"}
	raw, _ := sonic.Marshal(claims)
	if err := db.Model(&domain.AgentRequestBinding{}).Where("id = ?", sent.RunID).Update("claims_json", string(raw)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := app.Authorization(ctx, sent.RunID); !errors.Is(err, service.ErrAgentRunRejected) {
		t.Fatal("altered stored scope accepted", err)
	}
}

// Exercise the production composition (including the owned-file adapter), not
// only MarketToolDefinitions, through bootstrap, signed grants and live policy.
func TestKlineAnalysisProductionCatalogBootstrapAndAuthority(t *testing.T) {
	for _, profile := range []string{"research", "strategy_lab"} {
		t.Run(profile, func(t *testing.T) {
			base, _, settings, runtime, db, session := runAppFixture(t)
			ctx := context.Background()
			cfg := runTestConfig()
			cfg.Agent.Sessions.Profiles = map[string]string{profile: sessionTestDigest}
			p := cfg.Agent.Runs.Profiles["text_only"]
			p.Budgets.MaxToolCalls, p.ToolCatalogRevision = 2, ""
			cfg.Agent.Runs.Profiles = map[string]config.AgentRunProfile{profile: p}
			cfg.Agent.Gateway = config.AgentGatewayConfig{Enabled: true, ResultDirectory: filepath.Join(t.TempDir(), "results")}
			if err := db.Model(&domain.AgentSessionBinding{}).Where("id = ?", session).Update("profile", profile).Error; err != nil {
				t.Fatal(err)
			}
			pipeline := service.NewPipelineService(nil, nodes.BuildRegistry(nil, nil))
			catalog := NewAgentP0Catalog(&service.InstrumentService{}, service.NewIndicatorService(), service.NewStrategyService(nil), pipeline, &service.EventQueryService{}, &service.AgentCatalogQueryService{}, &service.AgentMutationService{}, &service.CostService{}, service.NewAgentKlineFileService(nil, catalogPythonExecutor{}))
			bootstrap, err := NewAgentBootstrapApplication(settings, cfg, catalog)
			if err != nil {
				t.Fatal("production catalog rejected by bootstrap", err)
			}
			snapshot, _, err := bootstrap.Read(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, c := range snapshot.ToolCatalogs {
				if c.Profile != profile {
					continue
				}
				for _, tool := range c.Tools {
					if tool.Name == "analyze_kline" {
						found = tool.Risk == "R0" && c.Revision == catalog.Revision(profile)
					}
				}
			}
			if !found {
				t.Fatal("analysis missing from authenticated bootstrap")
			}
			app, err := NewAgentRunApplication(settings, base.sessions, base.runs, base.runtime, cfg, catalog)
			if err != nil {
				t.Fatal("production catalog rejected by run authorizer", err)
			}
			public := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
			runtime.verify, err = agentrunauth.NewVerifier("fixture-issuer", agentrunauth.RuntimeAudience, map[string]ed25519.PublicKey{"key-1": public}, func(ctx context.Context, c agentrunauth.Claims) (bool, error) {
				expected, err := app.Authorization(ctx, c.Envelope.RunID)
				return err == nil && expected.JTI == c.JTI, err
			})
			if err != nil {
				t.Fatal(err)
			}
			sent, err := app.Send(ctx, "local-user", session, runTestMessage())
			if err != nil {
				t.Fatal(err)
			}
			claims, err := app.Authorization(ctx, sent.RunID)
			if err != nil || !slices.Contains(claims.AllowedTools, "analyze_kline") || !slices.Contains(claims.AllowedTools, "execute_python") || claims.Envelope.ToolCatalogRevision != catalog.Revision(profile) {
				t.Fatal("analysis missing from signed grant", err)
			}
			runtime.mu.Lock()
			run := runtime.runs[sent.RunID]
			run.State, run.Terminal = "running", false
			runtime.runs[sent.RunID] = run
			runtime.mu.Unlock()
			if allowed, err := app.ToolAuthority(ctx, claims); err != nil || !allowed {
				t.Fatal("analysis scope rejected by live authority", err)
			}
			claims.AllowedTools = []string{"analyze_kline"}
			if allowed, _ := app.ToolAuthority(ctx, claims); allowed {
				t.Fatal("tampered analysis-only grant accepted")
			}
		})
	}
}
