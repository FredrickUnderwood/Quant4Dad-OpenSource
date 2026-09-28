package config

import (
	"errors"
	"regexp"
)

type AgentSessionsConfig struct {
	Enabled  bool              `yaml:"enabled"`
	Profiles map[string]string `yaml:"profiles"` // Trusted Profile ID -> current revision; no browser override.
}

var ErrAgentSessionsConfig = errors.New("agent_sessions_configuration_invalid")

func (c *Config) ValidateAgentSessions() error {
	if c == nil || !c.Agent.Sessions.Enabled || c.ValidateAgentModelProbe() != nil || len(c.Agent.Sessions.Profiles) < 1 || len(c.Agent.Sessions.Profiles) > 128 {
		return ErrAgentSessionsConfig
	}
	id := regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	revision := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for key, value := range c.Agent.Sessions.Profiles {
		if !id.MatchString(key) || !revision.MatchString(value) {
			return ErrAgentSessionsConfig
		}
	}
	return nil
}
