package config

import (
	"errors"

	"github.com/quant4dad/agent-runtime/profiles"
)

// Product defaults follow the binary, while budgets, credentials, enabled
// profiles and custom profile IDs remain deployment configuration.
func (c *Config) loadAgentProfiles() error {
	if c.Agent.ProfileSource == "config" {
		return nil
	}
	if c.Agent.ProfileSource != "" && c.Agent.ProfileSource != "repository" {
		return errors.New("agent_profile_source_invalid")
	}
	if !c.Agent.Sessions.Enabled && !c.Agent.Runs.Enabled {
		return nil
	}
	if c.Agent.Sessions.Profiles == nil {
		c.Agent.Sessions.Profiles = make(map[string]string)
	}
	for id, p := range profiles.Defaults() {
		_, sessionEnabled := c.Agent.Sessions.Profiles[id]
		run, runEnabled := c.Agent.Runs.Profiles[id]
		if sessionEnabled || runEnabled {
			c.Agent.Sessions.Profiles[id] = p.Revision
		}
		if runEnabled {
			run.PromptBundleDigest, run.SkillsDigest = p.PromptBundleDigest, p.SkillsDigest
			run.ToolCatalogRevision = "" // Resolved from the live scoped catalog.
			c.Agent.Runs.Profiles[id] = run
		}
	}
	return nil
}
