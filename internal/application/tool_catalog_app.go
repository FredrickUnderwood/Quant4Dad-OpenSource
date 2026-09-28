package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/strictjson"
)

const ToolMaxResultBytes = 128 << 10

var ErrToolForbidden = errors.New("tool_forbidden")
var ErrToolResultTooLarge = errors.New("tool_result_too_large")
var ErrToolTimeout = errors.New("tool_timeout")

// Definition is shared policy and business adapter metadata. The external MCP
// fixes profile=external; an internal Gateway must independently verify authority.
type ToolDefinition struct {
	Name         string                                        `json:"name"`
	Description  string                                        `json:"description"`
	InputSchema  map[string]any                                `json:"inputSchema"`
	OutputSchema map[string]any                                `json:"outputSchema"`
	Risk         string                                        `json:"risk"`
	Profiles     []string                                      `json:"profiles"`
	TimeoutMS    int                                           `json:"timeout_ms"`
	MaxResult    int                                           `json:"max_result_bytes"`
	Deprecated   bool                                          `json:"deprecated,omitempty"`
	Handler      func(context.Context, []byte) (any, error)    `json:"-"`
	ResolveRisk  func(context.Context, []byte) (string, error) `json:"-"`
}
type ToolCatalogApplication struct{ tools map[string]ToolDefinition }

func NewToolCatalogApplication() *ToolCatalogApplication {
	return &ToolCatalogApplication{tools: map[string]ToolDefinition{}}
}
func (a *ToolCatalogApplication) Register(t ToolDefinition) {
	if t.Name == "" || t.Handler == nil || !slices.Contains([]string{"R0", "R1", "R2", "R3"}, t.Risk) || ((t.Risk == "R2" || t.Risk == "R3") && slices.Contains(t.Profiles, "external")) || len(t.Profiles) == 0 || t.TimeoutMS < 1 || t.TimeoutMS > 30000 || t.MaxResult < 1 || t.MaxResult > ToolMaxResultBytes || t.InputSchema == nil || t.OutputSchema == nil {
		panic("tool catalog definition invalid")
	}
	if _, ok := a.tools[t.Name]; ok {
		panic("tool catalog duplicate name")
	}
	copy := cloneDefinition(t)
	copy.Handler = t.Handler
	copy.ResolveRisk = t.ResolveRisk
	a.tools[t.Name] = copy
}
func cloneDefinition(t ToolDefinition) ToolDefinition {
	raw, err := sonic.Marshal(t)
	if err != nil {
		panic("tool catalog serialization invalid")
	}
	var copy ToolDefinition
	if sonic.Unmarshal(raw, &copy) != nil {
		panic("tool catalog serialization invalid")
	}
	return copy
}
func (a *ToolCatalogApplication) Definitions(profile string) []ToolDefinition {
	out := make([]ToolDefinition, 0)
	for _, t := range a.tools {
		if slices.Contains(t.Profiles, profile) && (!t.Deprecated || profile == "external") {
			out = append(out, cloneDefinition(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
func (a *ToolCatalogApplication) Revision(profile string) string {
	data, err := sonic.Config{SortMapKeys: true}.Froze().Marshal(a.Definitions(profile))
	if err != nil {
		panic("tool catalog serialization invalid")
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (a *ToolCatalogApplication) Invoke(ctx context.Context, profile, name string, args []byte) ([]byte, error) {
	t, ok := a.tools[name]
	if !ok || !slices.Contains(t.Profiles, profile) {
		return nil, ErrToolForbidden
	}
	if t.Risk == "R2" || t.Risk == "R3" {
		if _, ok := service.AgentExecutionFromContext(ctx); !ok {
			return nil, ErrToolForbidden
		}
	}
	if len(args) > 64<<10 {
		return nil, service.ErrToolInput
	}
	if len(args) == 0 {
		args = []byte("{}")
	}
	var err error
	args, err = a.ValidateInput(profile, name, args)
	if err != nil {
		return nil, err
	}
	var arguments map[string]any
	if strictjson.DecodeNullable(args, &arguments, 64<<10) != nil {
		return nil, service.ErrToolInput
	}
	for _, key := range []string{"actor_id", "session_id", "run_id", "profile", "tool_call_id", "idempotency_key", "arguments_hash", "approval_receipt", "run_capability", "runtime_token", "_meta"} {
		if _, exists := arguments[key]; exists {
			return nil, service.ErrToolInput
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.TimeoutMS)*time.Millisecond)
	defer cancel()
	value, err := t.Handler(ctx, args)
	if ctx.Err() != nil {
		return nil, ErrToolTimeout
	}
	if err != nil {
		return nil, err
	}
	data, err := sonic.Marshal(map[string]any{"data": value, "untrusted_data": true})
	if err != nil {
		return nil, service.ErrToolUnavailable
	}
	if len(data) > t.MaxResult {
		return nil, ErrToolResultTooLarge
	}
	return data, nil
}

func (a *ToolCatalogApplication) Risk(ctx context.Context, profile, name string, args []byte) (string, error) {
	t, ok := a.tools[name]
	if !ok || !slices.Contains(t.Profiles, profile) {
		return "", ErrToolForbidden
	}
	if t.ResolveRisk == nil {
		return t.Risk, nil
	}
	risk, err := t.ResolveRisk(ctx, args)
	if err != nil {
		return "", err
	}
	if (t.Risk == "R2" && (risk == "R2" || risk == "R3")) || risk == t.Risk {
		return risk, nil
	}
	return "", ErrToolForbidden
}

func readTool(name, description string, input, output map[string]any, profiles []string, fn func(context.Context, []byte) (any, error)) ToolDefinition {
	return ToolDefinition{Name: name, Description: description, InputSchema: input, OutputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"data", "untrusted_data"}, "properties": map[string]any{"data": output, "untrusted_data": map[string]any{"type": "boolean", "const": true}}}, Risk: "R0", Profiles: profiles, TimeoutMS: 5000, MaxResult: ToolMaxResultBytes, Handler: fn}
}
