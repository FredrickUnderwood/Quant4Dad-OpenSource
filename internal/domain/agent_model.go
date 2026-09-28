package domain

// LLMProvider is the single persisted model configuration used by both pipelines
// and Agent. CredentialRevoked is server-owned and distinguishes an intentionally
// removed credential from a provider that has always been keyless.
type LLMProvider struct {
	Type              string             `json:"type"`
	BaseURL           string             `json:"base_url"`
	APIKey            string             `json:"api_key"`
	DefaultModel      string             `json:"default_model"`
	Agent             *AgentModelOptions `json:"agent,omitempty"`
	CredentialRevoked bool               `json:"credential_revoked,omitempty"`
}

func (LLMProvider) String() string   { return "LLMProvider{redacted}" }
func (LLMProvider) GoString() string { return "LLMProvider{redacted}" }

type AgentModelOptions struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	Protocol        string `json:"protocol,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

const SettingKeyAgentModelRevision = "agent.model.revision"
