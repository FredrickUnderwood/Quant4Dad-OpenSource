package engine

import (
	"context"
	"sort"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/expr"
	"github.com/quant4dad/internal/indicator"
)

const (
	FillAtClose    = "close"
	FillAtNextOpen = "next_open"
)

// CompiledRule pairs a parsed AST with its metadata.
type CompiledRule struct {
	Name   string
	When   expr.Node
	Action domain.ActionSpec
}

// Signal is a per-bar order intent produced by a Decider. A nil *Signal means
// "do nothing this bar".
type Signal struct {
	Side domain.TradeSide
	Size domain.SizeSpec
	Rule string // attributed to trade.TriggeredRule
}

// Decider decides what to do for one symbol on one bar. It is the single
// pluggable seam between the backtest framework and a strategy's logic; both
// the config-rule engine (RuleDecider) and script strategies implement it.
type Decider interface {
	OnBar(sd *SymbolData, idx int, pf *Portfolio) (*Signal, error)
}

// RuleDecider runs the config-mode compiled rules: first matching rule wins.
type RuleDecider struct{ Rules []CompiledRule }

func (d RuleDecider) OnBar(sd *SymbolData, idx int, pf *Portfolio) (*Signal, error) {
	ctx := makeCtx(sd, idx, pf, sd.Symbol)
	for _, rule := range d.Rules {
		v, err := rule.When.Eval(ctx)
		if err != nil {
			return nil, err
		}
		b, ok := v.(bool)
		if !ok || !b {
			continue
		}
		return &Signal{
			Side: domain.TradeSide(rule.Action.Action),
			Size: rule.Action.Size,
			Rule: rule.Name,
		}, nil
	}
	return nil, nil
}

// SymbolData holds per-symbol bar+indicator series; bars must be ascending.
type SymbolData struct {
	Symbol     string
	Bars       []*domain.Bar
	Indicators map[string]indicator.Series // alias -> series
}

// RunInput bundles everything an engine invocation needs.
type RunInput struct {
	InitialCapital float64
	Cost           *domain.Cost
	FillAt         string
	Decider        Decider
	Symbols        []*SymbolData
}

// RunOutput is what the engine produces; trades & equity are written verbatim
// to repositories by the caller.
type RunOutput struct {
	Trades []*domain.Trade
	Equity []*domain.EquityPoint
	Result *domain.BacktestResult
}

// Run executes the backtest. Single-threaded per job for determinism.
func Run(in RunInput) (*RunOutput, error) {
	return RunContext(context.Background(), in)
}

func RunContext(ctx context.Context, in RunInput) (*RunOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	portfolio := NewPortfolio(in.InitialCapital)
	broker := NewBroker(in.Cost)

	calendar, byDate := buildCalendar(in.Symbols)
	if len(calendar) == 0 {
		return &RunOutput{Result: &domain.BacktestResult{}}, nil
	}

	trades := make([]*domain.Trade, 0, 64)
	equity := make([]*domain.EquityPoint, 0, len(calendar))

	// Pending orders by (symbol -> intent), drained at next bar's open.
	pending := map[string]*pendingOrder{}
	lastPrice := map[string]float64{}

	for _, day := range calendar {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bars := byDate[day]
		// 1) execute pending orders (filled at this bar's open).
		for sym, po := range pending {
			barsToday, ok := bars[sym]
			if !ok {
				continue
			}
			intent := po.intent
			intent.Time = barsToday.Date
			intent.FillPrice = barsToday.Open
			if tr := broker.Execute(portfolio, intent); tr != nil {
				trades = append(trades, tr)
			}
			delete(pending, sym)
		}

		// 2) ask the decider what to do on every symbol that has a bar today.
		for sym, bar := range bars {
			sd := findSymbol(in.Symbols, sym)
			idx := findIndex(sd.Bars, bar.Date)
			if idx < 0 {
				continue
			}
			sig, err := in.Decider.OnBar(sd, idx, portfolio)
			if err != nil {
				return nil, err
			}
			if sig == nil {
				continue
			}
			intent := orderIntent{
				Symbol:   sym,
				Side:     sig.Side,
				Spec:     sig.Size,
				RuleName: sig.Rule,
			}
			if in.FillAt == FillAtClose {
				intent.Time = bar.Date
				intent.FillPrice = bar.Close
				if tr := broker.Execute(portfolio, intent); tr != nil {
					trades = append(trades, tr)
				}
			} else {
				// next_open: queue for next bar (overwrite if a later symbol/day reuses the slot)
				pending[sym] = &pendingOrder{intent: intent}
			}
		}

		// 3) mark-to-market and emit equity point.
		for sym, bar := range bars {
			lastPrice[sym] = bar.Close
		}
		holding := portfolio.HoldingValue(lastPrice)
		total := portfolio.Cash + holding
		equity = append(equity, &domain.EquityPoint{
			Date:         day,
			TotalValue:   total,
			Cash:         portfolio.Cash,
			HoldingValue: holding,
		})
	}

	result := ComputeMetrics(in.InitialCapital, equity, trades)
	return &RunOutput{Trades: trades, Equity: equity, Result: result}, nil
}

type pendingOrder struct {
	intent orderIntent
}

func buildCalendar(symbols []*SymbolData) ([]time.Time, map[time.Time]map[string]*domain.Bar) {
	byDate := map[time.Time]map[string]*domain.Bar{}
	for _, sd := range symbols {
		for _, b := range sd.Bars {
			d := normalizeDate(b.Date)
			if _, ok := byDate[d]; !ok {
				byDate[d] = map[string]*domain.Bar{}
			}
			byDate[d][sd.Symbol] = b
		}
	}
	dates := make([]time.Time, 0, len(byDate))
	for d := range byDate {
		dates = append(dates, d)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	return dates, byDate
}

func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func findSymbol(symbols []*SymbolData, code string) *SymbolData {
	for _, sd := range symbols {
		if sd.Symbol == code {
			return sd
		}
	}
	return nil
}

func findIndex(bars []*domain.Bar, date time.Time) int {
	target := normalizeDate(date)
	for i, b := range bars {
		if normalizeDate(b.Date).Equal(target) {
			return i
		}
	}
	return -1
}

func makeCtx(sd *SymbolData, idx int, portfolio *Portfolio, sym string) *expr.EvalContext {
	bar := sd.Bars[idx]
	indicators := map[string]map[string][]float64{}
	for alias, series := range sd.Indicators {
		indicators[alias] = series.Outputs
	}
	ctx := &expr.EvalContext{
		Index:      idx,
		Indicators: indicators,
		OHLCV: map[string]float64{
			"open": bar.Open, "high": bar.High, "low": bar.Low,
			"close": bar.Close, "volume": bar.Volume,
		},
		Symbol: sym,
	}
	if idx > 0 {
		prev := sd.Bars[idx-1]
		ctx.OHLCVPrev = map[string]float64{
			"open": prev.Open, "high": prev.High, "low": prev.Low,
			"close": prev.Close, "volume": prev.Volume,
		}
	}
	if pos := portfolio.Position(sym); pos != nil && pos.Qty > 0 {
		ctx.HasPosition = true
		ctx.EntryPrice = pos.EntryPrice
		ctx.DaysHeld = int(normalizeDate(bar.Date).Sub(normalizeDate(pos.EntryDate)).Hours() / 24)
	}
	return ctx
}
