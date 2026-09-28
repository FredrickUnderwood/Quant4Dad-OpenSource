package expr

import "testing"

func TestConditionTypesRejectLatentErrorsWithoutEvaluation(t *testing.T) {
	for _, raw := range []string{
		`{"all":[{"has_position":null},{"days_held":null}]}`,
		`{"any":[true,{"pnl_pct":null}]}`,
		`{"not":{"days_held":null}}`,
		`{"days_held":null}`, `"close"`, `1`,
		`{"gt":[{"has_position":null},1]}`,
		`{"eq":[true,1]}`,
		`{"cross_up":[{"has_position":null},1]}`,
		`{"all":[{"any":[{"not":{"pnl_pct":null}}]}]}`,
		`{"all":[]}`, `{"any":[]}`,
		`{"gt":[{"ratio_from_entry":{"days_held":null}},1]}`,
		`{"gt":[{"ratio_from_entry":"unknown"},1]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			node, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal("fixture should parse before type check", err)
			}
			if err := ValidateCondition(node); err == nil {
				t.Fatal("invalid condition accepted")
			}
		})
	}
	for _, raw := range []string{`{"all":[{"days_held":{}}]}`, `{"not":{"has_position":false}}`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("invalid nested helper argument accepted", raw)
		}
	}
}

func TestValidConditionsPreserveBooleanAndNumericSemantics(t *testing.T) {
	for _, raw := range []string{
		`{"all":[{"has_position":null},{"gte":[{"days_held":null},1]}]}`,
		`{"all":[true,{"not":false},{"eq":[{"has_position":null},true]}]}`,
		`{"gte":[{"pnl_pct":null},0.05]}`,
		`{"gte":[{"ratio_from_entry":"close"},1.05]}`,
	} {
		node, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateCondition(node); err != nil {
			t.Fatal(raw, err)
		}
		value, err := node.Eval(&EvalContext{HasPosition: true, DaysHeld: 1, EntryPrice: 100, OHLCV: map[string]float64{"close": 110}})
		if err != nil || value != true {
			t.Fatal(raw, value, err)
		}
	}
	node, _ := Parse([]byte(`{"all":[{"has_position":null},{"gte":[{"days_held":null},1]}]}`))
	for _, ctx := range []*EvalContext{{}, {HasPosition: true, DaysHeld: 0}} {
		if value, err := node.Eval(ctx); err != nil || value != false {
			t.Fatal("numeric helper incorrectly treated as truthiness", value, err)
		}
	}
}
