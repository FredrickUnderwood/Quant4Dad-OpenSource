package domain

import "regexp"

const AgentModelProbeVersion = "echo-v1"
const SettingKeyAgentModelProbes = "agent.model.probes"

type AgentModelProbeRequest struct {
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	ModelConfigRevision string `json:"model_config_revision"`
	ProbeVersion        string `json:"probe_version"`
	Force               bool   `json:"force"`
}

// Only bounded public facts are persisted. No endpoint, key, prompt, response
// content, transport diagnostics or secret-derived digest belongs here.
type AgentModelProbeResult struct {
	Provider            string `json:"provider"`
	Model               string `json:"model"`
	ModelConfigRevision string `json:"model_config_revision"`
	ProbeVersion        string `json:"probe_version"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
	CheckedAtMS         int64  `json:"checked_at_ms"`
	ExpiresAtMS         int64  `json:"expires_at_ms"`
	ContextWindow       int    `json:"context_window"`
	MaxOutputTokens     int    `json:"max_output_tokens"`
	ContextSource       string `json:"context_source"`
	ModelTurns          int    `json:"model_turns"`
	ToolCalls           int    `json:"tool_calls"`
	FirstEventMS        int    `json:"first_event_ms"`
}

var probeProvider = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
var probeModel = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,128}$`)
var probeRevision = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (r AgentModelProbeRequest) Valid() bool {
	return probeProvider.MatchString(r.Provider) && probeModel.MatchString(r.Model) && probeRevision.MatchString(r.ModelConfigRevision) && r.ProbeVersion == AgentModelProbeVersion
}

func (r AgentModelProbeResult) Valid(nowMS int64) bool {
	if !(AgentModelProbeRequest{Provider: r.Provider, Model: r.Model, ModelConfigRevision: r.ModelConfigRevision, ProbeVersion: r.ProbeVersion}).Valid() ||
		r.CheckedAtMS < 1 || r.CheckedAtMS > nowMS+5000 || r.ExpiresAtMS <= nowMS || r.ContextSource != "explicit-config" ||
		r.ContextWindow < 2 || r.ContextWindow > 100000000 || r.MaxOutputTokens < 1 || r.MaxOutputTokens > 10000000 || r.MaxOutputTokens >= r.ContextWindow ||
		r.ModelTurns < 1 || r.ModelTurns > 2 || r.ToolCalls < 0 || r.ToolCalls > 1 || r.FirstEventMS < 0 || r.FirstEventMS > 15000 {
		return false
	}
	ttl := int64(86400000)
	switch r.Status {
	case "ready":
		if r.Reason != "probe_passed" || r.ModelTurns != 2 || r.ToolCalls != 1 {
			return false
		}
	case "incompatible":
		if r.Reason != "probe_contract_mismatch" {
			return false
		}
	case "unavailable":
		ttl = 300000
		if r.Reason != "probe_request_failed" {
			return false
		}
	default:
		return false
	}
	return r.ExpiresAtMS-r.CheckedAtMS == ttl
}
