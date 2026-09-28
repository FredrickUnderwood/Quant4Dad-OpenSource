package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/bytedance/sonic"

	"github.com/quant4dad/internal/llm"
	"github.com/quant4dad/internal/pipeline"
)

// Resolver resolves an LLM client by provider name; llm.DynamicFactory implements it,
// reading the provider config from the setting table at runtime.
type Resolver interface {
	Resolve(ctx context.Context, provider string) (llm.Client, error)
}

// aiConfig is the AI analysis node's config. Prompt is a Go text/template, which can reach
// upstream fields through {{.payload.xxx}} and {{.meta.xxx}}.
type aiConfig struct {
	Provider    string  `json:"provider"`    // key into the configured LLM providers
	Model       string  `json:"model"`       // empty uses the provider's default model
	System      string  `json:"system"`      // system prompt
	Prompt      string  `json:"prompt"`      // user prompt template
	JSONOutput  bool    `json:"json_output"` // ask the model for JSON and parse it
	WriteTo     string  `json:"write_to"`    // payload key the result is written back to; defaults to ai_result
	MaxTokens   int     `json:"max_tokens"`  // defaults to 1024
	Temperature float64 `json:"temperature"` // defaults to 0
	OnError     string  `json:"on_error"`    // fail (the default, aborts) / continue (records but lets it through)
	Gate        *aiGate `json:"gate"`        // retention check: keep or drop based on a structured output field; nil means no check
}

// aiGate is a retention check driven by structured output. It takes the value at Field (a
// dotted path), compares it against Value with Op to get a boolean predicate, and OnMatch
// decides what happens when the predicate holds; when it does not, the opposite action is
// taken. A gate only applies when json_output=true and the model's output is a JSON object.
type aiGate struct {
	Field   string `json:"field"`    // field in the structured output, as a dotted path, e.g. "score" or "result.relevant"
	Op      string `json:"op"`       // truthy/falsy/eq/ne/gt/gte/lt/lte/in/contains
	Value   any    `json:"value"`    // the value to compare against; an array for in; ignored for truthy/falsy
	OnMatch string `json:"on_match"` // when the predicate holds: keep (the default) or drop
}

// gateOps lists every comparison operator a gate supports.
var gateOps = map[string]bool{
	"truthy": true, "falsy": true,
	"eq": true, "ne": true,
	"gt": true, "gte": true, "lt": true, "lte": true,
	"in": true, "contains": true,
}

// gateNeedsValue marks the operators that require a Value; truthy and falsy do not.
func gateNeedsValue(op string) bool { return op != "truthy" && op != "falsy" }

// AIAnalysis renders the prompt, calls the model, writes the result back into the payload,
// and records the AI call for the application layer to persist.
type AIAnalysis struct {
	resolver Resolver
}

func NewAIAnalysis(resolver Resolver) *AIAnalysis { return &AIAnalysis{resolver: resolver} }

func (*AIAnalysis) Type() string        { return "ai_analysis" }
func (*AIAnalysis) DisplayName() string { return "AI 分析" }
func (*AIAnalysis) Category() string    { return "ai" }

