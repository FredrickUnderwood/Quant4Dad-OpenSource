package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"gopkg.in/yaml.v3"
)

const bootstrapControl = "fixture-control-token-0123456789-abcdef"
const bootstrapMCP = "fixture-mcp-token-0123456789-abcdef"

func bootstrapConfigFixture() *Config {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	return &Config{Security: SecurityConfig{Token: "fixture-user-token"}, Agent: AgentConfig{Enabled: true,
		Bootstrap: AgentBootstrapConfig{Enabled: true, ControlToken: bootstrapControl, MCPToken: bootstrapMCP,
			MCPURL: "http://mcp:8081/internal/mcp", CapabilityIssuer: "q4d-fixture",
			CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key)}}}}
}

func TestAgentBootstrapConfigValidation(t *testing.T) {
	if err := bootstrapConfigFixture().ValidateAgentBootstrap(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"agent disabled":       func(c *Config) { c.Agent.Enabled = false },
		"bootstrap disabled":   func(c *Config) { c.Agent.Bootstrap.Enabled = false },
		"no login":             func(c *Config) { c.Security.Token = "" },
		"no control":           func(c *Config) { c.Agent.Bootstrap.ControlToken = "" },
		"short token":          func(c *Config) { c.Agent.Bootstrap.MCPToken = "short" },
		"whitespace token":     func(c *Config) { c.Agent.Bootstrap.MCPToken = bootstrapMCP + "\n" },
		"shared service token": func(c *Config) { c.Agent.Bootstrap.MCPToken = bootstrapControl },
		"shared login":         func(c *Config) { c.Security.Token = bootstrapControl },
		"shared mcp login":     func(c *Config) { c.Security.Token = bootstrapMCP },
		"missing issuer":       func(c *Config) { c.Agent.Bootstrap.CapabilityIssuer = "" },
		"missing keys":         func(c *Config) { c.Agent.Bootstrap.CapabilityPublicKeys = nil },
		"padded key":           func(c *Config) { c.Agent.Bootstrap.CapabilityPublicKeys["key-1"] += "=" },
		"private key": func(c *Config) {
			c.Agent.Bootstrap.CapabilityPublicKeys["key-1"] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
		},
		"invalid kid": func(c *Config) {
			c.Agent.Bootstrap.CapabilityPublicKeys["bad\n"] = c.Agent.Bootstrap.CapabilityPublicKeys["key-1"]
		},
		"missing url":       func(c *Config) { c.Agent.Bootstrap.MCPURL = "" },
		"url userinfo":      func(c *Config) { c.Agent.Bootstrap.MCPURL = "http://secret@mcp/internal/mcp" },
		"url query":         func(c *Config) { c.Agent.Bootstrap.MCPURL += "?token=secret" },
		"url empty query":   func(c *Config) { c.Agent.Bootstrap.MCPURL += "?" },
		"url fragment":      func(c *Config) { c.Agent.Bootstrap.MCPURL += "#" },
		"external mcp path": func(c *Config) { c.Agent.Bootstrap.MCPURL = "http://mcp/mcp" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := bootstrapConfigFixture()
			mutate(cfg)
			if err := cfg.ValidateAgentBootstrap(); err != ErrAgentBootstrapConfig {
				t.Fatal("invalid bootstrap configuration accepted or error not sanitized")
			}
		})
	}
}

func TestAgentBootstrapSharedYAML(t *testing.T) {
	cfg := defaults()
	fixture := bootstrapConfigFixture()
	cfg.Security, cfg.Agent = fixture.Security, fixture.Agent
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := configTestFile(t, string(data))
	for _, loader := range []func(string) (*Config, error){LoadForApp, LoadForMCP} {
		loaded, err := loader(path)
		if err != nil || loaded.Agent.Bootstrap.ControlToken != bootstrapControl || loaded.Agent.Bootstrap.MCPToken != bootstrapMCP {
			t.Fatal("complete credentials must come from shared YAML", err)
		}
	}
	cfg.Agent.Bootstrap.MCPToken = ""
	data, _ = yaml.Marshal(cfg)
	t.Setenv("AGENT_MCP_TOKEN", bootstrapMCP)
	if _, err := Parse(data); err != ErrAgentBootstrapConfig {
		t.Fatal("legacy environment cannot supply a missing YAML credential")
	}
}

func TestAgentBootstrapConfigRedaction(t *testing.T) {
	cfg := bootstrapConfigFixture()
	encoded, err := sonic.MarshalString(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{encoded, fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)} {
		if strings.Contains(output, bootstrapControl) || strings.Contains(output, bootstrapMCP) {
			t.Fatal("configuration formatting exposes service credentials")
		}
	}
}
