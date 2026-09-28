package engine

import (
	"math"

	"github.com/quant4dad/internal/domain"
)

// Compute portfolio metrics from the equity curve + trade list.
// Annualization assumes ~252 A-share trading days/year for daily bars.
func ComputeMetrics(initial float64, equity []*domain.EquityPoint, trades []*domain.Trade) *domain.BacktestResult {
	res := &domain.BacktestResult{}
	if len(equity) == 0 || initial <= 0 {
		return res
	}
	final := equity[len(equity)-1].TotalValue
	res.TotalReturn = (final - initial) / initial

	// Annualized return based on bar count (~252 trading days).
	years := float64(len(equity)) / 252.0
	if years > 0 && final > 0 {
		res.AnnualizedReturn = math.Pow(final/initial, 1.0/years) - 1
	}

	// Max drawdown — track running peak.
	peak := initial
	maxDD := 0.0
	for _, p := range equity {
		if p.TotalValue > peak {
			peak = p.TotalValue
		}
		dd := (peak - p.TotalValue) / peak
		if dd > maxDD {
			maxDD = dd
		}
		p.Drawdown = dd
	}
	res.MaxDrawdown = maxDD

	// Sharpe ratio: daily returns, rf=0, annualized by sqrt(252).
	if len(equity) > 1 {
		rets := make([]float64, 0, len(equity)-1)
		for i := 1; i < len(equity); i++ {
			prev := equity[i-1].TotalValue
			if prev == 0 {
				continue
			}
			rets = append(rets, (equity[i].TotalValue-prev)/prev)
		}
		if len(rets) > 0 {
			mean := 0.0
			for _, r := range rets {
				mean += r
			}
			mean /= float64(len(rets))
			variance := 0.0
			for _, r := range rets {
				d := r - mean
				variance += d * d
			}
			variance /= float64(len(rets))
			std := math.Sqrt(variance)
			if std > 0 {
				res.Sharpe = mean / std * math.Sqrt(252)
			}
		}
	}

	// Win rate / trade count from realized PnL on sell trades.
	wins, total := 0, 0
	for _, t := range trades {
		if t.Side != domain.TradeSideSell {
			continue
		}
		total++
		if t.RealizedPnL > 0 {
			wins++
		}
	}
	res.TradeCount = len(trades)
	if total > 0 {
		res.WinRate = float64(wins) / float64(total)
	}
	return res
}
