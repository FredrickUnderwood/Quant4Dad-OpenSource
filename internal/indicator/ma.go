package indicator

import (
	"errors"
	"math"

	"github.com/quant4dad/internal/domain"
)

type maCalc struct{}

func (maCalc) Name() string          { return "MA" }
func (maCalc) OutputNames() []string { return []string{"value"} }

func (maCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((maCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	period := intParam(params, "period", 0)
	if period <= 0 {
		return Series{}, errors.New("MA: period must be > 0")
	}
	src := source(bars, stringParam(params, "source", "close"))
	out := nanSlice(len(src))
	if period <= len(src) {
		sum := 0.0
		for i, v := range src {
			sum += v
			if i >= period {
				sum -= src[i-period]
			}
			if i >= period-1 {
				out[i] = sum / float64(period)
			}
		}
	}
	return Series{Outputs: map[string][]float64{"value": out}}, nil
}

// ema computes the canonical exponential moving average with smoothing 2/(p+1).
// The first defined value is the simple average of the first p points so the
// indicator stays stable even on short series.
func ema(values []float64, period int) []float64 {
	out := nanSlice(len(values))
	if period <= 0 || period > len(values) {
		return out
	}
	alpha := 2.0 / float64(period+1)
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	out[period-1] = sum / float64(period)
	for i := period; i < len(values); i++ {
		out[i] = alpha*values[i] + (1-alpha)*out[i-1]
	}
	return out
}

type emaCalc struct{}

func (emaCalc) Name() string          { return "EMA" }
func (emaCalc) OutputNames() []string { return []string{"value"} }

func (emaCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((emaCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	period := intParam(params, "period", 0)
	if period <= 0 {
		return Series{}, errors.New("EMA: period must be > 0")
	}
	src := source(bars, stringParam(params, "source", "close"))
	return Series{Outputs: map[string][]float64{"value": ema(src, period)}}, nil
}

// _ keeps math imported when indicator-specific math is added later.
var _ = math.NaN
