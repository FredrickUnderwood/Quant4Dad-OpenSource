package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/quant4dad/internal/agentrunauth"
	"gopkg.in/yaml.v3"
)

func runsConfigFixture() *Config {
	c := bootstrapConfigFixture()
	digest := "sha256:" + strings.Repeat("a", 64)
	c.Agent.ModelProbe = AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: probeBridgeToken}
	c.Agent.Sessions = AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": digest}}
	c.Agent.Runs = AgentRunsConfig{Enabled: true, SigningKeyID: "key-1", SigningPrivateKey: base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32))),
		Manifest: AgentRunManifest{Q4DVersion: "0.0.0", AgentImageDigest: digest, AgentRuntimeVersion: "fixture-v1", AdapterVersion: "fixture-v1", DSHVersion: "0.1.2-alpha.5"},
		Profiles: map[string]AgentRunProfile{"text_only": {PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: digest, Budgets: agentrunauth.Budgets{MaxTurns: 1, MaxInputTokens: 8192, MaxOutputTokens: 512, WallTimeMS: 30000}}}}
	return c
}
func TestAgentRunsConfigurationAndSecrets(t *testing.T) {
	c := runsConfigFixture()
	if _, err := c.AgentRunSigner(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Agent.Sessions.Enabled = false }, func(c *Config) { c.Agent.Runs.SigningKeyID = "missing" }, func(c *Config) { c.Agent.Runs.SigningPrivateKey += "=" }, func(c *Config) { c.Agent.Runs.Manifest.AgentImageDigest = "latest" }, func(c *Config) { c.Agent.Runs.Profiles = nil }, func(c *Config) {
		p := c.Agent.Runs.Profiles["text_only"]
		p.Budgets.MaxToolCalls = 1
		c.Agent.Runs.Profiles["text_only"] = p
	}, func(c *Config) {
		p := c.Agent.Runs.Profiles["text_only"]
		p.Budgets.WallTimeMS = 3600001
		c.Agent.Runs.Profiles["text_only"] = p
	}} {
		bad := runsConfigFixture()
		mutate(bad)
		if bad.ValidateAgentRuns() != ErrAgentRunsConfig {
			t.Fatal("invalid Run configuration accepted")
		}
	}
	key := c.Agent.Runs.SigningPrivateKey
	if strings.Contains(fmt.Sprintf("%v %#v", c.Agent.Runs, c.Agent.Runs), key) {
		t.Fatal("signing key printed")
	}
	t.Setenv("AGENT_RUN_SIGNING_PRIVATE_KEY", key)
	c.Agent.Runs.SigningPrivateKey = ""
	if c.loadAgentRuns() != ErrAgentRunsConfig {
		t.Fatal("legacy environment supplied a missing YAML key")
	}
	c.Agent.Runs.Enabled = false
	if err := c.loadAgentRuns(); err != nil {
		t.Fatal("disabled feature read secret", err)
	}
}
func TestAgentRunBudgetsYAML(t *testing.T) {
	var profile AgentRunProfile
	if err := yaml.Unmarshal([]byte("budgets:\n  max_turns: 1\n  max_tool_calls: 0\n  max_input_tokens: 8192\n  max_output_tokens: 512\n  wall_time_ms: 30000\n"), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Budgets != runsConfigFixture().Agent.Runs.Profiles["text_only"].Budgets {
		t.Fatal("budgets not decoded")
	}
}

func TestResearchRunsRequireExplicitGateway(t *testing.T) {
	c := runsConfigFixture()
	p := c.Agent.Runs.Profiles["text_only"]
	p.Budgets.MaxToolCalls, p.ToolCatalogRevision = 2, ""
	c.Agent.Sessions.Profiles = map[string]string{"research": c.Agent.Sessions.Profiles["text_only"]}
	c.Agent.Runs.Profiles = map[string]AgentRunProfile{"research": p}
	if c.ValidateAgentRuns() == nil {
		t.Fatal("research enabled without Gateway")
	}
	c.Agent.Gateway.Enabled = true
	if err := c.ValidateAgentRuns(); err != nil {
		t.Fatal(err)
	}
	c.Agent.Runs.Profiles["strategy_lab"] = p
	if c.ValidateAgentRuns() == nil {
		t.Fatal("non-research tools enabled")
	}
}
