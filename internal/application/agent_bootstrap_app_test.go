package application

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func bootstrapAppFixture(t *testing.T) (*AgentBootstrapApplication, *service.SettingService, *config.Config) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "bootstrap.db") + "?_busy_timeout=5000"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Setting{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := db.DB(); _ = sql.Close() })
	svc := service.NewSettingService(repository.NewSettingRepository(db))
	if err := svc.SetLLMProviders(context.Background(), map[string]service.LLMProvider{"fixture": {
		Type: "openai", BaseURL: "http://localhost:11434/v1", DefaultModel: "fixture-model", APIKey: "fixture-model-private-key",
		Agent: &domain.AgentModelOptions{ContextWindow: 8192, MaxOutputTokens: 512},
	}}); err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-user-token"}, Agent: config.AgentConfig{Enabled: true,
		Bootstrap: config.AgentBootstrapConfig{Enabled: true, ControlToken: "fixture-control-token-0123456789-abcdef", MCPToken: "fixture-mcp-token-0123456789-abcdef",
			MCPURL: "http://mcp:8081/internal/mcp", CapabilityIssuer: "q4d-fixture", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key)}}}}
	app, err := NewAgentBootstrapApplication(svc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return app, svc, cfg
}

func TestAgentBootstrapConditionalAndRotation(t *testing.T) {
	app, svc, cfg := bootstrapAppFixture(t)
	ctx := context.Background()
	first, unchanged, err := app.Read(ctx, "")
	if err != nil || unchanged || !bootstrapRevisionPattern.MatchString(first.Revision) || first.ModelConfigRevision == first.Revision {
		t.Fatal("invalid initial bootstrap")
	}
	for i := 0; i < 3; i++ {
		next, unchanged, err := app.Read(ctx, first.Revision)
		if err != nil || !unchanged || next.Revision != first.Revision || next.Providers != nil || next.MCP.RuntimeToken != "" || next.Capability.PublicKeys != nil {
			t.Fatal("conditional response not stable or retains secrets")
		}
	}
	key := "replacement-model-key"
	if _, err := svc.PatchLLMProvider(ctx, "fixture", service.LLMProviderPatch{APIKeyUpdate: &service.ModelCredentialUpdate{Action: "replace", Value: &key}}, ""); err != nil {
		t.Fatal(err)
	}
	next, unchanged, err := app.Read(ctx, first.Revision)
	if err != nil || unchanged || next.ModelConfigRevision == first.ModelConfigRevision || next.Providers[0].APIKey != key {
		t.Fatal("model rotation hidden by conditional cache")
	}
	// Control config rotates only on restart/reconstruction. Same model revision
	// must not produce a 304 with old MCP credentials or trusted key set.
	cfg.Agent.Bootstrap.MCPToken = "rotated-mcp-token-0123456789-abcdef"
	cfg.Agent.Bootstrap.CapabilityPublicKeys["key-2"] = cfg.Agent.Bootstrap.CapabilityPublicKeys["key-1"]
	delete(cfg.Agent.Bootstrap.CapabilityPublicKeys, "key-1")
	rotated, err := NewAgentBootstrapApplication(svc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, unchanged, err := rotated.Read(ctx, next.Revision)
	if err != nil || unchanged || after.Revision == next.Revision || after.ModelConfigRevision != next.ModelConfigRevision || after.MCP.RuntimeToken == next.MCP.RuntimeToken || after.Capability.PublicKeys["key-2"] == "" {
		t.Fatal("control restart did not force a full refresh")
	}
	old, _, _ := app.Read(ctx, "")
	if old.Capability.PublicKeys["key-1"] == "" || len(old.Capability.PublicKeys) != 1 || old.MCP.RuntimeToken != first.MCP.RuntimeToken {
		t.Fatal("caller changed live control configuration")
	}
	old.Capability.PublicKeys["key-1"] = "corrupted"
	again, _, _ := app.Read(ctx, "")
	if again.Capability.PublicKeys["key-1"] == "corrupted" {
		t.Fatal("response aliases trusted key map")
	}
}

func TestAgentBootstrapWireFixtureAndRedaction(t *testing.T) {
	app, _, _ := bootstrapAppFixture(t)
	got, _, err := app.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../agent-runtime/contracts/bootstrap-v1/fixtures/bootstrap.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var want domain.AgentBootstrap
	if err := sonic.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	got.Revision, got.ModelConfigRevision = want.Revision, want.ModelConfigRevision
	// Compare complete JSON trees too: typed decoding alone could ignore fields.
	encoded, err := sonic.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotTree, wantTree any
	if sonic.Unmarshal(encoded, &gotTree) != nil || sonic.Unmarshal(data, &wantTree) != nil || !reflect.DeepEqual(gotTree, wantTree) {
		t.Fatal("Go bootstrap wire differs from shared schema fixture")
	}
	for _, value := range []any{got, got.Providers[0], got.MCP, app} {
		output := fmt.Sprintf("%v %+v %#v", value, value, value)
		if strings.Contains(output, "private-key") || strings.Contains(output, "fixture-mcp-token") || strings.Contains(output, "fixture-control-token") {
			t.Fatal("private bootstrap structure leaked through formatting")
		}
	}
}

func TestAgentBootstrapRejectsInvalidAndUnavailableState(t *testing.T) {
	app, svc, cfg := bootstrapAppFixture(t)
	if _, err := NewAgentBootstrapApplication(nil, cfg); err != config.ErrAgentBootstrapConfig {
		t.Fatal("nil settings accepted")
	}
	if _, err := NewAgentBootstrapApplication(svc, nil); err != config.ErrAgentBootstrapConfig {
		t.Fatal("nil config accepted")
	}
	for _, known := range []string{"secret", strings.Repeat("a", 32), strings.Repeat("a", 32) + "." + strings.Repeat("B", 32), strings.Repeat("a", 32) + "." + strings.Repeat("b", 32) + "\n"} {
		if _, _, err := app.Read(context.Background(), known); err != service.ErrModelInputInvalid {
			t.Fatal("invalid conditional revision accepted")
		}
	}
	first, _, err := app.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, unchanged, err := app.Read(ctx, first.Revision); err == nil || unchanged || out.MCP.RuntimeToken != "" {
		t.Fatal("unavailable current state returned 304 or secrets")
	}
}

func TestAgentBootstrapConcurrentReads(t *testing.T) {
	app, svc, _ := bootstrapAppFixture(t)
	initial, _, err := app.Read(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]string{initial.ModelConfigRevision: initial.Providers[0].DefaultModel}
	type pair struct{ revision, model string }
	observed := make(chan pair, 240)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				out, _, err := app.Read(context.Background(), "")
				if err != nil {
					errs <- err
					return
				}
				if !strings.HasSuffix(out.Revision, "."+out.ModelConfigRevision) || len(out.Providers) != 1 {
					t.Error("torn response")
					return
				}
				observed <- pair{out.ModelConfigRevision, out.Providers[0].DefaultModel}
				out.Capability.PublicKeys["key-1"] = "caller-change"
			}
		}()
	}
	for i := 0; i < 8; i++ {
		model := fmt.Sprintf("fixture-%d", i)
		view, err := svc.PatchLLMProvider(context.Background(), "fixture", service.LLMProviderPatch{DefaultModel: &model}, "")
		if err != nil {
			t.Fatal(err)
		}
		written[view.Revision] = model
	}
	wg.Wait()
	close(errs)
	close(observed)
	for err := range errs {
		t.Fatal(err)
	}
	for p := range observed {
		if written[p.revision] != p.model {
			t.Fatal("response mixed model data from a different committed revision")
		}
	}
	out, _, err := app.Read(context.Background(), "")
	if err != nil || out.Providers[0].DefaultModel != "fixture-7" || out.Capability.PublicKeys["key-1"] == "caller-change" {
		t.Fatal("final snapshot is not current or isolated")
	}
}
