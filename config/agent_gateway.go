package config

import (
	"errors"
	"path/filepath"
)

type AgentGatewayConfig struct {
	Enabled         bool   `yaml:"enabled"`
	ResultDirectory string `yaml:"result_directory"`
}

var ErrAgentGatewayConfig = errors.New("agent_gateway_configuration_invalid")

func (c *Config) ValidateAgentGateway() error {
	if c == nil || !c.Agent.Gateway.Enabled || c.ValidateAgentRuns() != nil || !filepath.IsAbs(c.Agent.Gateway.ResultDirectory) || filepath.Clean(c.Agent.Gateway.ResultDirectory) != c.Agent.Gateway.ResultDirectory {
		return ErrAgentGatewayConfig
	}
	return nil
}
