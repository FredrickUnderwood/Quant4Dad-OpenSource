package agentbridge

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
)

func TestAgentModelProbeWire(t *testing.T) {
	now := time.Now().UnixMilli()
	request := domain.AgentModelProbeRequest{Provider: "fixture", Model: "alpha", ModelConfigRevision: strings.Repeat("a", 32), ProbeVersion: domain.AgentModelProbeVersion}
	value := domain.AgentModelProbeResult{Provider: request.Provider, Model: request.Model, ModelConfigRevision: request.ModelConfigRevision, ProbeVersion: request.ProbeVersion, Status: "ready", Reason: "probe_passed", CheckedAtMS: now, ExpiresAtMS: now + 86400000, ContextWindow: 8192, MaxOutputTokens: 512, ContextSource: "explicit-config", ModelTurns: 2, ToolCalls: 1, FirstEventMS: 1}
	body, _ := sonic.Marshal(value)
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/q4d/v1/models/probe" || r.Method != "POST" {
			t.Error("wrong probe route")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	if result, err := client.ProbeModel(context.Background(), request); err != nil || result.Status != "ready" {
		t.Fatal("valid probe rejected", err)
	}
	good := string(body)
	for _, bad := range []string{strings.Replace(good, `"tool_calls":1`, `"tool_calls":0`, 1), strings.Replace(good, `"ready"`, `"probing"`, 1), strings.Replace(good, `"provider":"fixture"`, `"provider":"another"`, 1), strings.Replace(good, `"model_turns":2`, `"model_turns":2,"model_turns":2`, 1)} {
		body = []byte(bad)
		if _, err := client.ProbeModel(context.Background(), request); err == nil {
			t.Fatal("invalid probe accepted")
		}
	}
}
