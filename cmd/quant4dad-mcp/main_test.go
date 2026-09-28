package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appLog "github.com/quant4dad/internal/logger"

	"github.com/quant4dad/internal/mcp"
	"github.com/quant4dad/internal/observability"
)

func TestReadinessUnderMCPRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "ready", status: http.StatusOK},
		{name: "unavailable", err: errors.New("upstream unavailable"), status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			handler := monitoredHTTPHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("readiness reached the MCP protocol handler")
			}), func(context.Context) error {
				checks++
				return tc.err
			})
			for _, path := range []string{"/readyz", "/mcp/readyz"} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != tc.status {
					t.Fatalf("%s returned %d, want %d", path, response.Code, tc.status)
				}
			}
			if checks != 2 {
				t.Fatalf("readiness checks = %d, want 2", checks)
			}
		})
	}
}

func TestHTTPTraceMetricsAndSSE(t *testing.T) {
	handler := newHTTPHandler(mcp.NewServer("test", "test"), "test-token")
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(appLog.TraceHeader, "gateway-trace")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || response.Header().Get(appLog.TraceHeader) != "gateway-trace" {
		t.Fatalf("unexpected MCP response: %d %v %s", response.Code, response.Header(), response.Body)
	}
	if !response.Flushed || response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body.String(), "event: message\n") {
		t.Fatalf("middleware broke SSE: flushed=%t, headers=%v body=%s", response.Flushed, response.Header(), response.Body)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get(appLog.TraceHeader) == "" {
		t.Fatalf("auth or trace generation broken: %d %v", response.Code, response.Header())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health probe requires auth: %d", response.Code)
	}
	response = httptest.NewRecorder()
	observability.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{`route="/mcp",status="200"`, `route="/mcp",status="401"`, `route="/healthz",status="200"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("missing metric %s\n%s", want, response.Body)
		}
	}
}
