package engine

import (
	"math"
	"time"

	"github.com/quant4dad/internal/domain"
)

// lotSize is the standard A-share lot: 1 lot = 100 shares.
const lotSize = 100

// Broker performs simulated fills with slippage and fees.
type Broker struct {
	cost *domain.Cost
}

func NewBroker(cost *domain.Cost) *Broker {
	return &Broker{cost: cost}
}

// orderIntent is generated from a rule hit; broker turns it into a Trade and
// mutates the portfolio. Returns nil when the order can't be filled (e.g.
// insufficient cash for one lot).
type orderIntent struct {
	Symbol    string
	Side      domain.TradeSide
	Spec      domain.SizeSpec
	RuleName  string
	Time      time.Time
	FillPrice float64
}

func (b *Broker) Execute(p *Portfolio, intent orderIntent) *domain.Trade {
	price := b.applySlippage(intent.FillPrice, intent.Side)
	if price <= 0 {
		return nil
	}

	switch intent.Side {
	case domain.TradeSideBuy:
		return b.buy(p, intent, price)
	case domain.TradeSideSell:
		return b.sell(p, intent, price)
	}
	return nil
}

func (b *Broker) buy(p *Portfolio, intent orderIntent, price float64) *domain.Trade {
	if p.Has(intent.Symbol) {
		// v1 single-lot rule: ignore buy when already holding.
		return nil
	}
	cashFor := b.cashBudget(p, intent.Spec)
	if cashFor <= 0 {
		return nil
	}
	// Estimate qty assuming commission ~ rate*notional; round to lot then verify.
	rate := b.cost.CommissionRate
	estPrice := price * (1 + rate)
	qty := int(math.Floor(cashFor/estPrice/lotSize)) * lotSize
	if intent.Spec.Shares > 0 {
		qty = min(qty, intent.Spec.Shares/lotSize*lotSize)
	}
	for qty > 0 {
		notional := float64(qty) * price
		commission := math.Max(notional*b.cost.CommissionRate, b.cost.MinCommission)
		total := notional + commission
		if total <= cashFor && total <= p.Cash {
			p.Cash -= total
			p.Positions[intent.Symbol] = &Position{
				Symbol:     intent.Symbol,
				Qty:        qty,
				EntryPrice: price,
				EntryFees:  commission,
				EntryDate:  intent.Time,
			}
			return &domain.Trade{
				Code:          intent.Symbol,
				Side:          domain.TradeSideBuy,
				Qty:           qty,
				Price:         price,
				Notional:      notional,
				Commission:    commission,
				Time:          intent.Time,
				TriggeredRule: intent.RuleName,
			}
		}
		qty -= lotSize
	}
	return nil
}

func (b *Broker) sell(p *Portfolio, intent orderIntent, price float64) *domain.Trade {
	pos := p.Position(intent.Symbol)
	if pos == nil || pos.Qty == 0 {
		return nil
	}
	// sell + pct_of_cash is an invalid combination: pct_of_cash is buy-side semantics, and a
	// sell should use pct_of_position.
	if !intent.Spec.All && intent.Spec.PctOfCash > 0 {
		return nil
	}
	qty := b.sellQty(pos.Qty, price, intent.Spec)
	if qty <= 0 {
		return nil
	}
	if qty > pos.Qty {
		qty = pos.Qty
	}
	notional := float64(qty) * price
	commission := math.Max(notional*b.cost.CommissionRate, b.cost.MinCommission)
	stampDuty := notional * b.cost.StampDutyRate
	proceeds := notional - commission - stampDuty
	entryFees := pos.EntryFees * float64(qty) / float64(pos.Qty)
	realized := (price-pos.EntryPrice)*float64(qty) - commission - stampDuty - entryFees

	p.Cash += proceeds
	if qty >= pos.Qty {
		delete(p.Positions, intent.Symbol)
	} else {
		pos.Qty -= qty
		pos.EntryFees -= entryFees
	}
	return &domain.Trade{
		Code:          intent.Symbol,
		Side:          domain.TradeSideSell,
		Qty:           qty,
		Price:         price,
		Notional:      notional,
		Commission:    commission,
		StampDuty:     stampDuty,
		Time:          intent.Time,
		TriggeredRule: intent.RuleName,
		RealizedPnL:   realized,
	}
}

// sellQty computes how many shares to sell from the SizeSpec, rounded down to whole 100-share
// lots. A 0 means the calculation does not amount to a whole lot, and the caller should skip
// the trade.
func (b *Broker) sellQty(held int, price float64, spec domain.SizeSpec) int {
	if spec.All {
		return held
	}
	switch {
	case spec.PctOfPosition > 0:
		pct := spec.PctOfPosition
		if pct >= 1 {
			return held
		}
		return (int(float64(held)*pct) / lotSize) * lotSize
	case spec.FixedCash > 0 && price > 0:
		return int(spec.FixedCash/price/lotSize) * lotSize
	case spec.Shares > 0:
		if spec.Shares >= held {
			return held
		}
		return (spec.Shares / lotSize) * lotSize
	}
	// No valid size description at all: liquidate the position, preserving the earlier behavior.
	return held
}

func (b *Broker) cashBudget(p *Portfolio, spec domain.SizeSpec) float64 {
	if spec.All {
		return p.Cash
	}
	if spec.FixedCash > 0 {
		return math.Min(spec.FixedCash, p.Cash)
	}
	if spec.PctOfCash > 0 {
		return p.Cash * spec.PctOfCash
	}
	if spec.Shares > 0 {
		// Bound the estimate by available cash; Execute applies the explicit
		// share limit. MaxFloat64 here overflows the integer quantity.
		return p.Cash
	}
	// default: half of available cash so a buy doesn't accidentally go all-in.
	return p.Cash * 0.5
}

func (b *Broker) applySlippage(price float64, side domain.TradeSide) float64 {
	bps := b.cost.SlippageBps / 10000.0
	if side == domain.TradeSideBuy {
		return price * (1 + bps)
	}
	return price * (1 - bps)
}
