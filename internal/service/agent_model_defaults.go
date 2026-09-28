package service

import (
	_ "embed"
	"github.com/bytedance/sonic"
)

//go:embed agent_model_defaults.json
var agentModelDefaultsJSON []byte

type agentModelDefault struct {
	Protocol        string `json:"protocol"`
	Model           string `json:"model"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

var agentModelDefaults = func() map[string]agentModelDefault {
	var file struct {
		SchemaVersion int                 `json:"schema_version"`
		Models        []agentModelDefault `json:"models"`
	}
	if sonic.Unmarshal(agentModelDefaultsJSON, &file) != nil || file.SchemaVersion != 1 {
		panic("invalid embedded agent model defaults")
	}
	result := map[string]agentModelDefault{}
	for _, model := range file.Models {
		key := model.Protocol + ":" + model.Model
		if _, duplicate := result[key]; duplicate || model.ContextWindow < 2 || model.MaxOutputTokens < 1 || model.MaxOutputTokens >= model.ContextWindow {
			panic("invalid embedded agent model defaults")
		}
		result[key] = model
	}
	return result
}()

// Defaults apply only to exact catalog IDs and the configured protocol. They
// are read projections, never persisted as user overrides or probe readiness.
func effectiveAgentOptions(p LLMProvider) (AgentModelOptions, string) {
	var a AgentModelOptions
	if p.Agent != nil {
		a = *p.Agent
	}
	source := "configured"
	if d, ok := agentModelDefaults[modelProtocol(p.Type)+":"+p.DefaultModel]; ok {
		if a.ContextWindow == 0 {
			a.ContextWindow = d.ContextWindow
			source = "pinned_catalog"
		}
		if a.MaxOutputTokens == 0 {
			a.MaxOutputTokens = min(d.MaxOutputTokens, a.ContextWindow-1)
			source = "pinned_catalog"
		}
	}
	return a, source
}
