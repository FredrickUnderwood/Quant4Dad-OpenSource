package indicator

import (
	"errors"
	"math"

	"github.com/quant4dad/internal/domain"
)

type atrCalc struct{}

func (atrCalc) Name() string          { return "ATR" }
func (atrCalc) OutputNames() []string { return []string{"value"} }

// Wilder's ATR.
func (atrCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((atrCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	period := intParam(params, "period", 14)
	if period <= 0 {
		return Series{}, errors.New("ATR: period must be > 0")
	}
	tr := make([]float64, len(bars))
	for i := range bars {
		if i == 0 {
			tr[i] = bars[i].High - bars[i].Low
			continue
		}
		hl := bars[i].High - bars[i].Low
		hc := math.Abs(bars[i].High - bars[i-1].Close)
		lc := math.Abs(bars[i].Low - bars[i-1].Close)
		tr[i] = math.Max(hl, math.Max(hc, lc))
	}
	out := nanSlice(len(bars))
	if period > len(bars) {
		return Series{Outputs: map[string][]float64{"value": out}}, nil
	}
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += tr[i]
	}
	out[period-1] = sum / float64(period)
	for i := period; i < len(bars); i++ {
		out[i] = (out[i-1]*float64(period-1) + tr[i]) / float64(period)
	}
	return Series{Outputs: map[string][]float64{"value": out}}, nil
}