func (*AIAnalysis) ConfigSchema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "required": ["provider", "prompt"],
  "properties": {
    "provider": {"type": "string", "title": "模型提供方", "x_enum_source": "llm_providers", "description": "从设置中配置的大模型里选择"},
    "model": {"type": "string", "title": "模型(留空用默认)"},
    "system": {"type": "string", "title": "系统提示"},
    "prompt": {"type": "string", "title": "提示词模板", "description": "支持 {{.payload.字段}}"},
    "json_output": {"type": "boolean", "default": false, "title": "结构化JSON输出"},
    "write_to": {"type": "string", "default": "ai_result", "title": "结果写回字段"},
    "max_tokens": {"type": "integer", "default": 1024, "title": "最大输出token"},
    "temperature": {"type": "number", "default": 0, "title": "温度"},
    "on_error": {"type": "string", "enum": ["fail", "continue"], "default": "fail", "title": "出错策略"},
    "gate": {
      "type": "object",
      "title": "留存判断(基于结构化输出字段)",
      "description": "需开启结构化JSON输出；取字段值判断是否留存",
      "properties": {
        "field": {"type": "string", "title": "判断字段(点分路径)"},
        "op": {"type": "string", "enum": ["truthy", "falsy", "eq", "ne", "gt", "gte", "lt", "lte", "in", "contains"], "title": "比较算子"},
        "value": {"title": "比较值(in为数组；truthy/falsy可省略)"},
        "on_match": {"type": "string", "enum": ["keep", "drop"], "default": "keep", "title": "命中后动作"}
      }
    }
  }
}`)
}

func (*AIAnalysis) Validate(raw json.RawMessage) error {
	cfg, err := parseAIConfig(raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Provider) == "" {
		return errors.New("ai_analysis: provider must not be empty")
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		return errors.New("ai_analysis: prompt must not be empty")
	}
	if _, err := template.New("p").Parse(cfg.Prompt); err != nil {
		return fmt.Errorf("ai_analysis: invalid prompt template: %w", err)
	}
	if cfg.Gate != nil {
		if err := validateGate(cfg); err != nil {
			return err
		}
	}
	return nil
}

// validateGate checks the retention config: a non-empty field, a valid operator, a value
// present for operators that need one, and json_output=true — a gate depends on structured
// output, without which there is no field to read.
func validateGate(cfg aiConfig) error {
	g := cfg.Gate
	if !cfg.JSONOutput {
		return errors.New("ai_analysis: json_output must be enabled when a gate is configured, since the gate reads a field from structured output")
	}
	if strings.TrimSpace(g.Field) == "" {
		return errors.New("ai_analysis: gate.field must not be empty")
	}
	if !gateOps[g.Op] {
		return fmt.Errorf("ai_analysis: invalid gate.op %q", g.Op)
	}
	if gateNeedsValue(g.Op) && g.Value == nil {
		return fmt.Errorf("ai_analysis: gate.op=%s requires a value", g.Op)
	}
	if g.Op == "in" {
		if _, ok := g.Value.([]any); !ok {
			return errors.New("ai_analysis: the value for gate.op=in must be an array")
		}
	}
	if g.OnMatch != "" && g.OnMatch != "keep" && g.OnMatch != "drop" {
		return fmt.Errorf("ai_analysis: invalid gate.on_match %q (expect keep or drop)", g.OnMatch)
	}
	return nil
}

func (a *AIAnalysis) Process(ctx context.Context, rc *pipeline.RunContext, raw json.RawMessage) (pipeline.Action, error) {
	cfg, err := parseAIConfig(raw)
	if err != nil {
		return pipeline.ActionPass, err
	}

	prompt, err := renderPrompt(cfg.Prompt, rc.Msg)
	if err != nil {
		return a.onError(rc, cfg, fmt.Errorf("ai_analysis: failed to render prompt: %w", err))
	}

	client, err := a.resolver.Resolve(ctx, cfg.Provider)
	if err != nil {
		return a.onError(rc, cfg, err)
	}

	start := time.Now()
	resp, err := client.Complete(ctx, llm.CompleteRequest{
		Model:       cfg.Model,
		System:      cfg.System,
		Prompt:      prompt,
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
		JSONOutput:  cfg.JSONOutput,
	})
	if err != nil {
		return a.onError(rc, cfg, fmt.Errorf("ai_analysis: model call failed: %w", err))
	}

	// Parse the output: in JSON mode try to parse an object, otherwise wrap it as text.
	output, writeVal := normalizeOutput(resp.Text, cfg.JSONOutput)

	writeTo := cfg.WriteTo
	if writeTo == "" {
		writeTo = "ai_result"
	}
	rc.Msg.Payload[writeTo] = writeVal

	rc.RecordAI(pipeline.AIInvocation{
		Provider:         cfg.Provider,
		Model:            resp.Model,
		PromptSnapshot:   prompt,
		Output:           output,
		TokensPrompt:     resp.TokensPrompt,
		TokensCompletion: resp.TokensCompletion,
		LatencyMS:        time.Since(start).Milliseconds(),
	})

	// Retention check. The AI call was already recorded above — the executor still collects
	// AIResults after a drop, so persistence is unaffected — and here the structured output
	// field decides whether this message is kept or dropped.
	if cfg.Gate != nil {
		act, reason, gerr := evalGate(*cfg.Gate, writeVal)
		if gerr != nil {
			return a.onError(rc, cfg, fmt.Errorf("ai_analysis: retention check failed: %w", gerr))
		}
		if act == pipeline.ActionDrop {
			rc.Note(reason) // record why it was dropped in the lineage, for diagnosis
		}
		return act, nil
	}

	return pipeline.ActionPass, nil
}

// evalGate takes the value at g.Field in the structured output, compares it with the
// operator to get a predicate, then turns that into Pass (keep) or Drop according to
// OnMatch. When the structured output is not an object or the field is absent it returns an
// error, which onError handles.
func evalGate(g aiGate, structured any) (pipeline.Action, string, error) {
	obj, ok := structured.(map[string]any)
	if !ok {
		return pipeline.ActionPass, "", fmt.Errorf("the structured output is not a JSON object, so field %q cannot be read; check that the model returns JSON as instructed", g.Field)
	}
	val, found := lookupPath(obj, g.Field)
	if !found {
		return pipeline.ActionPass, "", fmt.Errorf("field %q is absent from the structured output", g.Field)
	}
	matched, err := matchGate(g.Op, val, g.Value)
	if err != nil {
		return pipeline.ActionPass, "", err
	}
	// on_match=keep (the default): the predicate holding keeps the message.
	// on_match=drop: the predicate holding drops it.
	keepOnMatch := g.OnMatch != "drop"
	if matched == keepOnMatch {
		return pipeline.ActionPass, "", nil
	}
	reason := fmt.Sprintf("dropped by retention check: field %s=%v does not satisfy the keep condition (op=%s value=%v on_match=%s)",
		g.Field, val, g.Op, g.Value, defaultStr(g.OnMatch, "keep"))
	return pipeline.ActionDrop, reason, nil
}

// matchGate compares the actual value got against the expected want with the operator, and
// reports whether the predicate holds.
func matchGate(op string, got, want any) (bool, error) {
	switch op {
	case "truthy":
		return isTruthy(got), nil
	case "falsy":
		return !isTruthy(got), nil
	case "eq":
		return looseEqual(got, want), nil
	case "ne":
		return !looseEqual(got, want), nil
	case "gt", "gte", "lt", "lte":
		gf, gerr := toFloat(got)
		wf, werr := toFloat(want)
		if gerr != nil || werr != nil {
			return false, fmt.Errorf("operator %s requires numbers: got=%v want=%v", op, got, want)
		}
		switch op {
		case "gt":
			return gf > wf, nil
		case "gte":
			return gf >= wf, nil
		case "lt":
			return gf < wf, nil
		default:
			return gf <= wf, nil
		}
	case "in":
		arr, ok := want.([]any)
		if !ok {
			return false, fmt.Errorf("the value for operator in must be an array")
		}
		for _, item := range arr {
			if looseEqual(got, item) {
				return true, nil
			}
		}
		return false, nil
	case "contains":
		return containsVal(got, want), nil
	default:
		return false, fmt.Errorf("unsupported operator %q", op)
	}
}

// lookupPath reads a value out of an object by dotted path.
func lookupPath(obj map[string]any, path string) (any, bool) {
	cur := any(obj)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// isTruthy reduces any value to a boolean: a bool is itself; a non-zero number is true; a
// string is true unless it is "", "false" or "0" (case-insensitively); nil is false.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s != "" && s != "false" && s != "0"
	default:
		if f, err := toFloat(v); err == nil {
			return f != 0
		}
		return true
	}
}

// looseEqual is a lenient equality: if both sides convert to numbers it compares
// numerically; if either is a bool it compares truthiness; otherwise it compares strings.
// This absorbs JSON decoding reading numbers as float64, a model writing a boolean as a
// string, and similar.
func looseEqual(a, b any) bool {
	if af, ae := toFloat(a); ae == nil {
		if bf, be := toFloat(b); be == nil {
			return af == bf
		}
	}
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	switch {
	case aok && bok:
		return ab == bb
	case aok:
		return ab == isTruthy(b)
	case bok:
		return bb == isTruthy(a)
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// containsVal reports whether got "contains" want: a substring check when got is a string,
// and lenient element equality when it is an array.
func containsVal(got, want any) bool {
	switch t := got.(type) {
	case string:
		return strings.Contains(t, fmt.Sprint(want))
	case []any:
		for _, item := range t {
			if looseEqual(item, want) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// toFloat converts a value to float64 where it can, accepting float64, the integer types,
// json.Number and numeric strings.
func toFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case json.Number:
		return t.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	default:
		return 0, fmt.Errorf("not a number: %v", v)
	}
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// onError decides, from the config, whether a failure aborts or is let through. When it is
// let through (continue), the failure reason goes into the node's lineage via rc.Note ->
// NodeTrace.Error rather than being silently swallowed, which keeps it diagnosable; fail
// instead returns the error to the executor and aborts.
func (*AIAnalysis) onError(rc *pipeline.RunContext, cfg aiConfig, err error) (pipeline.Action, error) {
	if cfg.OnError == "continue" {
		rc.Note(err.Error())
		return pipeline.ActionPass, nil
	}
	return pipeline.ActionPass, err
}

// normalizeOutput returns the JSON to persist along with the value to write back into the
// payload. In JSON mode with a successful parse both are the structured object; otherwise
// they are wrapped as {"text": "..."}.
func normalizeOutput(text string, jsonMode bool) (json.RawMessage, any) {
	trimmed := strings.TrimSpace(text)
	if jsonMode && (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) {
		var v any
		if err := sonic.UnmarshalString(trimmed, &v); err == nil {
			return json.RawMessage(trimmed), v
		}
	}
	wrapped := map[string]any{"text": text}
	b, _ := sonic.Marshal(wrapped)
	return json.RawMessage(b), wrapped
}

func renderPrompt(tmpl string, msg *pipeline.Message) (string, error) {
	t, err := template.New("prompt").Option("missingkey=zero").Parse(tmpl)
	if err != nil {
		return "", err
	}
	data := map[string]any{"payload": msg.Payload, "meta": msg.Meta}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func parseAIConfig(raw json.RawMessage) (aiConfig, error) {
	var cfg aiConfig
	if len(raw) == 0 {
		return cfg, errors.New("ai_analysis: config is empty")
	}
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("ai_analysis: failed to parse config: %w", err)
	}
	return cfg, nil
}
