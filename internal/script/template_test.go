package script

import (
	"math"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
)

// Regression guard for the MA-cross style scripts the UI ships (editor template
// + resources/sample_strategies/ma20_script.json). Exercises sum(), list slicing,
// helper defs, pnl_pct() and buy/sell over a full series: golden cross -> buy,
// then stop-loss -> sell. Notably catches reliance on builtins outside Starlark's
// universe (sum is injected by predeclared()).
const maCrossScript = `# Dual moving average with a stop loss and take profit (Starlark script strategy)
FAST = 5
SLOW = 20

def sma(values):
    return sum(values) / len(values)

def on_bar(ctx):
    closes = ctx.history("close", SLOW + 1)
    if len(closes) < SLOW + 1:
        return None
    fast_now = sma(closes[-FAST:])
    slow_now = sma(closes[-SLOW:])
    prev = closes[:-1]
    fast_prev = sma(prev[-FAST:])
    slow_prev = sma(prev[-SLOW:])
    golden = fast_prev <= slow_prev and fast_now > slow_now
    death  = fast_prev >= slow_prev and fast_now < slow_now
    if ctx.has_position:
        pnl = ctx.pnl_pct()
        if pnl != None and (pnl <= -0.08 or pnl >= 0.20):
            return sell("all")
        if death:
            return sell("all")
        return None
    if golden:
        return buy(pct_of_cash=0.5)
    return None
`

func TestMACrossTemplateScript(t *testing.T) {
	prog, err := Compile(maCrossScript)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Synthetic price: dip then strong rise (forces a golden cross), then a crash.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var bars []*domain.Bar
	price := 100.0
	for i := 0; i < 70; i++ {
		switch {
		case i < 28:
			price -= 0.5 // long decline (fast<slow) past the 21-bar warmup
		case i < 55:
			price += 1.5 // rally -> golden cross detected after warmup
		default:
			price -= 4.0 // crash -> stop-loss
		}
		p := math.Max(price, 1)
		bars = append(bars, &domain.Bar{Date: base.AddDate(0, 0, i), Open: p, High: p, Low: p, Close: p, Volume: 1000})
	}
	sd := &engine.SymbolData{Symbol: "test", Bars: bars}
	pf := engine.NewPortfolio(100000)

	buys, sells := 0, 0
	for i := range bars {
		sig, err := prog.OnBar(sd, i, pf)
		if err != nil {
			t.Fatalf("OnBar idx %d runtime error: %v", i, err)
		}
		if sig == nil {
			continue
		}
		// Simulate fills so has_position / pnl_pct behave realistically.
		switch sig.Side {
		case domain.TradeSideBuy:
			if !pf.Has("test") {
				pf.Positions["test"] = &engine.Position{Symbol: "test", Qty: 100, EntryPrice: bars[i].Close, EntryDate: bars[i].Date}
				buys++
			}
		case domain.TradeSideSell:
			if pf.Has("test") {
				delete(pf.Positions, "test")
				sells++
			}
		}
	}
	t.Logf("ma-cross template produced %d buys, %d sells", buys, sells)
	if buys == 0 {
		t.Fatalf("expected at least one buy from golden cross")
	}
}
