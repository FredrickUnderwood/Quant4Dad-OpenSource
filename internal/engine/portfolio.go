package engine

import "time"

// Position tracks one open position per symbol. v1 supports long-only and
// flattens (single open lot) for clarity — re-entering after a sell starts a
// fresh lot.
type Position struct {
	Symbol     string
	Qty        int
	EntryPrice float64
	EntryFees  float64 // unallocated buy commission, released pro rata on sells
	EntryDate  time.Time
}

type Portfolio struct {
	Cash      float64
	Positions map[string]*Position
}

func NewPortfolio(cash float64) *Portfolio {
	return &Portfolio{Cash: cash, Positions: map[string]*Position{}}
}

func (p *Portfolio) Has(symbol string) bool {
	pos, ok := p.Positions[symbol]
	return ok && pos != nil && pos.Qty > 0
}

func (p *Portfolio) Position(symbol string) *Position {
	return p.Positions[symbol]
}

// HoldingValue marks all positions to the provided per-symbol close prices.
// Symbols missing from prices are skipped (treated as last-known by caller).
func (p *Portfolio) HoldingValue(prices map[string]float64) float64 {
	total := 0.0
	for sym, pos := range p.Positions {
		if pos == nil || pos.Qty == 0 {
			continue
		}
		if px, ok := prices[sym]; ok {
			total += float64(pos.Qty) * px
		}
	}
	return total
}
