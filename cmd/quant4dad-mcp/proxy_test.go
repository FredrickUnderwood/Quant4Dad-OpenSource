package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quant4dad/config"
)

func TestFullMCPProxyPreservesProtocolAndRestrictsAuthority(t *testing.T) {
	const token = "fixture-external-mcp-token-0123456789"
	forwarded := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/prefix/readyz" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path != "/prefix/external/mcp" || r.URL.RawQuery != "" {
			t.Errorf("unexpected upstream URL %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing dedicated token")
		}
		if r.Method == "GET" {
			w.WriteHeader(405)
			return
		}
		forwarded++
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Arbitrary") != "" {
			t.Error("unrelated authority forwarded")
		}
		if r.Header.Get("Mcp-Session-Id") != "session-1" || r.Header.Get("Mcp-Protocol-Version") != "2025-11-25" {
			t.Error("protocol headers lost")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"jsonrpc":"2.0","id":1,"method":"ping"}` {
			t.Error("body changed")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "session-1")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.MCP.ExternalToken = token
	cfg.MCP.AgentToolsEnabled = true
	cfg.Agent.Gateway.Enabled, cfg.Agent.Runs.Enabled = true, true
	cfg.Web.APIBaseURL = upstream.URL + "/prefix"
	proxy, ready, err := newAPIProxy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Session-Id", "session-1")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "q4d_auth=must-not-forward")
	req.Header.Set("X-Arbitrary", "must-not-forward")
	res := httptest.NewRecorder()
	proxy.ServeHTTP(res, req)
	if res.Code != 200 || res.Header().Get("Mcp-Session-Id") != "session-1" || !strings.Contains(res.Body.String(), "event: message\n") {
		t.Fatalf("protocol lost: %d %v %s", res.Code, res.Header(), res.Body)
	}
	for _, test := range []struct {
		path, header, value string
		status              int
	}{
		{"/api/v1/strategies", "", "", 404},
		{"/mcp", "Authorization", "Bearer wrong", 401},
		{"/mcp?token=invalid", "", "", 400},
		{"/mcp", "Origin", "https://untrusted.example", 400},
		{"/mcp", "X-Q4D-Run-Capability", "untrusted", 400},
	} {
		r := httptest.NewRequest("POST", test.path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+token)
		if test.header != "" {
			r.Header.Set(test.header, test.value)
		}
		w := httptest.NewRecorder()
		proxy.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Errorf("%s/%s got %d", test.path, test.header, w.Code)
		}
	}
	if forwarded != 1 {
		t.Fatalf("rejected requests reached upstream: %d", forwarded)
	}
	upstream.Close()
	if ready(context.Background()) == nil {
		t.Fatal("unavailable API reported ready")
	}
}
