package service

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
)

func TestAgentBootstrapProviderFiltering(t *testing.T) {
	s, db := modelService(t)
	ctx := context.Background()
	providers := map[string]LLMProvider{}
	for _, name := range []string{"valid", "keyless", "disabled", "revoked", "unknown-context", "invalid", "not/bridge-id"} {
		providers[name] = modelFixture()
	}
	p := providers["keyless"]
	p.APIKey = ""
	providers["keyless"] = p
	providers["disabled"].Agent.Enabled = modelPtr(false)
	p = providers["revoked"]
	p.CredentialRevoked = true
	providers["revoked"] = p // even an inconsistent legacy row must drop its key
	providers["unknown-context"].Agent.ContextWindow = 0
	p = providers["invalid"]
	p.BaseURL += "?secret=private"
	providers["invalid"] = p
	value, _ := sonic.Marshal(providers)
	if err := repository.NewSettingRepository(db).Upsert(ctx, domain.SettingKeyLLMProviders, value); err != nil {
		t.Fatal(err)
	}
	revision, got, err := s.GetAgentBootstrapModels(ctx)
	if err != nil || !modelRevisionPattern.MatchString(revision) || len(got) != 2 || got[0].ID != "keyless" || got[1].ID != "valid" || got[0].APIKey != "" || got[1].APIKey != modelFixture().APIKey {
		t.Fatal("bootstrap filtering or paired revision incorrect")
	}
	if got[1].Agent.Protocol != "openai-completions" || !*got[1].Agent.Enabled {
		t.Fatal("missing explicit normalized metadata")
	}
	got[1].APIKey = "mutated"
	*got[1].Agent.Enabled = false
	_, again, err := s.GetAgentBootstrapModels(ctx)
	if err != nil || again[1].APIKey != modelFixture().APIKey || !*again[1].Agent.Enabled {
		t.Fatal("response mutated future snapshots")
	}
}

func TestAgentBootstrapRevocationSequence(t *testing.T) {
	s, _ := modelService(t)
	ctx := context.Background()
	if err := s.SetLLMProviders(ctx, map[string]LLMProvider{"fixture": modelFixture()}); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.GetAgentBootstrapModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"disable", "enable", "revoke", "replace", "delete"} {
		want := 0
		var err error
		switch action {
		case "disable", "enable":
			_, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{Agent: &AgentModelOptionsPatch{Enabled: modelPtr(action == "enable")}}, "")
			if action == "enable" {
				want = 1
			}
		case "revoke":
			_, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "revoke"}}, "")
		case "replace":
			_, err = s.PatchLLMProvider(ctx, "fixture", LLMProviderPatch{APIKeyUpdate: &ModelCredentialUpdate{Action: "replace", Value: modelPtr("new-fixture-key")}}, "")
			want = 1
		case "delete":
			_, err = s.DeleteLLMProvider(ctx, "fixture", "")
		}
		if err != nil {
			t.Fatal(err)
		}
		revision, got, err := s.GetAgentBootstrapModels(ctx)
		if err != nil || revision == before || len(got) != want {
			t.Fatalf("bootstrap did not reflect %s", action)
		}
		before = revision
	}
	_, got, _ := s.GetAgentBootstrapModels(ctx)
	body, _ := sonic.MarshalString(got)
	if strings.Contains(body, "fixture") || body != "[]" {
		t.Fatal("deleted credentials retained")
	}
}
