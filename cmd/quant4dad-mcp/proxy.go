package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/health"
)

// Full MCP mode delegates business execution to API so file/Python state,
// validation receipts, backtest workers and the Agent catalog have one owner.
// The proxy only exposes /mcp; it cannot forward arbitrary API or admin paths.
func newAPIProxy(cfg *config.Config) (http.Handler, health.Check, error) {
	if cfg == nil || !cfg.MCP.AgentToolsEnabled || cfg.ValidateExternalMCP() != nil {
		return nil, nil, errors.New("mcp_proxy_configuration_invalid")
	}
	base, err := url.Parse(cfg.MCPAPIBaseURL())
	if err != nil {
		return nil, nil, errors.New("mcp_proxy_configuration_invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 35 * time.Second
	transport.MaxIdleConnsPerHost = 32
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(base)
			r.Out.URL.Path = strings.TrimRight(base.Path, "/") + "/external/mcp"
			r.Out.URL.RawPath = ""
			r.Out.URL.RawQuery = ""
			r.Out.Host = base.Host
			r.Out.Header = make(http.Header)
			for _, name := range []string{"Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version"} {
				if values := r.In.Header.Values(name); len(values) > 0 {
					r.Out.Header[name] = append([]string(nil), values...)
				}
			}
			r.Out.Header.Set("Authorization", "Bearer "+cfg.MCP.ExternalToken)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "mcp upstream unavailable", http.StatusBadGateway)
		},
	}
	expected := sha256.Sum256([]byte("Bearer " + cfg.MCP.ExternalToken))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		values := r.Header.Values("Authorization")
		actual := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if len(values) != 1 || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		for _, key := range []string{"Origin", "Content-Encoding", "X-Q4D-Run-Capability", "X-Q4D-Tool-Call-ID", "X-Q4D-Approval-Receipt", "Idempotency-Key"} {
			if len(r.Header.Values(key)) != 0 {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
		}
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "POST, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		proxy.ServeHTTP(w, r)
	})
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ready := func(ctx context.Context) error {
		// Check DB readiness and that this API actually serves the MCP adapter.
		for _, probe := range []struct {
			path   string
			status int
			auth   bool
		}{{"/readyz", 200, false}, {"/external/mcp", 405, true}} {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.MCPAPIBaseURL()+probe.path, nil)
			if err != nil {
				return errors.New("mcp_upstream_unavailable")
			}
			if probe.auth {
				req.Header.Set("Authorization", "Bearer "+cfg.MCP.ExternalToken)
			}
			res, err := client.Do(req)
			if err != nil {
				return errors.New("mcp_upstream_unavailable")
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			_ = res.Body.Close()
			if res.StatusCode != probe.status {
				return errors.New("mcp_upstream_unavailable")
			}
		}
		return nil
	}
	return handler, ready, nil
}
