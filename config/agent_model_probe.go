package config

import (
	"errors"
	"net/url"
	"strings"
)

type AgentModelProbeConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	RuntimeURL  string `yaml:"runtime_url" json:"runtime_url"`
	BridgeToken string `yaml:"bridge_token" json:"-"`
}

func (AgentModelProbeConfig) String() string   { return "AgentModelProbeConfig{redacted}" }
func (AgentModelProbeConfig) GoString() string { return "AgentModelProbeConfig{redacted}" }

var ErrAgentModelProbeConfig = errors.New("agent_model_probe_configuration_invalid")

func (c *Config) ValidateAgentModelProbe() error {
	if c == nil || !c.Agent.ModelProbe.Enabled || c.ValidateAgentBootstrap() != nil {
		return ErrAgentModelProbeConfig
	}
	p := c.Agent.ModelProbe
	u, err := url.Parse(p.RuntimeURL)
	if err != nil || len(p.RuntimeURL) > 2048 || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(p.RuntimeURL, "#") || u.Opaque != "" ||
		!agentServiceToken.MatchString(p.BridgeToken) || p.BridgeToken == c.Security.Token || p.BridgeToken == c.Agent.Bootstrap.ControlToken || p.BridgeToken == c.Agent.Bootstrap.MCPToken {
		return ErrAgentModelProbeConfig
	}
	return nil
}
func (c *Config) loadAgentModelProbe() error {
	if !c.Agent.ModelProbe.Enabled {
		return nil
	}
	return c.ValidateAgentModelProbe()
}
