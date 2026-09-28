package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsKeepSSEAndBoundRoutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: test\n\n"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
	})
	response := httptest.NewRecorder()
	Middleware(mux, nil).ServeHTTP(response, httptest.NewRequest("GET", "/stream/private-item", nil))
	if !response.Flushed || response.Code != 200 {
		t.Fatal("SSE flushing was lost")
	}
	scrape := httptest.NewRecorder()
	Handler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
	text := scrape.Body.String()
	if !strings.Contains(text, `route="/stream/{id}"`) || strings.Contains(text, "private-item") {
		t.Fatal("metric route is not bounded")
	}
	if !strings.Contains(text, "http_request_duration_seconds_bucket") {
		t.Fatal("duration metric missing")
	}
}
