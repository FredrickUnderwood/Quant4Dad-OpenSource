package config

import (
	"testing"

	"github.com/quant4dad/agent-runtime/profiles"
	"github.com/quant4dad/internal/agentrunauth"
)

func TestAgentProfilesUpgradeKeepsEnvironment(t *testing.T) {
	budget := agentrunauth.Budgets{MaxTurns: 12, MaxToolCalls: 16, MaxInputTokens: 2000000, MaxOutputTokens: 16384, WallTimeMS: 600000}
	cfg := Config{Agent: AgentConfig{Sessions: AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"strategy_lab": "old", "custom": "custom-revision"}},
		Runs: AgentRunsConfig{Enabled: true, SigningPrivateKey: "unchanged-secret", ReleaseFile: "/data/release.json", Profiles: map[string]AgentRunProfile{
			"strategy_lab": {PromptBundleDigest: "old", SkillsDigest: "old", ToolCatalogRevision: "stale", Budgets: budget},
		}}}}
	if err := cfg.loadAgentProfiles(); err != nil {
		t.Fatal(err)
	}
	p := profiles.Defaults()["strategy_lab"]
	if cfg.Agent.Sessions.Profiles["strategy_lab"] != p.Revision || cfg.Agent.Runs.Profiles["strategy_lab"].PromptBundleDigest != p.PromptBundleDigest {
		t.Fatal("stale product profile")
	}
	if cfg.Agent.Runs.Profiles["strategy_lab"].Budgets != budget || cfg.Agent.Sessions.Profiles["custom"] != "custom-revision" || cfg.Agent.Runs.SigningPrivateKey != "unchanged-secret" || cfg.Agent.Runs.ReleaseFile != "/data/release.json" {
		t.Fatal("environment overwritten")
	}
	if cfg.Agent.Runs.Profiles["strategy_lab"].ToolCatalogRevision != "" {
		t.Fatal("catalog must be live")
	}
	if len(cfg.Agent.Runs.Profiles) != 1 || len(cfg.Agent.Sessions.Profiles) != 2 {
		t.Fatal("disabled profiles enabled")
	}
}

func TestAgentProfilesExplicitPinAndInvalidSource(t *testing.T) {
	cfg := Config{Agent: AgentConfig{ProfileSource: "config", Sessions: AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"strategy_lab": "pinned"}}}}
	if err := cfg.loadAgentProfiles(); err != nil || cfg.Agent.Sessions.Profiles["strategy_lab"] != "pinned" {
		t.Fatal("explicit pin changed")
	}
	cfg.Agent.ProfileSource = "typo"
	if cfg.loadAgentProfiles() == nil {
		t.Fatal("invalid source accepted")
	}
}
