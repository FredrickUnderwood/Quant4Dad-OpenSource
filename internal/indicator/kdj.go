package indicator

import (
	"math"

	"github.com/quant4dad/internal/domain"
)

type kdjCalc struct{}

func (kdjCalc) Name() string          { return "KDJ" }
func (kdjCalc) OutputNames() []string { return []string{"k", "d", "j"} }

// KDJ with the conventional (9, 3, 3) defaults and the smoothing formula:
// K_t = (2/3)*K_{t-1} + (1/3)*RSV_t ; D_t = (2/3)*D_{t-1} + (1/3)*K_t.
func (kdjCalc) Compute(bars []*domain.Bar, params map[string]any) (Series, error) {
	if err := ValidateParams((kdjCalc{}).Name(), params); err != nil {
		return Series{}, err
	}
	n := intParam(params, "n", 9)
	m1 := intParam(params, "m1", 3)
	m2 := intParam(params, "m2", 3)
	k := nanSlice(len(bars))
	d := nanSlice(len(bars))
	j := nanSlice(len(bars))
	for i := 0; i < len(bars); i++ {
		if i < n-1 {
			continue
		}
		hh, ll := bars[i].High, bars[i].Low
		for x := i - n + 1; x <= i; x++ {
			if bars[x].High > hh {
				hh = bars[x].High
			}
			if bars[x].Low < ll {
				ll = bars[x].Low
			}
		}
		var rsv float64
		if hh == ll {
			rsv = 50
		} else {
			rsv = (bars[i].Close - ll) / (hh - ll) * 100
		}
		prevK := 50.0
		if i > n-1 && !math.IsNaN(k[i-1]) {
			prevK = k[i-1]
		}
		prevD := 50.0
		if i > n-1 && !math.IsNaN(d[i-1]) {
			prevD = d[i-1]
		}
		k[i] = (float64(m1-1)*prevK + rsv) / float64(m1)
		d[i] = (float64(m2-1)*prevD + k[i]) / float64(m2)
		j[i] = 3*k[i] - 2*d[i]
	}
	return Series{Outputs: map[string][]float64{"k": k, "d": d, "j": j}}, nil
}
