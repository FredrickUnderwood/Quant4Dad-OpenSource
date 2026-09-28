package handler

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/agentrunauth"
)

func TestAgentGatewayRouteRequiresCompleteConfiguration(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	digest := "sha256:" + strings.Repeat("a", 64)
	cfg := &config.Config{Security: config.SecurityConfig{Token: "fixture-login"}, Agent: config.AgentConfig{Enabled: true,
		Bootstrap:  config.AgentBootstrapConfig{Enabled: true, ControlToken: "fixture-control-token-0123456789", MCPToken: "fixture-mcp-token-0123456789-abcdef", MCPURL: "http://api/internal/mcp", CapabilityIssuer: "fixture-issuer", CapabilityPublicKeys: map[string]string{"key-1": base64.RawURLEncoding.EncodeToString(key[32:])}},
		ModelProbe: config.AgentModelProbeConfig{Enabled: true, RuntimeURL: "http://runtime", BridgeToken: "fixture-bridge-token-0123456789-abcdef"},
		Sessions:   config.AgentSessionsConfig{Enabled: true, Profiles: map[string]string{"text_only": digest}},
		Runs:       config.AgentRunsConfig{Enabled: true, SigningKeyID: "key-1", SigningPrivateKey: base64.RawURLEncoding.EncodeToString(key), Manifest: config.AgentRunManifest{Q4DVersion: "0.0.0", AgentImageDigest: digest, AgentRuntimeVersion: "fixture-v1", AdapterVersion: "fixture-v1", DSHVersion: "0.1.2-alpha.5"}, Profiles: map[string]config.AgentRunProfile{"text_only": {PromptBundleDigest: digest, SkillsDigest: digest, ToolCatalogRevision: digest, Budgets: agentrunauth.Budgets{MaxTurns: 1, MaxInputTokens: 8192, MaxOutputTokens: 512, WallTimeMS: 30000}}}},
		Gateway:    config.AgentGatewayConfig{Enabled: true, ResultDirectory: "/private/results"}}}
	if cfg.ValidateAgentGateway() != nil {
		t.Fatal("invalid route fixture")
	}
	marker := &AgentToolGatewayHandler{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })}
	request := func() int {
		server := NewServer(cfg, Handlers{AgentGateway: marker})
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, httptest.NewRequest("POST", "/internal/mcp", nil))
		return w.Code
	}
	if request() != 204 {
		t.Fatal("configured route missing")
	}
	cfg.Agent.Gateway.Enabled = false
	if request() != 404 {
		t.Fatal("disabled route registered")
	}
	cfg.Agent.Gateway.Enabled = true
	cfg.Agent.Runs.Enabled = false
	if request() != 404 {
		t.Fatal("gateway bypassed Run prerequisites")
	}
	cfg.Agent.Runs.Enabled = true
	cfg.Security.Token = ""
	if request() != 404 {
		t.Fatal("gateway bypassed login configuration")
	}
}
