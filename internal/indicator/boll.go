package indicator

import (
	"errors"
	"math"

	"github.com/quant4dad/internal/domain"
)

type bollCalc struct{}

func (bollCalc) Name() string          { return "BOLL" }
func (bollCalc) OutputNames() []string { return []string{"upper", "mid", "lower"} }

func (bollCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((bollCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	period := intParam(params, "period", 20)
	mult := floatParam(params, "mult", 2.0)
	if period <= 0 {
		return Series{}, errors.New("BOLL: period must be > 0")
	}
	src := source(bars, stringParam(params, "source", "close"))
	mid := nanSlice(len(src))
	upper := nanSlice(len(src))
	lower := nanSlice(len(src))
	if period > len(src) {
		return Series{Outputs: map[string][]float64{"upper": upper, "mid": mid, "lower": lower}}, nil
	}
	for i := period - 1; i < len(src); i++ {
		var sum, sq float64
		for k := i - period + 1; k <= i; k++ {
			sum += src[k]
		}
		mean := sum / float64(period)
		for k := i - period + 1; k <= i; k++ {
			d := src[k] - mean
			sq += d * d
		}
		std := math.Sqrt(sq / float64(period))
		mid[i] = mean
		upper[i] = mean + mult*std
		lower[i] = mean - mult*std
	}
	return Series{Outputs: map[string][]float64{"upper": upper, "mid": mid, "lower": lower}}, nil
}
