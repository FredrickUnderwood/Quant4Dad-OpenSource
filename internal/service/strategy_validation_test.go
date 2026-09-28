package service

import (
	"context"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/indicator"
	"github.com/quant4dad/internal/script"
)

func TestStrategyRejectsNumericLogicalOperandBeforeSave(t *testing.T) {
	svc := NewStrategyService(nil) // Invalid creates must not reach storage.
	in := StrategyInput{Name: "r11-regression", Universe: []string{"sh.600809"}, Period: domain.Bar1d,
		Body: domain.StrategyBody{Rules: []domain.RuleSpec{{Name: "exit", When: []byte(`{"all":[{"has_position":null},{"days_held":null}]}`), Then: domain.ActionSpec{Action: "sell", Size: domain.SizeSpec{All: true}}}}}}
	if err := svc.ValidateAgent(in); err == nil || ValidationIssues(err)[0].Path != "body.rules[0].when" {
		t.Fatal("missing condition type validation", err)
	}
	if _, err := svc.Create(context.Background(), in); err == nil {
		t.Fatal("invalid strategy persisted")
	}
	in.Body.Rules[0].When = []byte(`{"all":[{"has_position":null},{"gte":[{"days_held":null},1]}]}`)
	if err := svc.ValidateAgent(in); err != nil {
		t.Fatal("valid nested helper comparison rejected", err)
	}
}

func TestAgentScriptValidationEnforcedWithoutToolSchema(t *testing.T) {
	svc := NewStrategyService(nil)
	valid := StrategyInput{Name: "script", Universe: []string{"sh.600519"}, Period: domain.Bar1d,
		Body: domain.StrategyBody{Mode: "script", Lang: "starlark", Code: "def on_bar(ctx):\n    return None", Execution: domain.ExecutionSpec{FillAt: "next_open"}}}
	if err := svc.ValidateAgent(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		mutate func(*StrategyInput)
	}{
		{"name", func(in *StrategyInput) { in.Name = "" }},
		{"period", func(in *StrategyInput) { in.Period = "1h" }},
		{"body.lang", func(in *StrategyInput) { in.Body.Lang = "python" }},
		{"body.mode", func(in *StrategyInput) { in.Body.Mode = "unknown" }},
		{"body.execution.fill_at", func(in *StrategyInput) { in.Body.Execution.FillAt = "tomorrow_close" }},
		{"body", func(in *StrategyInput) { in.Body.Mode = "config" }},
		{"body", func(in *StrategyInput) { in.Body.Rules = []domain.RuleSpec{{Name: "unused"}} }},
		{"body.code", func(in *StrategyInput) { in.Body.Code = "" }},
		{"body.code", func(in *StrategyInput) { in.Body.Code = "#" + strings.Repeat("界", script.MaxCodeBytes/2) }},
		{"body.code", func(in *StrategyInput) { in.Body.Code = "def on_bar(ctx):\n    return buy(pct_of_cash=2.0)" }},
	} {
		in := valid
		tc.mutate(&in)
		if err := svc.ValidateAgent(in); err == nil || ValidationIssues(err)[0].Path != tc.path {
			t.Fatalf("expected %s validation failure, got %v", tc.path, err)
		}
	}
}

func TestAgentStrategyValidationExplainsInvalidConditionsAndParameters(t *testing.T) {
	in := StrategyInput{Name: "trend", Universe: []string{"sh.600809"}, Period: domain.Bar1d,
		Body: domain.StrategyBody{Indicators: []domain.IndicatorSpec{{Alias: "fast", Type: "MA", Params: map[string]any{"period": 5}}, {Alias: "slow", Type: "MA", Params: map[string]any{"period": 20}}},
			Rules: []domain.RuleSpec{{Name: "entry", When: []byte(`{"cross_up":["fast","slow"]}`), Then: domain.ActionSpec{Action: "buy", Size: domain.SizeSpec{All: true}}}}, Execution: domain.ExecutionSpec{FillAt: "next_open"}}}
	svc := NewStrategyService(nil)
	if err := svc.ValidateAgent(in); err != nil {
		t.Fatal(err)
	}
	for _, condition := range []string{`{"cross_above":["fast","slow"]}`, `{"left":"fast","op":">","right":"slow"}`, `{}`, `{"gt":["undeclared",0]}`} {
		in.Body.Rules[0].When = []byte(condition)
		err := svc.ValidateAgent(in)
		if err == nil || ValidationIssues(err)[0].Path != "body.rules[0].when" {
			t.Fatalf("missing actionable issue for %s: %v", condition, err)
		}
	}
	in.Body.Rules[0].When = []byte(`{"cross_down":["fast","slow"]}`)
	for _, params := range []map[string]any{{"bogus_param_zzz": 999}, {}, {"period": 0}, {"period": 1.5}, {"period": "20"}, {"period": 20, "source": "bogus"}} {
		in.Body.Indicators[0].Params = params
		err := svc.ValidateAgent(in)
		if err == nil || ValidationIssues(err)[0].Path != "body.indicators[0].params" {
			t.Fatalf("invalid MA params accepted: %v", params)
		}
	}
	for _, definition := range indicator.List() {
		if definition["parameters"] == nil || definition["parameter_schema"] == nil {
			t.Fatal("missing discovery contract")
		}
	}
	// The tool decoder can supply number wrappers; calculators must use the
	// same validated value, rather than silently falling back to zero/defaults.
	in.Body.Indicators[0].Params = map[string]any{"period": 5}
	raw, _ := sonic.Marshal(in)
	var decoded StrategyInput
	if err := (sonic.Config{UseNumber: true}).Froze().Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateAgent(decoded); err != nil {
		t.Fatal(err)
	}
	calc, _ := indicator.Get("MA")
	if _, err := calc.Compute(nil, decoded.Body.Indicators[0].Params); err != nil {
		t.Fatal(err)
	}
}
