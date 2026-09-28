package handler

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/quant4dad/internal/application"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
)

func TestAgentToolResultOwnershipRetentionAndReadOnlyProjection(t *testing.T) {
	_, db := modelHTTPServer(t)
	if err := db.AutoMigrate(&domain.AgentToolAudit{}); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "results")
	artifacts, err := repository.NewAgentToolArtifactRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := artifacts.Put([]byte(`{"data":{"id":1,"version":1,"name":"<script>untrusted</script>"},"untrusted_data":true}`))
	if err != nil {
		t.Fatal(err)
	}
	run, call := "01M00000000000000000000001", "01M00000000000000000000002"
	row := domain.AgentToolAudit{ID: "private-audit-id", ActorID: agentLocalActor, RunID: run, ToolCallID: call, ToolName: "create_strategy", Status: "succeeded", Risk: "R2", Started: true, ResultRef: ref, IdempotencyKey: "private-key", EnvelopeDigest: "private-envelope"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	audit := service.NewAgentToolAuditService(repository.NewAgentToolAuditRepository(db), artifacts)
	if _, err := audit.Result(context.Background(), "another-actor", run, call); !errors.Is(err, service.ErrAgentRunNotFound) {
		t.Fatal("cross actor result", err)
	}
	router := gin.New()
	NewAgentToolResultHandler(application.NewAgentToolResultApplication(audit, nil)).Register(router.Group("/api/v1"))
	path := "/api/v1/agent/runs/" + run + "/tools/" + call
	read := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("result cached")
		}
		return w.Body.String()
	}
	body := read(path, 200)
	for _, private := range []string{ref, "private-audit-id", "private-key", "private-envelope", "receipt"} {
		if strings.Contains(body, private) {
			t.Fatal("private audit field escaped")
		}
	}
	read(path+"?result_ref=forged", 400)
	if err := os.WriteFile(filepath.Join(dir, ref), []byte(`{"forged":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	read(path, 503)
	if err := db.Model(&row).Updates(map[string]any{"status": "executing", "result_ref": ""}).Error; err != nil {
		t.Fatal(err)
	}
	if body := read(path, 200); strings.Contains(body, `"result":`) || !strings.Contains(body, `"executing"`) {
		t.Fatal(body)
	}
	// No Effect table is installed: a GET must not invoke replay/repair at all.
	if err := db.Model(&row).Updates(map[string]any{"status": "expired", "error_code": "agent_tool_result_expired"}).Error; err != nil {
		t.Fatal(err)
	}
	if body := read(path, 200); strings.Contains(body, `"result":`) || !strings.Contains(body, `"expired"`) {
		t.Fatal(body)
	}
}
