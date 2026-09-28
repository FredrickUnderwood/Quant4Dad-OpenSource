package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/config"
	"github.com/quant4dad/internal/logger"
	"github.com/quant4dad/internal/observability"
)

func TestStandaloneHTTPLogs(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHTTPObservabilityProcess$")
	cmd.Env = append(os.Environ(), "QUANT4DAD_HTTP_TEST_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("HTTP check failed: %v\n%s", err, output)
	}
	for _, secret := range []string{"private-panic-business-value", "private-cookie-token", "private-bearer-token"} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatal("request credentials or panic value leaked")
		}
	}
	access, panics := 0, 0
	for _, line := range bytes.Split(output, []byte("\n")) {
		var entry map[string]any
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if entry["msg"] == "http handler panic" {
			panics++
			if entry["trace_id"] == nil || entry["stack"] == nil || entry["panic_type"] != "string" {
				t.Fatal("safe panic diagnostics missing")
			}
		}
		if entry["path"] == "/test/items/101" {
			access++
			if entry["trace_id"] != "gateway-trace" {
				t.Fatal("request trace lost")
			}
		}
	}
	if access != 1 || panics != 1 {
		t.Fatalf("access=%d panics=%d", access, panics)
	}
}

func TestHTTPObservabilityProcess(t *testing.T) {
	if os.Getenv("QUANT4DAD_HTTP_TEST_CHILD") != "1" {
		return
	}
	if err := logger.Init(logger.Config{Level: "info"}); err != nil {
		t.Fatal(err)
	}
	defer logger.Shutdown()
	server := NewServer(&config.Config{}, Handlers{})
	server.engine.GET("/test/items/:id", func(c *gin.Context) {
		if logger.TraceIDFromContext(c.Request.Context()) != "gateway-trace" {
			t.Error("trace not propagated")
		}
		c.Status(http.StatusNoContent)
	})
	server.engine.GET("/test/panic", func(c *gin.Context) { panic("private-panic-business-value") })
	for _, path := range []string{"/test/items/101", "/test/items/102", "/test/panic"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set(logger.TraceHeader, "gateway-trace")
		request.Header.Set("Cookie", "q4d_auth=private-cookie-token")
		request.Header.Set("Authorization", "Bearer private-bearer-token")
		response := httptest.NewRecorder()
		server.engine.ServeHTTP(response, request)
		if response.Header().Get(logger.TraceHeader) != "gateway-trace" {
			t.Error("response trace missing")
		}
	}
	scrape := httptest.NewRecorder()
	observability.Handler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`http_requests_total{method="GET",route="/test/items/:id",status="204"} 2`, `http_requests_total{method="GET",route="/test/panic",status="500"} 1`, "http_request_duration_seconds_bucket"} {
		if !strings.Contains(scrape.Body.String(), want) {
			t.Errorf("missing metric %s", want)
		}
	}
	if strings.Contains(scrape.Body.String(), "/test/items/101") {
		t.Error("metrics contain raw IDs")
	}
}
