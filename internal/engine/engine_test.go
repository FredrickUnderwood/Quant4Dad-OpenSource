package engine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/expr"
	"github.com/quant4dad/internal/indicator"
)

func TestEngineGoldenCrossSimple(t *testing.T) {
	// Build a 30-bar series that ramps up steadily so MA5 crosses above MA20.
	closes := []float64{
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		10, 10, 10, 10, 10, 10, 10, 10, 10, 10,
		11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
	}
	bars := make([]*domain.Bar, len(closes))
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range closes {
		bars[i] = &domain.Bar{
			Code:   "test",
			Period: domain.Bar1d,
			Date:   start.AddDate(0, 0, i),
			Open:   c, High: c + 0.5, Low: c - 0.5, Close: c, Volume: 1000,
		}
	}
	ma, _ := indicator.Get("MA")
	ma5, _ := ma.Compute(bars, map[string]any{"period": 5})
	ma20, _ := ma.Compute(bars, map[string]any{"period": 20})

	// Rules: buy on golden cross, sell on death cross.
	parseRule := func(name, when, action string) CompiledRule {
		n, err := expr.Parse(json.RawMessage(when))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		var a domain.ActionSpec
		if err := json.Unmarshal([]byte(action), &a); err != nil {
			t.Fatalf("action: %v", err)
		}
		return CompiledRule{Name: name, When: n, Action: a}
	}
	rules := []CompiledRule{
		parseRule("golden", `{"cross_up":["ma5","ma20"]}`, `{"action":"buy","size":{"pct_of_cash":1.0}}`),
		parseRule("death", `{"cross_down":["ma5","ma20"]}`, `{"action":"sell","size":"all"}`),
	}

	out, err := Run(RunInput{
		InitialCapital: 100000,
		Cost:           domain.DefaultAShareCost(),
		FillAt:         FillAtClose,
		Decider:        RuleDecider{Rules: rules},
		Symbols: []*SymbolData{
			{
				Symbol: "test",
				Bars:   bars,
				Indicators: map[string]indicator.Series{
					"ma5":  ma5,
					"ma20": ma20,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Trades) == 0 {
		t.Fatal("expected at least one buy trade on the upswing")
	}
	if out.Trades[0].Side != domain.TradeSideBuy {
		t.Fatalf("first trade side want buy got %s", out.Trades[0].Side)
	}
	if out.Trades[0].Qty <= 0 || out.Trades[0].Qty%100 != 0 {
		t.Fatalf("trade qty must be positive whole lots, got %d", out.Trades[0].Qty)
	}
	if len(out.Equity) != len(bars) {
		t.Fatalf("equity len %d want %d", len(out.Equity), len(bars))
	}
	if out.Result.TotalReturn <= 0 {
		t.Fatalf("buying a rising market should be profitable, got %v", out.Result.TotalReturn)
	}
}
