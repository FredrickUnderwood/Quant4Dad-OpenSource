package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func modelPtr[T any](v T) *T { return &v }

func modelService(t *testing.T) (*SettingService, *gorm.DB) {
	t.Helper()
	db, err := repository.Open(config.StorageConfig{Backend: config.StorageBackendSQLite, SQLite: config.SQLiteConfig{Path: filepath.Join(t.TempDir(), "models.db") + "?_busy_timeout=5000"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.Setting{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	return NewSettingService(repository.NewSettingRepository(db)), db
}

func modelFixture() LLMProvider {
	return LLMProvider{Type: "openai", BaseURL: "http://localhost:11434/v1", DefaultModel: "fixture-model", APIKey: "fixture-private-model-key",
		Agent: &AgentModelOptions{Enabled: modelPtr(true), Protocol: "openai-completions", ContextWindow: 8192, MaxOutputTokens: 512}}
}

func requireModelSnapshot(t *testing.T, s *SettingService) LLMModelSnapshot {
	t.Helper()
	v, err := s.GetLLMModelSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !modelRevisionPattern.MatchString(v.Revision) {
		t.Fatal("invalid opaque revision")
	}
	return v
}

func TestAgentModelLegacyMergeAndRevision(t *testing.T) {
	s, db := modelService(t)
	ctx := context.Background()
	// Simulate an installation with provider JSON predating the revision key.
	p := modelFixture()
	value, _ := sonic.Marshal(map[string]LLMProvider{"fixture": p})
	if err := repository.NewSettingRepository(db).Upsert(ctx, domain.SettingKeyLLMProviders, value); err != nil {
		t.Fatal(err)
	}
	initial := requireModelSnapshot(t, s)
	if next := requireModelSnapshot(t, s); next.Revision != initial.Revision {
		t.Fatal("read regenerated revision")
	}
	p.Agent, p.APIKey = nil, ""
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": p}); err != nil {
		t.Fatal(err)
	}
	kept := requireModelSnapshot(t, s)
	if kept.Revision != initial.Revision || kept.Providers["fixture"].APIKey == "" || kept.Providers["fixture"].Agent.ContextWindow != 8192 {
		t.Fatal("legacy save lost metadata/key or changed semantic no-op revision")
	}
	p.DefaultModel = "changed-model"
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": p}); err != nil {
		t.Fatal(err)
	}
	changed := requireModelSnapshot(t, s)
	if changed.Revision == initial.Revision {
		t.Fatal("changed config reused revision")
	}
	other := NewSettingService(repository.NewSettingRepository(db))
	if requireModelSnapshot(t, other).Revision != changed.Revision {
		t.Fatal("revision was process-local")
	}
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{}); err != nil {
		t.Fatal(err)
	}
	empty := requireModelSnapshot(t, s)
	if len(empty.Providers) != 0 || empty.Revision == changed.Revision {
		t.Fatal("legacy full-map delete failed")
	}
}

func TestAgentModelCredentialLifecycle(t *testing.T) {
	s, _ := modelService(t)
	ctx := context.Background()
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": modelFixture()}); err != nil {
		t.Fatal(err)
	}
	before := requireModelSnapshot(t, s)
	view, err := s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "revoke"}}, before.Revision)
	if err != nil || view.Revision == before.Revision || view.Providers["fixture"].HasAPIKey || !view.Providers["fixture"].CredentialRevoked {
		t.Fatal("revoke failed")
	}
	revoked := requireModelSnapshot(t, s)
	if revoked.Providers["fixture"].APIKey != "" {
		t.Fatal("revoked key still persisted")
	}
	// Both the old editor and enabling Agent must retain explicit revocation.
	p := modelFixture()
	p.APIKey, p.Agent = "", nil
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": p}); err != nil {
		t.Fatal(err)
	}
	_, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{Agent: &AgentModelOptionsPatch{Enabled: modelPtr(true)}}, "")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Reason != "credential_revoked" {
		t.Fatal("revocation was cleared without replacement")
	}
	if requireModelSnapshot(t, s).Revision != revoked.Revision {
		t.Fatal("no-op save changed revoked revision")
	}
	view, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "replace", Value: modelPtr("replacement-fixture-key")}}, "")
	if err != nil || !view.Providers["fixture"].HasAPIKey || view.Providers["fixture"].CredentialRevoked || view.Revision == revoked.Revision {
		t.Fatal("replacement failed")
	}
	catalog, err = s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Status != "unverified" {
		t.Fatal("replacement incorrectly retained revoke or bypassed probe")
	}
}

