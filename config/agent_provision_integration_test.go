//go:build provisionintegration

package config

import (
	"github.com/quant4dad/agent-runtime/profiles"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentProvisionedConfig(t *testing.T) {
	root := os.Getenv("Q4D_PROVISION_CONFIG")
	if root == "" {
		t.Skip("synthetic provisioning fixture required")
	}
	bytes, err := os.ReadFile(filepath.Join(root, "agent-fragment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err = yaml.Unmarshal(bytes, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Security.Token = "fixture-browser-token"
	for id, p := range profiles.Defaults() {
		if cfg.Agent.Sessions.Profiles[id] != p.Revision || cfg.Agent.Runs.Profiles[id].PromptBundleDigest != p.PromptBundleDigest {
			t.Fatal("Go embedded defaults differ from Runtime provisioning")
		}
	}
	// Simulate a deployment carrying old configuration digests, preserving budgets.
	budget := cfg.Agent.Runs.Profiles["strategy_lab"].Budgets
	cfg.Agent.Sessions.Profiles["strategy_lab"] = "stale"
	stale := cfg.Agent.Runs.Profiles["strategy_lab"]
	stale.PromptBundleDigest = "stale"
	cfg.Agent.Runs.Profiles["strategy_lab"] = stale
	if err = cfg.loadAgentProfiles(); err != nil || cfg.Agent.Runs.Profiles["strategy_lab"].Budgets != budget {
		t.Fatal("profile migration failed or replaced budgets", err)
	}
	for _, check := range []func() error{cfg.loadAgentBootstrap, cfg.loadAgentModelProbe, cfg.ValidateAgentSessions, cfg.loadAgentRuns, cfg.ValidateAgentGateway} {
		if err = check(); err != nil {
			t.Fatal(err)
		}
	}
	if len(cfg.Agent.Runs.Profiles) != 3 || cfg.Agent.Runs.ReleaseFile == "" || cfg.Agent.Runs.ReleasePublicKey == "" {
		t.Fatal("incomplete provisioned configuration")
	}
}
