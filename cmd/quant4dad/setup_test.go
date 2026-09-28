package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
	"gopkg.in/yaml.v3"
)

func setupTestDirectory(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Q4D_SETUP_MCP", "Q4D_SETUP_AGENT", "Q4D_SETUP_MYSQL_DSN_FILE"} {
		t.Setenv(key, "")
	}
	return dir
}
func readSetupConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(dir, "config", "api.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
func TestStandaloneSetupProfilesAndCredentialPersistence(t *testing.T) {
	for _, combination := range []struct {
		name       string
		mcp, agent bool
	}{{"base", false, false}, {"mcp", true, false}, {"agent", false, true}, {"both", true, true}} {
		t.Run(combination.name, func(t *testing.T) {
			dir := setupTestDirectory(t)
			if combination.mcp {
				t.Setenv("Q4D_SETUP_MCP", "1")
			}
			if combination.agent {
				writeAgentFragment(t, dir)
				t.Setenv("Q4D_SETUP_AGENT", "1")
			}
			if err := setupStandalone(dir); err != nil {
				t.Fatal(err)
			}
			cfg := readSetupConfig(t, dir)
			if cfg.Agent.Enabled != combination.agent || cfg.MCP.AgentToolsEnabled != combination.mcp || len(cfg.Security.Token) < 32 {
				t.Fatal("incorrect profile or login")
			}
			if cfg.Storage.Backend != "sqlite" || cfg.AutoSync.Enabled || cfg.News.Enabled || cfg.Archive.Enabled {
				t.Fatal("unexpected background activity")
			}
			before := cfg.Security.Token
			mcpToken, mcpKey := cfg.MCP.ExternalToken, cfg.MCP.SigningPrivateKey
			sentinel := filepath.Join(dir, "data", "keep")
			if err := os.WriteFile(sentinel, []byte("existing-data"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("Q4D_SETUP_MCP", "")
			t.Setenv("Q4D_SETUP_AGENT", "")
			if err := setupStandalone(dir); err != nil {
				t.Fatal(err)
			}
			saved := readSetupConfig(t, dir)
			if saved.Security.Token != before || saved.MCP.ExternalToken != mcpToken || saved.MCP.SigningPrivateKey != mcpKey || saved.Agent.Enabled != combination.agent {
				t.Fatal("repeat setup changed credentials/profiles")
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "existing-data" {
				t.Fatal("data changed")
			}
			web, err := os.ReadFile(filepath.Join(dir, "config", "web.yaml"))
			if err != nil || bytes.Contains(web, []byte(before)) {
				t.Fatal("web received a secret")
			}
			if combination.mcp {
				proxy, err := config.Load(filepath.Join(dir, "config", "mcp.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				if proxy.MCP.SigningPrivateKey != "" || proxy.Security.Token != "" || proxy.Agent.Enabled || proxy.MCP.ExternalToken != mcpToken {
					t.Fatal("proxy received unrelated authority")
				}
				if _, err := cfg.MCPReceiptSigner(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestStandaloneSetupMySQLAndPrivateFiles(t *testing.T) {
	dir := setupTestDirectory(t)
	dsnPath := filepath.Join(dir, "mysql-input")
	dsn := "fixture:pa:ss@(db.example:3306)/fixture?parseTime=true&charset=utf8mb4"
	if err := os.WriteFile(dsnPath, []byte(dsn), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("Q4D_SETUP_MYSQL_DSN_FILE", dsnPath)
	if err := setupStandalone(dir); err != nil {
		t.Fatal(err)
	}
	cfg := readSetupConfig(t, dir)
	if cfg.Storage.MySQL.DSN != dsn || cfg.Storage.Backend != "mysql" {
		t.Fatal("DSN changed during serialization")
	}
	tokenPath := filepath.Join(dir, "config", "login-token")
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatal(err)
	}
	if err := setupStandalone(dir); err == nil {
		t.Fatal("public token accepted")
	}
}
func writeAgentFragment(t *testing.T, dir string) {
	t.Helper()
	folder := filepath.Join(dir, "agent", "config")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	agent := config.AgentConfig{Enabled: true, ProfileSource: "config",
		Bootstrap:  config.AgentBootstrapConfig{Enabled: true, ControlToken: strings.Repeat("c", 48), MCPToken: strings.Repeat("m", 48), MCPURL: "http://127.0.0.1:8080/internal/mcp", CapabilityIssuer: "quant4dad", CapabilityPublicKeys: map[string]string{"run-1": base64.RawURLEncoding.EncodeToString(key[32:])}},
		ModelProbe: config.AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://127.0.0.1:29091", BridgeToken: strings.Repeat("b", 48)},
		Sessions:   config.AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"research": digest}},
		Gateway:    config.AgentGatewayConfig{Enabled: true, ResultDirectory: "/app/data/agent-tool-results"},
		Runs:       config.AgentRunsConfig{Enabled: true, SigningKeyID: "run-1", SigningPrivateKey: base64.RawURLEncoding.EncodeToString(key), Manifest: config.AgentRunManifest{Q4DVersion: "local-release", AgentImageDigest: digest, AgentRuntimeVersion: "fixture", AdapterVersion: "fixture", DSHVersion: "0.1.2-alpha.5"}, Profiles: map[string]config.AgentRunProfile{"research": {PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: "", Budgets: agentrunauth.Budgets{MaxTurns: 1, MaxToolCalls: 5, MaxInputTokens: 8192, MaxOutputTokens: 512, WallTimeMS: 30000}}}},
	}
	body, err := yaml.Marshal(struct {
		Agent config.AgentConfig `yaml:"agent"`
	}{agent})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(folder, "agent-fragment.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAddingAgentPreservesMCPArtifacts(t *testing.T) {
	dir := setupTestDirectory(t)
	t.Setenv("Q4D_SETUP_MCP", "1")
	if err := setupStandalone(dir); err != nil {
		t.Fatal(err)
	}
	before := readSetupConfig(t, dir)
	writeAgentFragment(t, dir)
	t.Setenv("Q4D_SETUP_AGENT", "1")
	if err := setupStandalone(dir); err != nil {
		t.Fatal(err)
	}
	after := readSetupConfig(t, dir)
	if after.Agent.Gateway.ResultDirectory != before.MCP.ToolResultDirectory() || after.MCP.ResultDirectory != before.MCP.ResultDirectory || after.MCP.ExternalToken != before.MCP.ExternalToken || after.MCP.SigningPrivateKey != before.MCP.SigningPrivateKey {
		t.Fatal("adding Agent moved external MCP artifacts or authority")
	}
}

func TestStandaloneSetupPreservesWebProxyConfiguration(t *testing.T) {
	dir := setupTestDirectory(t)
	if err := setupStandalone(dir); err != nil {
		t.Fatal(err)
	}
	webPath := filepath.Join(dir, "config", "web.yaml")
	body := []byte("web:\n  addr: ':8080'\n  api_base_url: http://custom-api:8080\n  trusted_proxies: [192.0.2.7/32]\nsecurity:\n  token: unrelated-api-secret\n")
	if err := os.WriteFile(webPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := setupStandalone(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(webPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.APIBaseURL != "http://custom-api:8080" || len(cfg.Web.TrustedProxies) != 1 || cfg.Web.TrustedProxies[0] != "192.0.2.7/32" {
		t.Fatal("Web proxy configuration overwritten")
	}
	saved, err := os.ReadFile(webPath)
	if err != nil || bytes.Contains(saved, []byte("unrelated-api-secret")) {
		t.Fatal("private API settings leaked into Web")
	}
}