func TestAgentModelPatchValidationAndPrecondition(t *testing.T) {
	s, _ := modelService(t)
	ctx := context.Background()
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": modelFixture()}); err != nil {
		t.Fatal(err)
	}
	before := requireModelSnapshot(t, s)
	invalid := []LLMProviderPatch{
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "replace"}},
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "replace", Value: modelPtr("  ")}},
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "revoke", Value: modelPtr("")}},
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "keep", Value: modelPtr("forbidden")}},
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "unknown"}},
		{BaseURL: modelPtr("https://user:password@example.com")},
		{BaseURL: modelPtr("https://example.com?api_key=private")},
		{BaseURL: modelPtr("file:///tmp/model")},
		{Type: modelPtr("unsupported")},
		{Agent: &AgentModelOptionsPatch{ContextWindow: modelPtr(-1)}},
		{Agent: &AgentModelOptionsPatch{MaxOutputTokens: modelPtr(8192)}},
		{Agent: &AgentModelOptionsPatch{Protocol: modelPtr("anthropic-messages")}},
	}
	for i, patch := range invalid {
		if _, err := s.PatchLLMProvider(ctx, "fixture", patch, ""); !errors.Is(err, ErrModelInputInvalid) {
			t.Fatalf("case %d: expected invalid", i)
		}
		if requireModelSnapshot(t, s).Revision != before.Revision {
			t.Fatal("invalid patch changed revision")
		}
	}
	if _, err := s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{}, "stale"); !errors.Is(err, ErrModelRevisionStale) {
		t.Fatal("stale patch accepted")
	}
	if _, err := s.DeleteLLMProvider(ctx, "fixture", "stale"); !errors.Is(err, ErrModelRevisionStale) {
		t.Fatal("stale delete accepted")
	}
	if _, err := s.ReplaceLLMProviders(ctx, map[string]LLMProvider{}, "stale"); !errors.Is(err, ErrModelRevisionStale) {
		t.Fatal("stale replacement accepted")
	}
	if _, err := s.PatchLLMProvider(ctx, "missing", LLMProviderPatch{}, ""); !errors.Is(err, ErrModelNotFound) {
		t.Fatal("patch created missing provider")
	}
	if _, err := s.DeleteLLMProvider(ctx, "fixture", before.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteLLMProvider(ctx, "fixture", ""); !errors.Is(err, ErrModelNotFound) {
		t.Fatal("missing delete accepted")
	}
}

func TestAgentModelConcurrentFieldPatches(t *testing.T) {
	s, db := modelService(t)
	ctx := context.Background()
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": modelFixture()}); err != nil {
		t.Fatal(err)
	}
	patches := []LLMProviderPatch{
		{DefaultModel: modelPtr("changed-model")},
		{BaseURL: modelPtr("http://localhost:1234/v1")},
		{Agent: &AgentModelOptionsPatch{ContextWindow: modelPtr(16384)}},
		{APIKeyUpdate: &ModelCredentialUpdate{Action: "replace", Value: modelPtr("rotated-fixture-key")}},
	}
	results := make(chan error, len(patches))
	for _, patch := range patches {
		go func() {
			_, err := NewSettingService(repository.NewSettingRepository(db)).PatchLLMProvider(ctx, "fixture", patch, "")
			results <- err
		}()
	}
	for range patches {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	p := requireModelSnapshot(t, s).Providers["fixture"]
	if p.DefaultModel != "changed-model" || p.BaseURL != "http://localhost:1234/v1" || p.Agent.ContextWindow != 16384 || p.APIKey != "rotated-fixture-key" {
		t.Fatal("concurrent merge lost a field")
	}
}

