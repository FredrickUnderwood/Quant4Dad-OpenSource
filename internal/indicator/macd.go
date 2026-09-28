package indicator

import (
	"math"

	"github.com/quant4dad/internal/domain"
)

type macdCalc struct{}

func (macdCalc) Name() string          { return "MACD" }
func (macdCalc) OutputNames() []string { return []string{"dif", "dea", "macd"} }

func (macdCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((macdCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	fast := intParam(params, "fast", 12)
	slow := intParam(params, "slow", 26)
	signal := intParam(params, "signal", 9)
	src := source(bars, stringParam(params, "source", "close"))

	fastEMA := ema(src, fast)
	slowEMA := ema(src, slow)
	dif := make([]float64, len(src))
	for i := range src {
		if math.IsNaN(fastEMA[i]) || math.IsNaN(slowEMA[i]) {
			dif[i] = math.NaN()
		} else {
			dif[i] = fastEMA[i] - slowEMA[i]
		}
	}
	// DEA: signal-period EMA of dif, skipping leading NaN
	dea := nanSlice(len(src))
	first := -1
	for i, v := range dif {
		if !math.IsNaN(v) {
			first = i
			break
		}
	}
	if first >= 0 && first+signal <= len(dif) {
		sum := 0.0
		for i := first; i < first+signal; i++ {
			sum += dif[i]
		}
		dea[first+signal-1] = sum / float64(signal)
		alpha := 2.0 / float64(signal+1)
		for i := first + signal; i < len(dif); i++ {
			dea[i] = alpha*dif[i] + (1-alpha)*dea[i-1]
		}
	}
	macd := make([]float64, len(src))
	for i := range src {
		if math.IsNaN(dif[i]) || math.IsNaN(dea[i]) {
			macd[i] = math.NaN()
		} else {
			macd[i] = 2 * (dif[i] - dea[i])
		}
	}
	return Series{Outputs: map[string][]float64{
		"dif": dif, "dea": dea, "macd": macd,
	}}, nil
}

var _ = func(b []*domain.Bar) {}
