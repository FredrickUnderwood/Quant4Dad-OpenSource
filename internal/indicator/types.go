package indicator

import (
	"errors"
	"math"
	"sort"

	"github.com/quant4dad/internal/domain"
)

// Series is an indicator-aligned series. The output index matches the input
// bar index 1:1; values that cannot be computed (warm-up period) hold NaN.
type Series struct {
	// Outputs is keyed by output name. Single-output indicators conventionally
	// use the key "value"; multi-output indicators (MACD, KDJ, BOLL) use named
	// keys like "dif" / "dea" / "macd" or "upper" / "mid" / "lower".
	Outputs map[string][]float64
}

// Value returns Outputs[name][i] or NaN when missing.
func (s Series) Value(name string, i int) float64 {
	v, ok := s.Outputs[name]
	if !ok || i < 0 || i >= len(v) {
		return math.NaN()
	}
	return v[i]
}

// Calculator is the per-indicator computation function.
type Calculator interface {
	Name() string
	Compute(bars []*domain.Bar, params map[string]any) (Series, error)
	// OutputNames declares the keys present in Series.Outputs. UI uses this
	// to let users reference sub-outputs like "macd.dif".
	OutputNames() []string
}

var registry = map[string]Calculator{}

func Register(c Calculator) {
	registry[c.Name()] = c
}

// Get returns the registered calculator by canonical name (case-insensitive).
func Get(name string) (Calculator, error) {
	if c, ok := registry[name]; ok {
		return c, nil
	}
	if c, ok := registry[upper(name)]; ok {
		return c, nil
	}
	return nil, errors.New("unknown indicator: " + name)
}

// List returns metadata about all registered indicators (for UI dropdowns).
func List() []map[string]any {
	out := make([]map[string]any, 0, len(registry))
	for name, c := range registry {
		out = append(out, map[string]any{
			"name":             name,
			"outputs":          c.OutputNames(),
			"parameters":       Parameters(name),
			"parameter_schema": ParameterSchema(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Helpers shared across indicator implementations
// ---------------------------------------------------------------------------

func nanSlice(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.NaN()
	}
	return out
}

func source(bars []*domain.Bar, key string) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		switch key {
		case "open":
			out[i] = b.Open
		case "high":
			out[i] = b.High
		case "low":
			out[i] = b.Low
		case "volume":
			out[i] = b.Volume
		default:
			out[i] = b.Close
		}
	}
	return out
}

func intParam(p map[string]any, key string, def int) int {
	v, ok := p[key]
	if !ok {
		return def
	}
	if n, ok := numeric(v); ok {
		return int(n)
	}
	return def
}

func stringParam(p map[string]any, key string, def string) string {
	v, ok := p[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func floatParam(p map[string]any, key string, def float64) float64 {
	v, ok := p[key]
	if !ok {
		return def
	}
	if n, ok := numeric(v); ok {
		return n
	}
	return def
}
