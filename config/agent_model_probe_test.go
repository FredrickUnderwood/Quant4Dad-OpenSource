package config

import (
	"fmt"
	"strings"
	"testing"
)

const probeBridgeToken = "fixture-probe-bridge-0123456789-abcdef"

func TestAgentModelProbeConfig(t *testing.T) {
	for _, endpoint := range []string{"http://runtime:8090", "https://runtime/", "http://127.0.0.1:9090"} {
		cfg := bootstrapConfigFixture()
		cfg.Agent.ModelProbe = AgentModelProbeConfig{Enabled: true, RuntimeURL: endpoint, BridgeToken: probeBridgeToken}
		if err := cfg.ValidateAgentModelProbe(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.Agent.Enabled = false }, func(c *Config) { c.Agent.ModelProbe.BridgeToken = bootstrapControl }, func(c *Config) { c.Agent.ModelProbe.BridgeToken = bootstrapMCP }, func(c *Config) { c.Agent.ModelProbe.RuntimeURL = "http://secret@runtime" }, func(c *Config) { c.Agent.ModelProbe.RuntimeURL = "http://runtime/#" }, func(c *Config) { c.Agent.ModelProbe.RuntimeURL = "http://runtime/api" }} {
		cfg := bootstrapConfigFixture()
		cfg.Agent.ModelProbe = AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: probeBridgeToken}
		mutate(cfg)
		if cfg.ValidateAgentModelProbe() != ErrAgentModelProbeConfig {
			t.Fatal("invalid probe config accepted")
		}
	}
}
func TestAgentModelProbeSecretLoading(t *testing.T) {
	cfg := bootstrapConfigFixture()
	cfg.Agent.ModelProbe = AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: probeBridgeToken}
	t.Setenv("AGENT_BRIDGE_TOKEN", probeBridgeToken)
	t.Setenv("AGENT_BRIDGE_TOKEN_FILE", "")
	if err := cfg.loadAgentModelProbe(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", cfg.Agent.ModelProbe, cfg.Agent.ModelProbe), probeBridgeToken) {
		t.Fatal("secret printed")
	}
	cfg.Agent.ModelProbe.Enabled = false
	t.Setenv("AGENT_BRIDGE_TOKEN_FILE", "missing-file")
	if err := cfg.loadAgentModelProbe(); err != nil {
		t.Fatal("disabled feature read secret", err)
	}
}
