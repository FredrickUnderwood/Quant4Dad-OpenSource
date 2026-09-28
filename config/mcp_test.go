package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

const externalTestToken = "fixture-external-mcp-token-0123456789"

func TestExternalMCPRequiresIndependentSecret(t *testing.T) {
	cfg := bootstrapConfigFixture()
	cfg.MCP.ExternalToken = externalTestToken
	if err := cfg.ValidateExternalMCP(); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "short", bootstrapControl, bootstrapMCP, cfg.Security.Token, externalTestToken + "\n"} {
		cfg.MCP.ExternalToken = token
		if cfg.ValidateExternalMCP() == nil {
			t.Fatal("accepted invalid or reused token")
		}
	}
	cfg.MCP.ExternalToken = externalTestToken
	if strings.Contains(fmt.Sprintf("%v %#v", cfg.MCP, cfg.MCP), externalTestToken) {
		t.Fatal("secret printed")
	}
}
func TestExternalMCPCredentialsFromSharedYAML(t *testing.T) {
	path := configTestFile(t, "mcp:\n  external_token: "+externalTestToken+"\n  mysql_dsn: readonly-dsn\n")
	t.Setenv("MCP_EXTERNAL_TOKEN", "ignored")
	t.Setenv("MCP_EXTERNAL_TOKEN_FILE", "/missing")
	cfg, err := LoadForMCP(path)
	if err != nil || cfg.MCP.ExternalToken != externalTestToken || cfg.MCPStorage().MySQL.DSN != "readonly-dsn" {
		t.Fatal("MCP must use shared YAML", err)
	}
	cfg.Agent.ModelProbe.BridgeToken = externalTestToken
	if cfg.ValidateExternalMCP() == nil {
		t.Fatal("bridge credential reuse accepted")
	}
	cfg.Agent.ModelProbe.BridgeToken = ""
	cfg.Agent.Runs.SigningPrivateKey = externalTestToken
	if cfg.ValidateExternalMCP() == nil {
		t.Fatal("signing credential reuse accepted")
	}
}

func TestFullMCPModeRequiresExplicitConfiguredAuthority(t *testing.T) {
	cfg := bootstrapConfigFixture()
	cfg.MCP.ExternalToken = externalTestToken
	cfg.MCP.AgentToolsEnabled = true
	cfg.Web.APIBaseURL = "http://api:8080"
	if cfg.ValidateExternalMCP() != nil {
		t.Fatal("full MCP should not require Agent Runtime")
	}
	cfg.Web.APIBaseURL = "https://api.example/prefix/"
	if err := cfg.ValidateExternalMCP(); err != nil || cfg.MCPAPIBaseURL() != "https://api.example/prefix" {
		t.Fatal("valid full MCP rejected", err)
	}
	cfg.MCP.APIBaseURL = "https://user:secret@api.example"
	if cfg.ValidateExternalMCP() == nil {
		t.Fatal("credential URL accepted")
	}
	cfg.MCP.APIBaseURL = "http://api.private:8080"
	if cfg.MCPAPIBaseURL() != cfg.MCP.APIBaseURL || cfg.ValidateExternalMCP() != nil {
		t.Fatal("private override rejected")
	}
	_, err := Load(configTestFile(t, "mcp:\n  agent_tools_enabled: true\n"))
	if err == nil {
		t.Fatal("full access enabled without dedicated token")
	}
}

func TestExternalMCPReceiptSignerIsIndependent(t *testing.T) {
	cfg := defaults()
	cfg.MCP.AgentToolsEnabled, cfg.MCP.ExternalToken = true, externalTestToken
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MCP.SigningPrivateKey = base64.RawURLEncoding.EncodeToString(key)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.MCPReceiptSigner(); err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Enabled || cfg.Agent.Runs.Enabled || cfg.Agent.Gateway.Enabled {
		t.Fatal("MCP enabled internal Agent authority")
	}
	cfg.Agent.Runs.SigningPrivateKey = cfg.MCP.SigningPrivateKey
	if _, err := cfg.MCPReceiptSigner(); err == nil {
		t.Fatal("internal signing key reused")
	}
	cfg.Agent.Runs.SigningPrivateKey = ""
	cfg.MCP.SigningPrivateKey = ""
	if _, err := cfg.MCPReceiptSigner(); err == nil {
		t.Fatal("missing receipt signer accepted")
	}
	cfg.MCP.ResultDirectory = "relative/path"
	if cfg.ValidateExternalMCP() == nil {
		t.Fatal("relative result directory accepted")
	}
}
