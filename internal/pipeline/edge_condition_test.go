package pipeline

import (
	"encoding/json"
	"testing"
)

func TestParseCondition(t *testing.T) {
	if c, err := ParseCondition(nil); c != nil || err != nil {
		t.Errorf("nil raw should be (nil,nil), got (%v,%v)", c, err)
	}
	if c, err := ParseCondition(json.RawMessage(`null`)); c != nil || err != nil {
		t.Errorf("null should be (nil,nil), got (%v,%v)", c, err)
	}
	if _, err := ParseCondition(json.RawMessage(`{"op":"eq","value":1}`)); err == nil {
		t.Error("missing field should error")
	}
	if _, err := ParseCondition(json.RawMessage(`{"field":"x","op":"bogus"}`)); err == nil {
		t.Error("bad op should error")
	}
	if _, err := ParseCondition(json.RawMessage(`{"field":"x","op":"gt","value":"nope"}`)); err == nil {
		t.Error("gt with non-numeric value should error")
	}
}

func TestEdgeConditionEval(t *testing.T) {
	tests := []struct {
		name    string
		cond    string
		payload map[string]any
		want    bool
	}{
		{"eq string hit", `{"field":"s","op":"eq","value":"pos"}`, map[string]any{"s": "pos"}, true},
		{"eq string miss", `{"field":"s","op":"eq","value":"pos"}`, map[string]any{"s": "neg"}, false},
		{"eq number (json float)", `{"field":"n","op":"eq","value":3}`, map[string]any{"n": float64(3)}, true},
		{"ne hit", `{"field":"s","op":"ne","value":"pos"}`, map[string]any{"s": "neg"}, true},
		{"ne on missing field is true", `{"field":"s","op":"ne","value":"pos"}`, map[string]any{}, true},
		{"gt hit", `{"field":"n","op":"gt","value":5}`, map[string]any{"n": float64(9)}, true},
		{"gt miss", `{"field":"n","op":"gt","value":5}`, map[string]any{"n": float64(1)}, false},
		{"lt hit", `{"field":"n","op":"lt","value":5}`, map[string]any{"n": float64(1)}, true},
		{"contains substring", `{"field":"t","op":"contains","value":"AI"}`, map[string]any{"t": "用 AI 分析"}, true},
		{"contains array element", `{"field":"tags","op":"contains","value":"a"}`, map[string]any{"tags": []any{"a", "b"}}, true},
		{"exists hit", `{"field":"k","op":"exists"}`, map[string]any{"k": 0}, true},
		{"exists miss", `{"field":"k","op":"exists"}`, map[string]any{}, false},
		{"exists nil is false", `{"field":"k","op":"exists"}`, map[string]any{"k": nil}, false},
		{"gt on missing field false", `{"field":"n","op":"gt","value":5}`, map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseCondition(json.RawMessage(tt.cond))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := c.Eval(tt.payload); got != tt.want {
				t.Errorf("Eval = %v, want %v", got, tt.want)
			}
		})
	}
}
