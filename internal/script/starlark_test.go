package script

import (
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
)

func mkBars(closes ...float64) []*domain.Bar {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]*domain.Bar, len(closes))
	for i, c := range closes {
		bars[i] = &domain.Bar{
			Date: base.AddDate(0, 0, i),
			Open: c, High: c, Low: c, Close: c, Volume: 1000,
		}
	}
	return bars
}

func TestCompileErrors(t *testing.T) {
	if _, err := Compile(""); err == nil {
		t.Fatal("empty code should error")
	}
	if _, err := Compile("x = (1"); err == nil {
		t.Fatal("syntax error should be reported")
	}
	if _, err := Compile("y = 1"); err == nil {
		t.Fatal("missing on_bar should error")
	}
	if _, err := Compile("def on_bar(ctx):\n    return None\n"); err != nil {
		t.Fatalf("valid script should compile: %v", err)
	}
}

func TestOnBarBuySell(t *testing.T) {
	code := `
def on_bar(ctx):
    h = ctx.history("close", 2)
    if len(h) < 2:
        return None
    if not ctx.has_position and ctx.close > h[0]:
        return buy("all")
    if ctx.has_position and ctx.pnl_pct() != None and ctx.pnl_pct() < -0.05:
        return sell("all")
    return None
`
	prog, err := Compile(code)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	bars := mkBars(10, 11, 9) // up then down
	sd := &engine.SymbolData{Symbol: "test", Bars: bars}
	pf := engine.NewPortfolio(100000)

	// idx 0: only one bar of history -> no action.
	if sig, _ := prog.OnBar(sd, 0, pf); sig != nil {
		t.Fatalf("idx0 expected nil, got %+v", sig)
	}

	// idx 1: close 11 > prev 10, flat -> buy all.
	sig, err := prog.OnBar(sd, 1, pf)
	if err != nil {
		t.Fatalf("idx1: %v", err)
	}
	if sig == nil || sig.Side != domain.TradeSideBuy || !sig.Size.All {
		t.Fatalf("idx1 expected buy all, got %+v", sig)
	}

	// Simulate the broker opening a position at 11, then check stop-loss at idx2.
	pf.Positions["test"] = &engine.Position{Symbol: "test", Qty: 100, EntryPrice: 11, EntryDate: bars[1].Date}
	sig, err = prog.OnBar(sd, 2, pf) // close 9 vs entry 11 => -18% < -5%
	if err != nil {
		t.Fatalf("idx2: %v", err)
	}
	if sig == nil || sig.Side != domain.TradeSideSell || !sig.Size.All {
		t.Fatalf("idx2 expected sell all, got %+v", sig)
	}
}

func TestSizeKeywords(t *testing.T) {
	prog, err := Compile("def on_bar(ctx):\n    return buy(pct_of_cash=0.5)\n")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	sd := &engine.SymbolData{Symbol: "test", Bars: mkBars(10)}
	sig, err := prog.OnBar(sd, 0, engine.NewPortfolio(100000))
	if err != nil {
		t.Fatalf("onbar: %v", err)
	}
	if sig == nil || sig.Size.PctOfCash != 0.5 {
		t.Fatalf("expected pct_of_cash 0.5, got %+v", sig)
	}
}
