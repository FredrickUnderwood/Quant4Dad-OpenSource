package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"path/filepath"

	"github.com/quant4dad/internal/agentrunauth"
	"strings"
)

// ExternalToken and an optional SELECT-only MySQL DSN belong to the shared YAML.
// -http explicitly enables the external listener; stdio has no network token.
type MCPConfig struct {
	Addr          string `yaml:"addr"`
	MySQLDSN      string `yaml:"mysql_dsn" json:"-"`
	ExternalToken string `yaml:"external_token" json:"-"`
	// AgentToolsEnabled explicitly grants the dedicated token the Agent tool
	// surface, including mutations. The legacy query-only listener stays default.
	AgentToolsEnabled bool   `yaml:"agent_tools_enabled"`
	APIBaseURL        string `yaml:"api_base_url"`
	ResultDirectory   string `yaml:"result_directory"`
	SigningPrivateKey string `yaml:"signing_private_key" json:"-"`
}

func (MCPConfig) String() string   { return "MCPConfig{redacted}" }
func (MCPConfig) GoString() string { return "MCPConfig{redacted}" }

var ErrMCPConfig = errors.New("mcp_external_configuration_invalid")

func (c *Config) ValidateExternalMCP() error {
	if c == nil || !agentServiceToken.MatchString(c.MCP.ExternalToken) {
		return ErrMCPConfig
	}
	for _, value := range []string{c.Security.Token, c.Agent.Bootstrap.ControlToken, c.Agent.Bootstrap.MCPToken, c.Agent.ModelProbe.BridgeToken, c.Agent.Runs.SigningPrivateKey} {
		if value != "" && value == c.MCP.ExternalToken {
			return ErrMCPConfig
		}
	}
	if c.MCP.APIBaseURL != "" && (WebConfig{APIBaseURL: c.MCP.APIBaseURL}).Validate() != nil {
		return ErrMCPConfig
	}
	if c.MCP.AgentToolsEnabled && ((WebConfig{APIBaseURL: c.MCPAPIBaseURL()}).Validate() != nil) {
		return ErrMCPConfig
	}
	if c.MCP.ResultDirectory != "" && (!filepath.IsAbs(c.MCP.ResultDirectory) || filepath.Clean(c.MCP.ResultDirectory) != c.MCP.ResultDirectory) {
		return ErrMCPConfig
	}
	if c.MCP.SigningPrivateKey != "" {
		if _, err := c.MCPReceiptSigner(); err != nil {
			return err
		}
	}
	return nil
}

// MCPReceiptSigner owns only external-token receipts, independently of Agent Runs.
func (c *Config) MCPReceiptSigner() (*agentrunauth.Signer, error) {
	key, err := base64.RawURLEncoding.Strict().DecodeString(c.MCP.SigningPrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || c.MCP.SigningPrivateKey == c.Agent.Runs.SigningPrivateKey {
		return nil, ErrMCPConfig
	}
	return agentrunauth.NewSigner("external-mcp-1", "quant4dad-external-mcp", ed25519.PrivateKey(key))
}

func (m MCPConfig) ToolResultDirectory() string {
	if m.ResultDirectory != "" {
		return m.ResultDirectory
	}
	return "/app/data/tool-results"
}

// MCPAPIBaseURL uses the same private API upstream as Web unless overridden.
func (c *Config) MCPAPIBaseURL() string {
	if c.MCP.APIBaseURL != "" {
		return strings.TrimRight(c.MCP.APIBaseURL, "/")
	}
	return strings.TrimRight(c.Web.APIBaseURL, "/")
}

// MCPStorage preserves the shared database choice while allowing a read-only account.
func (c *Config) MCPStorage() StorageConfig {
	storage := c.Storage
	if c.MCP.MySQLDSN != "" {
		storage.MySQL.DSN = c.MCP.MySQLDSN
	}
	return storage
}