func TestAgentModelCatalogAndRedaction(t *testing.T) {
	s, _ := modelService(t)
	ctx := context.Background()
	keyless, disabled, missing, badID := modelFixture(), modelFixture(), modelFixture(), modelFixture()
	keyless.APIKey = ""
	disabled.Agent.Enabled = modelPtr(false)
	missing.Agent = nil
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"a-keyless": keyless, "b-disabled": disabled, "c-missing": missing, "d/bad": badID}); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, reason := range []string{"probe_required", "disabled", "context_window_required", "invalid_provider_config"} {
		if catalog.Models[i].Reason != reason || catalog.Models[i].Status == "ready" {
			t.Fatal("unexpected catalog status or order")
		}
	}
	view, err := s.GetLLMProvidersView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Revision != view.Revision || !view.Providers["b-disabled"].HasAPIKey || view.Providers["a-keyless"].HasAPIKey {
		t.Fatal("masked snapshot mismatch")
	}
	for _, public := range []any{catalog, view} {
		data, err := sonic.Marshal(public)
		if err != nil || strings.Contains(string(data), "fixture-private-model-key") || strings.Contains(string(data), `"api_key"`) {
			t.Fatal("public view exposed key")
		}
	}
	for _, private := range []any{modelFixture(), requireModelSnapshot(t, s), ModelCredentialUpdate{Action: "replace", Value: modelPtr("fixture-private-model-key")}} {
		if strings.Contains(fmt.Sprintf("%v %+v %#v", private, private, private), "fixture-private-model-key") {
			t.Fatal("formatted private struct exposed key")
		}
	}
}

func TestAgentModelCorruptStateFailsClosed(t *testing.T) {
	s, db := modelService(t)
	ctx := context.Background()
	repo := repository.NewSettingRepository(db)
	if err := repo.Upsert(ctx, domain.SettingKeyLLMProviders, []byte(`{"private-key":`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetLLMModelSnapshot(ctx); !errors.Is(err, ErrModelConfigInvalid) || strings.Contains(err.Error(), "private-key") {
		t.Fatal("corrupt config error not sanitized")
	}
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{}); !errors.Is(err, ErrModelConfigInvalid) {
		t.Fatal("mutation silently replaced corrupt state")
	}
}

func TestAgentModelLegacyEndpointRedaction(t *testing.T) {
	s, db := modelService(t)
	ctx := context.Background()
	p := modelFixture()
	p.BaseURL = "https://private-user:private-password@example.com/v1?key=private-query"
	value, _ := sonic.Marshal(map[string]LLMProvider{"fixture": p})
	if err := repository.NewSettingRepository(db).Upsert(ctx, domain.SettingKeyLLMProviders, value); err != nil {
		t.Fatal(err)
	}
	view, err := s.GetLLMProvidersView(ctx)
	if err != nil || view.Providers["fixture"].BaseURL != "" {
		t.Fatal("legacy endpoint exposed credentials")
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Status != "incompatible_config" {
		t.Fatal("credential endpoint admitted")
	}
	if _, err := s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{Agent: &AgentModelOptionsPatch{Enabled: modelPtr(false)}}, ""); err != nil {
		t.Fatal("invalid legacy config prevented disable")
	}
	if _, err := s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "revoke"}}, ""); err != nil {
		t.Fatal("invalid legacy config prevented credential revocation")
	}
	if requireModelSnapshot(t, s).Providers["fixture"].APIKey != "" {
		t.Fatal("legacy key survived revoke")
	}
}

func TestAgentModelConcurrentRevisionInitialization(t *testing.T) {
	s, db := modelService(t)
	type result struct {
		revision string
		err      error
	}
	results := make(chan result, 12)
	for range 12 {
		go func() {
			v, err := NewSettingService(repository.NewSettingRepository(db)).GetLLMModelSnapshot(context.Background())
			results <- result{v.Revision, err}
		}()
	}
	revision := ""
	for range 12 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if revision != "" && revision != r.revision {
			t.Fatal("first readers observed different revisions")
		}
		revision = r.revision
	}
	if requireModelSnapshot(t, s).Revision != revision {
		t.Fatal("initial revision not durable")
	}
}
