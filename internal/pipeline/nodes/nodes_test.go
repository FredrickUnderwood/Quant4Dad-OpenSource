package nodes

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/quant4dad/internal/llm"
	"github.com/quant4dad/internal/pipeline"
)

func runKeyword(t *testing.T, cfg string, payload map[string]any) pipeline.Action {
	t.Helper()
	kf := NewKeywordFilter()
	if err := kf.Validate(json.RawMessage(cfg)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: payload}}
	act, err := kf.Process(context.Background(), rc, json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	return act
}

func TestKeyword_BlacklistDrop(t *testing.T) {
	// The default blacklist: matching any keyword blocks, case-insensitively.
	cfg := `{"keywords":["广告","SPAM"]}`
	if act := runKeyword(t, cfg, map[string]any{"title": "这是一条广告推送"}); act != pipeline.ActionDrop {
		t.Fatalf("want drop, got %s", act)
	}
	if act := runKeyword(t, cfg, map[string]any{"title": "命中 spam 关键词"}); act != pipeline.ActionDrop {
		t.Fatalf("want drop (case-insensitive), got %s", act)
	}
	if act := runKeyword(t, cfg, map[string]any{"title": "正常财经新闻"}); act != pipeline.ActionPass {
		t.Fatalf("want pass, got %s", act)
	}
}

func TestKeyword_Whitelist(t *testing.T) {
	// Whitelist: matching no keyword blocks, matching one passes.
	cfg := `{"keywords":["财经","股市"],"list_type":"whitelist"}`
	if act := runKeyword(t, cfg, map[string]any{"title": "今日股市行情"}); act != pipeline.ActionPass {
		t.Fatalf("want pass (hit), got %s", act)
	}
	if act := runKeyword(t, cfg, map[string]any{"title": "娱乐八卦新闻"}); act != pipeline.ActionDrop {
		t.Fatalf("want drop (no hit), got %s", act)
	}
}

func TestKeyword_BlankKeywordsIgnored(t *testing.T) {
	// Blank and whitespace-only keywords are ignored and take no part in matching, so an
	// empty string cannot match everything.
	cfg := `{"keywords":["  ","广告"]}`
	if act := runKeyword(t, cfg, map[string]any{"title": "正常财经新闻"}); act != pipeline.ActionPass {
		t.Fatalf("want pass (blank keyword ignored), got %s", act)
	}
}

func TestKeyword_EmptyKeywordsRejected(t *testing.T) {
	if err := NewKeywordFilter().Validate(json.RawMessage(`{"keywords":[]}`)); err == nil {
		t.Fatal("expected validation error for empty keywords")
	}
}

// fakeClient and fakeResolver are for testing the AI node without making real requests.
type fakeClient struct{ resp llm.CompleteResponse }

func (fakeClient) Provider() string { return "fake" }
func (f fakeClient) Complete(context.Context, llm.CompleteRequest) (llm.CompleteResponse, error) {
	return f.resp, nil
}

type fakeResolver struct{ c llm.Client }

func (r fakeResolver) Resolve(context.Context, string) (llm.Client, error) { return r.c, nil }

func TestAIAnalysis_RendersAndWritesBack(t *testing.T) {
	client := fakeClient{resp: llm.CompleteResponse{
		Text: `{"sentiment":"positive"}`, Model: "test-model", TokensPrompt: 10, TokensCompletion: 5,
	}}
	ai := NewAIAnalysis(fakeResolver{c: client})

	cfg := `{"provider":"fake","prompt":"分析:{{.payload.title}}","json_output":true,"write_to":"analysis"}`
	if err := ai.Validate(json.RawMessage(cfg)); err != nil {
		t.Fatalf("validate: %v", err)
	}

	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{"title": "茅台大涨"}}}
	act, err := ai.Process(context.Background(), rc, json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if act != pipeline.ActionPass {
		t.Fatalf("want pass, got %s", act)
	}

	got, ok := rc.Msg.Payload["analysis"].(map[string]any)
	if !ok || got["sentiment"] != "positive" {
		t.Fatalf("payload write-back wrong: %#v", rc.Msg.Payload["analysis"])
	}

	air := rc.AIResults()
	if len(air) != 1 {
		t.Fatalf("ai results = %d, want 1", len(air))
	}
	if air[0].PromptSnapshot != "分析:茅台大涨" {
		t.Fatalf("prompt snapshot = %q", air[0].PromptSnapshot)
	}
	if air[0].Model != "test-model" || air[0].TokensPrompt != 10 {
		t.Fatalf("ai invocation meta wrong: %+v", air[0])
	}
}

