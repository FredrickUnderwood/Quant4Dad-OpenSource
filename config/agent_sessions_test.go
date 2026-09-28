package config

import (
	"strings"
	"testing"
)

func TestAgentSessionsConfiguration(t *testing.T) {
	cfg := bootstrapConfigFixture()
	cfg.Agent.ModelProbe = AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: probeBridgeToken}
	cfg.Agent.Sessions = AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": "sha256:" + strings.Repeat("a", 64)}}
	if err := cfg.ValidateAgentSessions(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(){func() { cfg.Agent.ModelProbe.Enabled = false }, func() { cfg.Agent.Sessions.Profiles = nil }, func() {
		cfg.Agent.Sessions.Profiles = map[string]string{"../../profile": "sha256:" + strings.Repeat("a", 64)}
	}, func() { cfg.Agent.Sessions.Profiles = map[string]string{"text_only": "untrusted"} }} {
		cfg.Agent.ModelProbe.Enabled = true
		cfg.Agent.Sessions.Profiles = map[string]string{"text_only": "sha256:" + strings.Repeat("a", 64)}
		change()
		if cfg.ValidateAgentSessions() != ErrAgentSessionsConfig {
			t.Fatal("invalid sessions configuration accepted")
		}
	}
}
