package domain

import (
	"encoding/json"
	"time"
)

// Strategy is the user-defined reusable trading rule set.
// Body (indicators / rules / execution) is persisted as a JSON blob; this lets
// the schema evolve without migrations and keeps the storage backend agnostic.
type Strategy struct {
	ID          int64           `json:"id"          gorm:"primaryKey;autoIncrement"`
	Version     int             `json:"version"     gorm:"not null;default:1"`
	Name        string          `json:"name"        gorm:"uniqueIndex;size:128;not null"`
	Description string          `json:"description" gorm:"type:text"`
	Universe    StringSlice     `json:"universe"    gorm:"type:text"` // []string of instrument codes
	Period      BarPeriod       `json:"period"      gorm:"size:8"`
	Body        json.RawMessage `json:"body"        gorm:"type:text"` // {indicators, rules, execution}
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (Strategy) TableName() string { return "strategy" }

// StrategyBody is the inner shape stored in Strategy.Body.
//
// Two modes share this struct:
//   - config mode (Mode == "" / "config"): driven by Indicators + Rules.
//   - script mode (Mode == "script"): driven by Code (a Starlark program);
//     Indicators/Rules are ignored. Execution (fill_at) applies to both.
type StrategyBody struct {
	Mode       string          `json:"mode,omitempty"` // "" / "config" / "script"
	Lang       string          `json:"lang,omitempty"` // "starlark" when Mode == "script"
	Code       string          `json:"code,omitempty"` // script source
	Indicators []IndicatorSpec `json:"indicators"`
	Rules      []RuleSpec      `json:"rules"`
	Execution  ExecutionSpec   `json:"execution"`
}

// IsScript reports whether the body is a script-mode strategy.
func (b StrategyBody) IsScript() bool { return b.Mode == "script" }

type IndicatorSpec struct {
	Alias  string         `json:"alias"`
	Type   string         `json:"type"`
	Params map[string]any `json:"params"`
}

type RuleSpec struct {
	Name string          `json:"name"`
	When json.RawMessage `json:"when"`
	Then ActionSpec      `json:"then"`
}

type ActionSpec struct {
	Action string   `json:"action"` // buy / sell
	Size   SizeSpec `json:"size"`
}

// SizeSpec accepts either the string "all" or an object like {"pct_of_cash": 0.5}
// or {"shares": 100}. Unmarshal handles both forms.
//
// Buy supports:  all / pct_of_cash / fixed_cash / shares
// Sell supports: all / pct_of_position / fixed_cash / shares
// sell + pct_of_cash is an invalid combination; the broker rejects the order.
type SizeSpec struct {
	All           bool    `json:"-"`
	PctOfCash     float64 `json:"pct_of_cash,omitempty"`
	PctOfPosition float64 `json:"pct_of_position,omitempty"`
	Shares        int     `json:"shares,omitempty"`
	FixedCash     float64 `json:"fixed_cash,omitempty"`
}

func (s *SizeSpec) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		if raw == "all" {
			s.All = true
		}
		return nil
	}
	type alias SizeSpec
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*s = SizeSpec(a)
	return nil
}

func (s SizeSpec) MarshalJSON() ([]byte, error) {
	if s.All {
		return []byte(`"all"`), nil
	}
	type alias SizeSpec
	return json.Marshal(alias(s))
}

type ExecutionSpec struct {
	FillAt string `json:"fill_at"` // next_open / close
}
