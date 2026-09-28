package indicator

import (
	"errors"
	"math"

	"github.com/quant4dad/internal/domain"
)

type rsiCalc struct{}

func (rsiCalc) Name() string          { return "RSI" }
func (rsiCalc) OutputNames() []string { return []string{"value"} }

// Wilder's RSI.
func (rsiCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((rsiCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	period := intParam(params, "period", 14)
	if period <= 0 {
		return Series{}, errors.New("RSI: period must be > 0")
	}
	src := source(bars, stringParam(params, "source", "close"))
	out := nanSlice(len(src))
	if len(src) <= period {
		return Series{Outputs: map[string][]float64{"value": out}}, nil
	}
	var gainSum, lossSum float64
	for i := 1; i <= period; i++ {
		ch := src[i] - src[i-1]
		if ch >= 0 {
			gainSum += ch
		} else {
			lossSum -= ch
		}
	}
	avgGain := gainSum / float64(period)
	avgLoss := lossSum / float64(period)
	out[period] = rsiFromAverages(avgGain, avgLoss)
	for i := period + 1; i < len(src); i++ {
		ch := src[i] - src[i-1]
		gain, loss := 0.0, 0.0
		if ch >= 0 {
			gain = ch
		} else {
			loss = -ch
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		out[i] = rsiFromAverages(avgGain, avgLoss)
	}
	return Series{Outputs: map[string][]float64{"value": out}}, nil
}

func rsiFromAverages(g, l float64) float64 {
	if l == 0 {
		if g == 0 {
			return 50
		}
		return 100
	}
	rs := g / l
	return 100 - 100/(1+rs)
}

var _ = math.NaN
