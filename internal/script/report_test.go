package script

import (
	"context"
	"strings"
	"testing"

	"github.com/quant4dad/internal/engine"
)

const crossEntryScript = `def on_bar(ctx):
    h = ctx.history("close", 4)
    if len(h) < 4:
        return None
    previous = sum(h[:3]) / 3
    current = sum(h[1:]) / 3
    if h[-2] <= previous and h[-1] > current:
        return buy(shares=100)
    return None
`
const roundTripScript = `def on_bar(ctx):
    if ctx.has_position and ctx.days_held >= 1:
        return sell("all")
    if not ctx.has_position:
        return buy(shares=100)
    return None
`

func TestScriptReportMeasuresSmokeWithoutInventingCoverage(t *testing.T) {
	r, err := Inspect(context.Background(), "def on_bar(ctx):\n    if len(ctx.history('close',256)) < 256:\n        return None\n    return buy(shares=100)\n", nil)
	if err != nil || r.Compile != "passed" || r.Smoke.Attempted != 256 || r.Smoke.Passed != 256 || r.Smoke.Signals["none"] != 256 || r.Smoke.Signals["buy"] != 0 || r.BranchCoverageMeasured || r.Behavior.Status != "not_requested" {
		t.Fatalf("unexpected evidence: %+v %v", r, err)
	}
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].Code != "script_no_signal_observed" {
		t.Fatal(r.Diagnostics)
	}
	for _, code := range []string{"def on_bar(ctx)\n    return None", "def on_bar(ctx):\n    return ctx.ma(3)"} {
		r, err = Inspect(context.Background(), code, []string{MA3CrossUpEntry})
		if err == nil || len(r.Diagnostics) != 1 || r.Diagnostics[0].Line < 1 || r.Behavior.Status != "not_run" {
			t.Fatalf("error lacks position/evidence: %+v %v", r, err)
		}
	}
}

func TestMA3EntrySemanticsGeneralizeAcrossPricesAndSizing(t *testing.T) {
	// These independent price transformations preserve crossing semantics and
	// expose implementations that merely recognize the fixed fixture prices.
	for _, scale := range []float64{0.25, 1, 10} {
		for _, offset := range []float64{0, 20, 1000} {
			for _, size := range []string{"shares=200", "pct_of_cash=0.5"} {
				p, err := Compile(strings.Replace(crossEntryScript, "shares=100", size, 1))
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range behaviorCases(MA3CrossUpEntry) {
					closes := make([]float64, len(c.closes))
					for i, price := range c.closes {
						closes[i] = price*scale + offset
					}
					signal, err := p.OnBar(&engine.SymbolData{Symbol: "test", Bars: mkBars(closes...)}, len(closes)-1, engine.NewPortfolio(100000))
					action := "none"
					if signal != nil {
						action = string(signal.Side)
					}
					if err != nil || action != c.expected.Action {
						t.Fatalf("scale=%g offset=%g size=%s case=%s action=%s: %v", scale, offset, size, c.name, action, err)
					}
				}
			}
		}
	}
	contracts := SuiteContracts()
	if contracts[0].ID != MA3CrossUpEntry || contracts[0].Sizing != "" || contracts[0].Exit != "" ||
		contracts[1].ID != DailyRoundTrip100 || contracts[1].Sizing == "" || contracts[1].Exit == "" {
		t.Fatal("suite contract scopes drifted", contracts)
	}
}

func TestScriptBehaviorSuitesDistinguishThresholdFromCrossing(t *testing.T) {
	r, err := Inspect(context.Background(), crossEntryScript, []string{MA3CrossUpEntry})
	if err != nil || r.Behavior.Status != "passed" || len(r.Behavior.Cases) != 6 {
		t.Fatalf("correct entry rejected: %+v %v", r, err)
	}
	threshold := strings.Replace(crossEntryScript, "h[-2] <= previous and h[-1] > current", "h[-1] > current", 1)
	r, err = Inspect(context.Background(), threshold, []string{MA3CrossUpEntry})
	if err == nil || r.Smoke.Status != "passed" || r.Behavior.Status != "failed" {
		t.Fatalf("threshold passed crossing: %+v", r)
	}
	found := false
	for _, c := range r.Behavior.Cases {
		if c.Case == "already_above" && !c.Passed && c.Actual.Action == "buy" && c.Expected.Action == "none" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing concrete counterexample", r.Behavior)
	}
	// The suite does not invent an exit or require 100 shares for an entry-only request.
	if _, err = Inspect(context.Background(), strings.Replace(crossEntryScript, "shares=100", "shares=200", 1), []string{MA3CrossUpEntry}); err != nil {
		t.Fatal(err)
	}
}

func TestScriptBehaviorRoundTripChecksQuantityAndHeldLoss(t *testing.T) {
	r, err := Inspect(context.Background(), roundTripScript, []string{DailyRoundTrip100})
	if err != nil || len(r.Behavior.Cases) != 5 || r.Behavior.Status != "passed" {
		t.Fatal(r, err)
	}
	for _, bad := range []string{
		strings.Replace(roundTripScript, "shares=100", "shares=200", 1),
		strings.Replace(roundTripScript, "ctx.days_held >= 1", "ctx.days_held >= 1 and ctx.pnl_pct() > 0", 1),
		strings.Replace(roundTripScript, "ctx.days_held >= 1", "ctx.days_held >= 0", 1),
	} {
		if _, err := Inspect(context.Background(), bad, []string{DailyRoundTrip100}); err == nil {
			t.Fatal("behavior mismatch admitted", bad)
		}
	}
	for _, suites := range [][]string{{"made_up"}, {MA3CrossUpEntry, MA3CrossUpEntry}} {
		if _, err := Inspect(context.Background(), roundTripScript, suites); err == nil {
			t.Fatal("invalid suite admitted")
		}
	}
}
