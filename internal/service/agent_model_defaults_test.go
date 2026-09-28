package service

import (
	"context"
	"testing"
)

func TestPinnedModelDefaultsStayAlignedWithoutBecomingStoredOverrides(t *testing.T) {
	s, _ := modelService(t)
	ctx := context.Background()
	p := modelFixture()
	p.DefaultModel = "gpt-4o"
	p.Agent = nil
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": p}); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.GetAgentModelCatalog(ctx)
	if err != nil || len(catalog.Models) != 1 {
		t.Fatal(err)
	}
	candidate := catalog.Models[0]
	if candidate.Status != "unverified" || candidate.LimitsSource != "pinned_catalog" || candidate.ContextWindow < 8193 || candidate.MaxOutputTokens != 8192 {
		t.Fatal("invalid defaults", candidate)
	}
	revision, bootstrap, err := s.GetAgentBootstrapModels(ctx)
	if err != nil || revision != catalog.Revision || len(bootstrap) != 1 || bootstrap[0].Agent.ContextWindow != candidate.ContextWindow || bootstrap[0].Agent.MaxOutputTokens != candidate.MaxOutputTokens {
		t.Fatal("bootstrap/catalog limits diverged", err)
	}
	snapshot := requireModelSnapshot(t, s)
	if snapshot.Providers["fixture"].Agent != nil {
		t.Fatal("default limits persisted as user overrides")
	}
	unknown := "custom-unknown-model"
	if _, err := s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{DefaultModel: &unknown}, catalog.Revision); err != nil {
		t.Fatal(err)
	}
	catalog, err = s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].Reason != "context_window_required" {
		t.Fatal("old model defaults followed a renamed model", err)
	}
	configured := modelFixture()
	configured.DefaultModel = "gpt-4o"
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": configured}); err != nil {
		t.Fatal(err)
	}
	catalog, err = s.GetAgentModelCatalog(ctx)
	if err != nil || catalog.Models[0].LimitsSource != "configured" || catalog.Models[0].ContextWindow != 8192 || catalog.Models[0].MaxOutputTokens != 512 {
		t.Fatal("explicit limits were overwritten", err)
	}
	configured.Agent.Enabled = modelPtr(false)
	if modelCandidate("fixture", configured).Reason != "disabled" {
		t.Fatal("default bypassed disable")
	}
	configured.CredentialRevoked = true
	if modelCandidate("fixture", configured).Reason != "credential_revoked" {
		t.Fatal("default bypassed revoke")
	}
	configured.Type = "anthropic"
	configured.Agent = nil
	if modelCandidate("fixture", configured).ContextWindow != 0 {
		t.Fatal("catalog ignored protocol")
	}
}
