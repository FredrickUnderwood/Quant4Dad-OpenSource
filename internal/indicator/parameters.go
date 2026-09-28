package indicator

import (
	"errors"
	"math"
	"slices"
)

// Parameter definitions are shared by discovery, strategy validation and the
// calculators. Defaults are applied only when a parameter is absent.
type Parameter struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  any      `json:"default,omitempty"`
	Minimum  float64  `json:"minimum,omitempty"`
	Maximum  float64  `json:"maximum,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

func Parameters(name string) map[string]Parameter {
	period := func(value int, required bool) Parameter {
		p := Parameter{Type: "integer", Required: required, Minimum: 1, Maximum: 10000}
		if !required {
			p.Default = value
		}
		return p
	}
	source := Parameter{Type: "string", Default: "close", Enum: []string{"open", "high", "low", "close", "volume"}}
	switch upper(name) {
	case "MA", "EMA":
		return map[string]Parameter{"period": period(0, true), "source": source}
	case "MACD":
		return map[string]Parameter{"fast": period(12, false), "slow": period(26, false), "signal": period(9, false), "source": source}
	case "RSI":
		return map[string]Parameter{"period": period(14, false), "source": source}
	case "ATR":
		return map[string]Parameter{"period": period(14, false)}
	case "BOLL":
		return map[string]Parameter{"period": period(20, false), "mult": {Type: "number", Default: 2.0, Minimum: 0.01, Maximum: 100}, "source": source}
	case "KDJ":
		return map[string]Parameter{"n": period(9, false), "m1": period(3, false), "m2": period(3, false)}
	default:
		return nil
	}
}

func numeric(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case interface{ Float64() (float64, error) }:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func ValidateParams(name string, params map[string]any) error {
	definitions := Parameters(name)
	if definitions == nil {
		return errors.New("unknown indicator type")
	}
	for key := range params {
		if _, ok := definitions[key]; !ok {
			return errors.New("unknown parameter: " + key)
		}
	}
	for key, def := range definitions {
		value, present := params[key]
		if !present {
			if def.Required {
				return errors.New("required parameter: " + key)
			}
			continue
		}
		if def.Type == "string" {
			v, ok := value.(string)
			if !ok || !slices.Contains(def.Enum, v) {
				return errors.New("invalid value for parameter: " + key)
			}
			continue
		}
		n, ok := numeric(value)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < def.Minimum || n > def.Maximum || (def.Type == "integer" && math.Trunc(n) != n) {
			return errors.New("parameter out of range or wrong type: " + key)
		}
	}
	return nil
}

// ParameterSchema is also used by the Agent tool catalog.
func ParameterSchema(name string) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for key, p := range Parameters(name) {
		s := map[string]any{"type": p.Type}
		if p.Type != "string" {
			s["minimum"], s["maximum"] = p.Minimum, p.Maximum
		}
		if p.Default != nil {
			s["default"] = p.Default
		}
		if len(p.Enum) > 0 {
			s["enum"] = p.Enum
		}
		if p.Required {
			required = append(required, key)
		}
		properties[key] = s
	}
	slices.Sort(required)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}