// runAIGate runs the AI node once against a fixed model output and returns the action along
// with how many AI call records it produced, which makes both "kept vs dropped" and "a call
// record is still registered after a drop, so it can still be persisted" assertable.
func runAIGate(t *testing.T, modelText, cfg string) (pipeline.Action, int) {
	t.Helper()
	ai := NewAIAnalysis(fakeResolver{c: fakeClient{resp: llm.CompleteResponse{Text: modelText, Model: "m"}}})
	if err := ai.Validate(json.RawMessage(cfg)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{"title": "x"}}}
	act, err := ai.Process(context.Background(), rc, json.RawMessage(cfg))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	return act, len(rc.AIResults())
}

func TestAIGate_KeepAndDropByScore(t *testing.T) {
	cfg := `{"provider":"fake","prompt":"p","json_output":true,"gate":{"field":"score","op":"gte","value":0.7}}`

	if act, n := runAIGate(t, `{"score":0.9}`, cfg); act != pipeline.ActionPass {
		t.Fatalf("score 0.9 want pass, got %s", act)
	} else if n != 1 {
		t.Fatalf("want 1 ai record on pass, got %d", n)
	}

	// Failing the keep condition must drop, but the AI call record must still be registered:
	// a drop does not affect persistence.
	if act, n := runAIGate(t, `{"score":0.3}`, cfg); act != pipeline.ActionDrop {
		t.Fatalf("score 0.3 want drop, got %s", act)
	} else if n != 1 {
		t.Fatalf("want 1 ai record on drop, got %d", n)
	}
}

func TestAIGate_TruthyAndOnMatchDrop(t *testing.T) {
	keepCfg := `{"provider":"fake","prompt":"p","json_output":true,"gate":{"field":"relevant","op":"truthy"}}`
	if act, _ := runAIGate(t, `{"relevant":true}`, keepCfg); act != pipeline.ActionPass {
		t.Fatalf("relevant true want pass, got %s", act)
	}
	if act, _ := runAIGate(t, `{"relevant":false}`, keepCfg); act != pipeline.ActionDrop {
		t.Fatalf("relevant false want drop, got %s", act)
	}

	// on_match=drop used as a filter: matching spam drops.
	dropCfg := `{"provider":"fake","prompt":"p","json_output":true,"gate":{"field":"category","op":"eq","value":"spam","on_match":"drop"}}`
	if act, _ := runAIGate(t, `{"category":"spam"}`, dropCfg); act != pipeline.ActionDrop {
		t.Fatalf("spam want drop, got %s", act)
	}
	if act, _ := runAIGate(t, `{"category":"news"}`, dropCfg); act != pipeline.ActionPass {
		t.Fatalf("news want pass, got %s", act)
	}
}

func TestAIGate_InOperatorAndMissingField(t *testing.T) {
	inCfg := `{"provider":"fake","prompt":"p","json_output":true,"gate":{"field":"sentiment","op":"in","value":["positive","neutral"]}}`
	if act, _ := runAIGate(t, `{"sentiment":"positive"}`, inCfg); act != pipeline.ActionPass {
		t.Fatalf("positive in set want pass, got %s", act)
	}
	if act, _ := runAIGate(t, `{"sentiment":"negative"}`, inCfg); act != pipeline.ActionDrop {
		t.Fatalf("negative not in set want drop, got %s", act)
	}

	// A missing field: with the default on_error=fail this must return an error.
	missCfg := `{"provider":"fake","prompt":"p","json_output":true,"gate":{"field":"score","op":"gte","value":0.5}}`
	ai := NewAIAnalysis(fakeResolver{c: fakeClient{resp: llm.CompleteResponse{Text: `{"other":1}`}}})
	rc := &pipeline.RunContext{Msg: &pipeline.Message{Payload: map[string]any{}}}
	if _, err := ai.Process(context.Background(), rc, json.RawMessage(missCfg)); err == nil {
		t.Fatal("missing gate field should fail under on_error=fail")
	}
}

func TestAIGate_ValidationRequiresJSONOutput(t *testing.T) {
	// A gate configured without json_output must be rejected at Validate time.
	cfg := `{"provider":"fake","prompt":"p","gate":{"field":"score","op":"gte","value":0.5}}`
	if err := NewAIAnalysis(fakeResolver{}).Validate(json.RawMessage(cfg)); err == nil {
		t.Fatal("gate without json_output should fail validation")
	}
}
