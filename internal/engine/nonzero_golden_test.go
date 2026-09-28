package engine

import (
	"math"
	"testing"
	"time"

	"github.com/quant4dad/internal/domain"
)

type goldenDecider struct{}

func (goldenDecider) OnBar(_ *SymbolData, index int, _ *Portfolio) (*Signal, error) {
	switch index {
	case 0:
		return &Signal{Side: domain.TradeSideBuy, Size: domain.SizeSpec{Shares: 100}, Rule: "buy"}, nil
	case 2:
		return &Signal{Side: domain.TradeSideSell, Size: domain.SizeSpec{All: true}, Rule: "sell"}, nil
	}
	return nil, nil
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("%s: got %.12f want %.12f", name, got, want)
	}
}

func TestNonzeroNextOpenGoldenFeesEquityAndDrawdown(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	bars := make([]*domain.Bar, 4)
	for i, close := range []float64{10, 12, 9, 11} {
		bars[i] = &domain.Bar{Code: "fixture", Date: start.AddDate(0, 0, i), Open: 10, Close: close}
	}
	bars[3].Open = 11
	out, err := Run(RunInput{InitialCapital: 10000, FillAt: FillAtNextOpen, Decider: goldenDecider{},
		Cost:    &domain.Cost{CommissionRate: 0.001, MinCommission: 1, StampDutyRate: 0.002, SlippageBps: 100},
		Symbols: []*SymbolData{{Symbol: "fixture", Bars: bars}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Trades) != 2 || len(out.Equity) != 4 {
		t.Fatalf("want a completed round trip and four equity points; trades=%d equity=%d", len(out.Trades), len(out.Equity))
	}
	buy, sell := out.Trades[0], out.Trades[1]
	if buy.Qty != 100 || sell.Qty != 100 || !buy.Time.Equal(bars[1].Date) || !sell.Time.Equal(bars[3].Date) {
		t.Fatalf("wrong share count or next-open execution: buy=%+v sell=%+v", buy, sell)
	}
	// Independent hand ledger: buy 100 * 10.10 + 1.01; sell
	// 100 * 10.89 - 1.089 - 2.178. Net cash gain is 74.723.
	near(t, "buy price", buy.Price, 10.1)
	near(t, "sell price", sell.Price, 10.89)
	near(t, "buy commission", buy.Commission, 1.01)
	near(t, "sell commission", sell.Commission, 1.089)
	near(t, "buy stamp duty", buy.StampDuty, 0)
	near(t, "sell stamp duty", sell.StampDuty, 2.178)
	near(t, "round-trip realized pnl", sell.RealizedPnL, 74.723)
	for i, value := range []float64{10000, 10188.99, 9888.99, 10074.723} {
		near(t, "equity", out.Equity[i].TotalValue, value)
	}
	near(t, "final cash", out.Equity[3].Cash, 10074.723)
	near(t, "final holdings", out.Equity[3].HoldingValue, 0)
	near(t, "return", out.Result.TotalReturn, 0.0074723)
	near(t, "max drawdown", out.Result.MaxDrawdown, 300.0/10188.99)
	near(t, "win rate", out.Result.WinRate, 1)
}

func TestBuyCommissionAllocatedAcrossPartialSells(t *testing.T) {
	p := NewPortfolio(10000)
	b := NewBroker(&domain.Cost{MinCommission: 5})
	buy := b.Execute(p, orderIntent{Symbol: "fixture", Side: domain.TradeSideBuy, Spec: domain.SizeSpec{Shares: 200}, FillPrice: 10})
	if buy == nil || buy.Qty != 200 {
		t.Fatalf("explicit share count was not filled: %+v", buy)
	}
	first := b.Execute(p, orderIntent{Symbol: "fixture", Side: domain.TradeSideSell, Spec: domain.SizeSpec{Shares: 100}, FillPrice: 10.06})
	last := b.Execute(p, orderIntent{Symbol: "fixture", Side: domain.TradeSideSell, Spec: domain.SizeSpec{All: true}, FillPrice: 10.06})
	if first == nil || last == nil {
		t.Fatal("partial liquidation missing")
	}
	near(t, "first net pnl", first.RealizedPnL, -1.5)
	near(t, "last net pnl", last.RealizedPnL, -1.5)
	near(t, "cash reconciles to realized pnl", p.Cash-10000, first.RealizedPnL+last.RealizedPnL)
	near(t, "fee-adjusted win rate", ComputeMetrics(10000, []*domain.EquityPoint{{TotalValue: p.Cash}}, []*domain.Trade{buy, first, last}).WinRate, 0)
}

func TestMinimumCommissionDoesNotExceedRequestedBuyBudget(t *testing.T) {
	p := NewPortfolio(10000)
	trade := NewBroker(&domain.Cost{MinCommission: 5}).Execute(p, orderIntent{Symbol: "fixture", Side: domain.TradeSideBuy,
		Spec: domain.SizeSpec{FixedCash: 1000}, FillPrice: 10})
	if trade != nil {
		t.Fatalf("100 shares plus fees exceed the 1000 budget: %+v", trade)
	}
}
