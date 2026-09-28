package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/pipeline"
	"github.com/quant4dad/internal/pipeline/nodes"
	"github.com/quant4dad/internal/service"
)

func TestPipelinePreviewDoesNotSaveOrSend(t *testing.T) {
	reg := pipeline.NewRegistry()
	reg.Register(nodes.NewDelivery(nil))
	// Nil repository and notifier turn any persistence or delivery into a failure.
	router := gin.New()
	NewPipelineHandler(service.NewPipelineService(nil, reg), nil).Register(router.Group("/api/v1"))
	for _, channel := range []string{"email", "feishu"} {
		body := `{"pipeline":{"name":"unsaved","status":"enabled","nodes":[{"node_key":"out","type":"delivery","config":{"channel":"` + channel + `","body":"{{.payload.title}}"}}]},"sample_event":{"title":"本地预览"}}`
		r := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/preview", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var result map[string]any
		if w.Code != 200 || sonic.Unmarshal(w.Body.Bytes(), &result) != nil || !bytes.Contains(w.Body.Bytes(), []byte(`"delivery_preview"`)) || !bytes.Contains(w.Body.Bytes(), []byte("本地预览")) {
			t.Fatalf("preview failed: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"pipeline":{"name":"invalid","nodes":[]}}`, strings.Repeat("x", 256*1024+1)} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines/preview", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid preview accepted: %d", w.Code)
		}
	}
}
