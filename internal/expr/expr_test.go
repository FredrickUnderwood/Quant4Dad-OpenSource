package expr

import (
	"encoding/json"
	"math"
	"testing"
)

func mustParse(t *testing.T, src string) Node {
	t.Helper()
	n, err := Parse(json.RawMessage(src))
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	return n
}

func baseCtx() *EvalContext {
	return &EvalContext{
		Index: 5,
		Indicators: map[string]map[string][]float64{
			"ma5":  {"value": []float64{1, 2, 3, 4, 5, 6}},
			"ma20": {"value": []float64{6, 5, 4, 3, 2, 1}},
			"rsi":  {"value": []float64{10, 20, 30, 40, 50, 65}},
		},
		OHLCV:     map[string]float64{"close": 10, "open": 9, "high": 11, "low": 8, "volume": 100},
		OHLCVPrev: map[string]float64{"close": 9.5, "open": 9.5, "high": 10, "low": 9, "volume": 50},
	}
}

func TestCmpAndLogic(t *testing.T) {
	ctx := baseCtx()
	n := mustParse(t, `{"all":[{"gt":["ma5",4]},{"lt":["ma20","ma5"]}]}`)
	v, err := n.Eval(ctx)
	if err != nil || v.(bool) != true {
		t.Fatalf("want true, got %v err=%v", v, err)
	}
	n2 := mustParse(t, `{"not":{"eq":["close",10]}}`)
	v2, _ := n2.Eval(ctx)
	if v2.(bool) != false {
		t.Fatalf("not eq close 10 want false")
	}
}

func TestCrossUp(t *testing.T) {
	ctx := baseCtx()
	// ma5 prev=5, now=6; ma20 prev=2, now=1 — ma5 already above, but check
	// using a fresh ctx where prev is below.
	ctx2 := &EvalContext{
		Index: 1,
		Indicators: map[string]map[string][]float64{
			"a": {"value": []float64{1, 5}},
			"b": {"value": []float64{3, 3}},
		},
		OHLCV: map[string]float64{"close": 5},
	}
	n := mustParse(t, `{"cross_up":["a","b"]}`)
	v, err := n.Eval(ctx2)
	if err != nil || v.(bool) != true {
		t.Fatalf("cross_up want true got %v err=%v", v, err)
	}
	// no cross at index 0
	ctx2.Index = 0
	v, _ = n.Eval(ctx2)
	if v.(bool) {
		t.Fatalf("cross_up at index 0 must be false")
	}
	_ = ctx
}

func TestCrossDown(t *testing.T) {
	ctx := &EvalContext{
		Index: 1,
		Indicators: map[string]map[string][]float64{
			"a": {"value": []float64{5, 1}},
			"b": {"value": []float64{3, 3}},
		},
		OHLCV: map[string]float64{"close": 1},
	}
	n := mustParse(t, `{"cross_down":["a","b"]}`)
	v, _ := n.Eval(ctx)
	if !v.(bool) {
		t.Fatalf("cross_down want true")
	}
}

func TestContextCalls(t *testing.T) {
	ctx := &EvalContext{
		Index:       0,
		Indicators:  map[string]map[string][]float64{},
		OHLCV:       map[string]float64{"close": 110},
		HasPosition: true,
		EntryPrice:  100,
		DaysHeld:    5,
	}
	for _, src := range []string{
		`{"has_position":null}`,
		`{"gt":[{"pnl_pct":null},0]}`,
		`{"eq":[{"days_held":null},5]}`,
		`{"gt":[{"ratio_from_entry":"close"},1.05]}`,
	} {
		n := mustParse(t, src)
		v, err := n.Eval(ctx)
		if err != nil {
			t.Fatalf("%s: err %v", src, err)
		}
		if v.(bool) != true {
			t.Fatalf("%s: want true got %v", src, v)
		}
	}
}

func TestNaNGuard(t *testing.T) {
	ctx := baseCtx()
	ctx.Indicators["bad"] = map[string][]float64{"value": []float64{math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN(), math.NaN()}}
	n := mustParse(t, `{"gt":["bad",0]}`)
	v, _ := n.Eval(ctx)
	if v.(bool) {
		t.Fatal("NaN compare must be false")
	}
}

func TestCollectRefs(t *testing.T) {
	n := mustParse(t, `{"all":[{"gt":["ma5","ma20"]},{"lt":["rsi.value",70]},{"eq":["close",10]}]}`)
	refs := CollectRefs(n)
	want := map[string]bool{"ma5": true, "ma20": true, "rsi": true}
	if len(refs) != len(want) {
		t.Fatalf("refs %v want %v", refs, want)
	}
	for _, r := range refs {
		if !want[r] {
			t.Fatalf("unexpected ref %s", r)
		}
	}
}
