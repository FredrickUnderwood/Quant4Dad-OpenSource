package domain

// AgentBootstrap is a trusted control response. It must never enter browser
// responses, Session storage or logs. Only its explicit wire encoding has secrets.
type AgentBootstrap struct {
	ProtocolVersion     string                   `json:"protocol_version"`
	Revision            string                   `json:"revision"`
	ModelConfigRevision string                   `json:"model_config_revision"`
	Providers           []AgentBootstrapProvider `json:"providers"`
	MCP                 AgentBootstrapMCP        `json:"mcp"`
	Capability          AgentBootstrapCapability `json:"capability"`
	ToolCatalog         *AgentBootstrapCatalog   `json:"tool_catalog,omitempty"`
	ToolCatalogs        []AgentBootstrapCatalog  `json:"tool_catalogs,omitempty"`
	Profiles            []AgentBootstrapProfile  `json:"profiles,omitempty"`
}

// Profile identity is public metadata; prompt text and credentials stay private.
type AgentBootstrapProfile struct {
	ID                  string `json:"id"`
	Revision            string `json:"revision"`
	PromptBundleDigest  string `json:"promptBundleDigest"`
	SkillsDigest        string `json:"skillsDigest"`
	ToolCatalogRevision string `json:"toolCatalogRevision"`
}

// Only enabled research metadata crosses the trusted control boundary.
type AgentBootstrapCatalog struct {
	Profile  string               `json:"profile"`
	Revision string               `json:"revision"`
	Tools    []AgentBootstrapTool `json:"tools"`
}
type AgentBootstrapTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema"`
	Risk         string         `json:"risk"`
	TimeoutMS    int            `json:"timeout_ms"`
	MaxResult    int            `json:"max_result_bytes"`
}

func (AgentBootstrap) String() string   { return "AgentBootstrap{redacted}" }
func (AgentBootstrap) GoString() string { return "AgentBootstrap{redacted}" }

type AgentBootstrapProvider struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	BaseURL      string            `json:"base_url"`
	DefaultModel string            `json:"default_model"`
	APIKey       string            `json:"api_key"`
	Agent        AgentModelOptions `json:"agent"`
}

func (AgentBootstrapProvider) String() string   { return "AgentBootstrapProvider{redacted}" }
func (AgentBootstrapProvider) GoString() string { return "AgentBootstrapProvider{redacted}" }

type AgentBootstrapMCP struct {
	URL              string `json:"url"`
	RuntimeToken     string `json:"runtime_token"`
	ConnectTimeoutMS int    `json:"connect_timeout_ms"`
	ToolTimeoutMS    int    `json:"tool_timeout_ms"`
}

func (AgentBootstrapMCP) String() string   { return "AgentBootstrapMCP{redacted}" }
func (AgentBootstrapMCP) GoString() string { return "AgentBootstrapMCP{redacted}" }

type AgentBootstrapCapability struct {
	Issuer     string            `json:"issuer"`
	Algorithm  string            `json:"algorithm"`
	TokenType  string            `json:"token_type"`
	PublicKeys map[string]string `json:"public_keys"`
}
