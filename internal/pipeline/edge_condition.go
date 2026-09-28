package pipeline

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Comparison operators available to an edge condition.
const (
	condEq       = "eq"       // equal
	condNe       = "ne"       // not equal
	condContains = "contains" // substring of a string, or membership in an array
	condGt       = "gt"       // numerically greater than
	condLt       = "lt"       // numerically less than
	condExists   = "exists"   // the field is present and not null
)

// EdgeCondition is the routing condition attached to an edge: it makes one judgement
// about one field of the upstream node's output payload. True activates the edge and the
// message flows downstream; false leaves it inactive, which may prune what is downstream.
// This is a deliberately minimal single-field predicate, meant for non-technical users
// configuring it on the canvas; complex logic belongs in several filter nodes in series
// rather than in a piled-up expression. A nil condition means unconditional — always
// active.
type EdgeCondition struct {
	Field string `json:"field"`           // the payload field name
	Op    string `json:"op"`              // eq / ne / contains / gt / lt / exists
	Value any    `json:"value,omitempty"` // the value to compare against; ignored for exists
}

// ParseCondition parses an edge condition. An empty raw returns (nil, nil), meaning
// unconditional. It checks that the field name is non-empty, the operator is valid, and
// the comparison value for a numeric operator converts to a number — so a pipeline can be
// validated up front when saved.
func ParseCondition(raw json.RawMessage) (*EdgeCondition, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var c EdgeCondition
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("edge condition: parse failed: %w", err)
	}
	if strings.TrimSpace(c.Field) == "" {
		return nil, fmt.Errorf("edge condition: field must not be empty")
	}
	switch c.Op {
	case condEq, condNe, condContains, condExists:
	case condGt, condLt:
		if _, ok := toFloatVal(c.Value); !ok {
			return nil, fmt.Errorf("edge condition: the comparison value for operator %q must be a number", c.Op)
		}
	default:
		return nil, fmt.Errorf("edge condition: unsupported operator %q (expect eq/ne/contains/gt/lt/exists)", c.Op)
	}
	return &c, nil
}

// Eval evaluates the condition against a payload. Any type-mismatched comparison returns
// false — conservatively inactive — so bad data cannot open a branch by accident.
func (c *EdgeCondition) Eval(payload map[string]any) bool {
	fv, ok := payload[c.Field]
	switch c.Op {
	case condExists:
		return ok && fv != nil
	case condEq:
		return ok && scalarEqual(fv, c.Value)
	case condNe:
		return !(ok && scalarEqual(fv, c.Value))
	case condContains:
		return ok && containsValue(fv, c.Value)
	case condGt, condLt:
		lf, lok := toFloatVal(fv)
		rf, rok := toFloatVal(c.Value)
		if !lok || !rok {
			return false
		}
		if c.Op == condGt {
			return lf > rf
		}
		return lf < rf
	default:
		return false
	}
}

// scalarEqual compares two values at the scalar level: numbers compare as floats
// (accepting int, float64 and json.Number), and anything else falls back to string
// comparison, which covers bool, string and the mixed types the UI submits.
func scalarEqual(a, b any) bool {
	if af, aok := toFloatVal(a); aok {
		if bf, bok := toFloatVal(b); bok {
			return af == bf
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// containsValue supports two kinds of "contains": a substring check when the field is a
// string, and a membership check when the field is an array.
func containsValue(field, want any) bool {
	if s, ok := field.(string); ok {
		return strings.Contains(s, fmt.Sprintf("%v", want))
	}
	if arr, ok := field.([]any); ok {
		for _, it := range arr {
			if scalarEqual(it, want) {
				return true
			}
		}
	}
	return false
}

// toFloatVal converts any value to a float64 on a best-effort basis. JSON decoding yields
// float64 for every number; this additionally accepts the int types, json.Number, and
// numeric strings.
func toFloatVal(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		// The UI form submits every comparison value as a string (e.g. "5" for gt/lt), so
		// parse it to a number as needed. This also lets a "numeric string vs number"
		// equality check take the numeric path.
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
