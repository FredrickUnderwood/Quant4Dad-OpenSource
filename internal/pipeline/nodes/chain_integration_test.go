package nodes

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/llm"
	"github.com/quant4dad/internal/notify"
	"github.com/quant4dad/internal/pipeline"
)

func TestKeywordAIAndFeishuChainThroughHTTP(t *testing.T) {
	var modelCalls, deliveries atomic.Int32
	var rejectDelivery atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || !strings.Contains(string(body), "Agent测试新闻") {
			t.Errorf("unexpected model request: %s %s", r.URL.Path, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"keep\":true,\"rating\":\"通过\"}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer model.Close()
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliveries.Add(1)
		body, _ := io.ReadAll(r.Body)
		var message map[string]any
		if sonic.Unmarshal(body, &message) != nil || message["msg_type"] != "post" ||
			!strings.Contains(string(body), "Agent测试：Agent测试新闻") || !strings.Contains(string(body), "评级：通过") {
			t.Errorf("unexpected delivery: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if rejectDelivery.Load() {
			_, _ = io.WriteString(w, `{"code":123,"msg":"fixture rejected"}`)
		} else {
			_, _ = io.WriteString(w, `{"code":0}`)
		}
	}))
	defer webhook.Close()
	resolver := llm.NewDynamicFactory(func(context.Context) (map[string]llm.ProviderConfig, error) {
		return map[string]llm.ProviderConfig{"fixture": {Type: "openai", BaseURL: model.URL + "/v1", DefaultModel: "fixture"}}, nil
	})
	notifier := notify.NewDynamicFactory(func(context.Context) (notify.ChannelConfigs, error) {
		return notify.ChannelConfigs{Feishu: &notify.FeishuConfig{WebhookURL: webhook.URL}}, nil
	})
	ex := pipeline.NewExecutor(BuildRegistry(resolver, notifier))
	nodes := []pipeline.Node{
		{Key: "filter", Type: "keyword_filter", Config: []byte(`{"keywords":["Agent测试"],"list_type":"whitelist"}`)},
		{Key: "ai", Type: "ai_analysis", Config: []byte(`{"provider":"fixture","prompt":"判断：{{.payload.title}}","json_output":true,"gate":{"field":"keep","op":"truthy","on_match":"keep"}}`)},
		{Key: "delivery", Type: "delivery", Config: []byte(`{"channel":"feishu","title":"Agent测试：{{.payload.title}}","body":"评级：{{.payload.ai_result.rating}}"}`)},
	}
	edges := []pipeline.Edge{{From: "filter", To: "ai"}, {From: "ai", To: "delivery"}}
	run := func(title string, dry bool) pipeline.ExecResult {
		return ex.Run(context.Background(), nodes, edges, &pipeline.Message{Payload: map[string]any{"title": title}}, dry)
	}
	passed := run("Agent测试新闻", false)
	if passed.Err != nil || passed.Status != "passed" || len(passed.Traces) != 3 || modelCalls.Load() != 1 || deliveries.Load() != 1 {
		t.Fatalf("chain did not complete: %+v", passed)
	}
	dropped := run("普通新闻", false)
	if dropped.Status != "dropped" || len(dropped.Traces) != 1 || modelCalls.Load() != 1 || deliveries.Load() != 1 {
		t.Fatalf("filter did not suppress downstream work: %+v", dropped)
	}
	preview := run("Agent测试新闻", true)
	if preview.Err != nil || preview.Traces[2].DeliveryPreview == nil || deliveries.Load() != 1 {
		t.Fatalf("preview sent a notification or omitted rendered content: %+v", preview)
	}
	rejectDelivery.Store(true)
	failed := run("Agent测试新闻", false)
	if failed.Err == nil || failed.Status != "failed" || failed.FailedAtNode != "delivery" || deliveries.Load() != 2 {
		t.Fatalf("delivery failure reported as success: %+v", failed)
	}
}
