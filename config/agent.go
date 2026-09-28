package config

import (
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Secrets are supplied in the private shared YAML snapshot.
// This configuration is immutable for the lifetime of a bootstrap service.
type AgentBootstrapConfig struct {
	Enabled              bool              `yaml:"enabled" json:"enabled"`
	MCPURL               string            `yaml:"mcp_url" json:"mcp_url"`
	CapabilityIssuer     string            `yaml:"capability_issuer" json:"capability_issuer"`
	CapabilityPublicKeys map[string]string `yaml:"capability_public_keys" json:"capability_public_keys"`
	ControlToken         string            `yaml:"control_token" json:"-"`
	MCPToken             string            `yaml:"mcp_token" json:"-"`
}

func (AgentBootstrapConfig) String() string   { return "AgentBootstrapConfig{redacted}" }
func (AgentBootstrapConfig) GoString() string { return "AgentBootstrapConfig{redacted}" }

var ErrAgentBootstrapConfig = errors.New("agent_bootstrap_configuration_invalid")

var agentServiceToken = regexp.MustCompile(`^[A-Za-z0-9_.~+/=-]{32,256}$`)
var agentKeyID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var agentIssuer = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

// ValidateAgentBootstrap also protects manual construction outside LoadForApp.
// Full Run admission will require the additional Bridge/signing/Gateway gates.
func (c *Config) ValidateAgentBootstrap() error {
	if c == nil {
		return ErrAgentBootstrapConfig
	}
	b := c.Agent.Bootstrap
	if !c.Agent.Enabled || !b.Enabled || strings.TrimSpace(c.Security.Token) == "" ||
		!agentServiceToken.MatchString(b.ControlToken) || !agentServiceToken.MatchString(b.MCPToken) ||
		b.ControlToken == b.MCPToken || b.ControlToken == c.Security.Token || b.MCPToken == c.Security.Token ||
		!agentIssuer.MatchString(b.CapabilityIssuer) || len(b.CapabilityPublicKeys) < 1 || len(b.CapabilityPublicKeys) > 16 {
		return ErrAgentBootstrapConfig
	}
	u, err := url.Parse(b.MCPURL)
	if err != nil || len(b.MCPURL) > 2048 || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.Path != "/internal/mcp" || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(b.MCPURL, "#") {
		return ErrAgentBootstrapConfig
	}
	for kid, raw := range b.CapabilityPublicKeys {
		key, err := base64.RawURLEncoding.Strict().DecodeString(raw)
		if !agentKeyID.MatchString(kid) || err != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != raw {
			return ErrAgentBootstrapConfig
		}
	}
	return nil
}

func (c *Config) loadAgentBootstrap() error {
	if !c.Agent.Bootstrap.Enabled {
		return nil
	}
	return c.ValidateAgentBootstrap()
}
