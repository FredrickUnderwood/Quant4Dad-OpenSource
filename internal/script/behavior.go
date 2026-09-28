package script

import (
	"errors"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
)

const MA3CrossUpEntry = "ma3_cross_up_entry"
const DailyRoundTrip100 = "flat_buy_100_exit_after_one_day"

// SuiteContract describes what a server-owned test proves, independently of
// model explanations. An empty rule means that dimension is not checked.
type SuiteContract struct {
	ID     string `json:"id"`
	Mode   string `json:"mode"`
	Entry  string `json:"entry"`
	Sizing string `json:"sizing"`
	Exit   string `json:"exit"`
}

func SuiteContracts() []SuiteContract {
	return []SuiteContract{
		{ID: MA3CrossUpEntry, Mode: "script", Entry: "previous_close <= previous_ma3 and current_close > current_ma3; current_ma3 includes current_close; four bars required", Sizing: "", Exit: ""},
		{ID: DailyRoundTrip100, Mode: "script", Entry: "buy when flat", Sizing: "buy exactly 100 shares", Exit: "wait on entry day; sell all after at least one calendar day, including losing positions"},
	}
}

type BehaviorSignal struct {
	Action string           `json:"action"`
	Size   *domain.SizeSpec `json:"size,omitempty"`
}
type BehaviorResult struct {
	Suite    string          `json:"suite"`
	Case     string          `json:"case"`
	Passed   bool            `json:"passed"`
	Expected BehaviorSignal  `json:"expected"`
	Actual   *BehaviorSignal `json:"actual,omitempty"`
	Error    string          `json:"error,omitempty"`
}
type BehaviorReport struct {
	Status string           `json:"status"`
	Suites []string         `json:"suites"`
	Cases  []BehaviorResult `json:"cases"`
}

func ValidateSuites(suites []string) error {
	seen := map[string]bool{}
	for _, s := range suites {
		if (s != MA3CrossUpEntry && s != DailyRoundTrip100) || seen[s] {
			return errors.New("unknown or duplicate behavior test suite")
		}
		seen[s] = true
	}
	return nil
}

type behaviorCase struct {
	name     string
	closes   []float64
	held     bool
	days     int
	entry    float64
	expected BehaviorSignal
}

func behaviorCases(suite string) []behaviorCase {
	none := BehaviorSignal{Action: "none"}
	buy := BehaviorSignal{Action: "buy", Size: &domain.SizeSpec{Shares: 100}}
	sell := BehaviorSignal{Action: "sell", Size: &domain.SizeSpec{All: true}}
	if suite == MA3CrossUpEntry {
		// Entry only: no exit requirement is inferred. Sizes are intentionally
		// unconstrained for this suite; sizing needs its own user requirement.
		return []behaviorCase{
			{name: "insufficient_history", closes: []float64{3, 2, 4}, expected: none},
			{name: "crosses_from_below", closes: []float64{3, 2, 1, 4}, expected: BehaviorSignal{Action: "buy"}},
			{name: "crosses_from_equal", closes: []float64{1, 3, 2, 4}, expected: BehaviorSignal{Action: "buy"}},
			{name: "touches_equal_only", closes: []float64{4, 3, 1, 2}, expected: none},
			{name: "already_above", closes: []float64{1, 2, 3, 4}, expected: none},
			{name: "stays_below", closes: []float64{4, 3, 2, 1}, expected: none},
		}
	}
	return []behaviorCase{
		{name: "flat_buys_100", closes: []float64{100, 101}, expected: buy},
		{name: "held_same_day_waits", closes: []float64{100, 101}, held: true, entry: 100, expected: none},
		{name: "held_one_calendar_day_sells_all", closes: []float64{100, 101}, held: true, days: 1, entry: 100, expected: sell},
		{name: "held_loss_still_sells", closes: []float64{100, 80}, held: true, days: 1, entry: 100, expected: sell},
		{name: "held_multiple_days_sells", closes: []float64{100, 125}, held: true, days: 3, entry: 100, expected: sell},
	}
}

func runBehaviors(p *Program, suites []string, report *Report) error {
	if len(suites) == 0 {
		return nil
	}
	report.Behavior.Status = "passed"
	for _, suite := range suites {
		for _, c := range behaviorCases(suite) {
			const symbol = "script-check"
			bars := make([]*domain.Bar, len(c.closes))
			for i, price := range c.closes {
				bars[i] = &domain.Bar{Code: symbol, Period: domain.Bar1d, Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC), Open: price, High: price + 1, Low: price - 1, Close: price, Volume: 1000}
			}
			pf := engine.NewPortfolio(100000)
			if c.held {
				pf.Positions[symbol] = &engine.Position{Symbol: symbol, Qty: 100, EntryPrice: c.entry, EntryDate: bars[len(bars)-1].Date.AddDate(0, 0, -c.days)}
			}
			signal, err := p.OnBar(&engine.SymbolData{Symbol: symbol, Bars: bars}, len(bars)-1, pf)
			r := BehaviorResult{Suite: suite, Case: c.name, Expected: c.expected}
			if err != nil {
				d := diagnostic("script_behavior_execution_failed", "behavior", err)
				report.Diagnostics = append(report.Diagnostics, d)
				r.Error = d.Message
			} else {
				actual := BehaviorSignal{Action: "none"}
				if signal != nil {
					actual.Action = string(signal.Side)
					actual.Size = &signal.Size
				}
				r.Actual = &actual
				r.Passed = actual.Action == c.expected.Action && (c.expected.Size == nil || (actual.Size != nil && *actual.Size == *c.expected.Size))
				if !r.Passed {
					report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "script_behavior_mismatch", Phase: "behavior", Severity: "error", Path: "body.code", Message: "Behavior expectation failed: " + suite + "/" + c.name})
				}
			}
			report.Behavior.Cases = append(report.Behavior.Cases, r)
			if !r.Passed {
				report.Behavior.Status = "failed"
			}
			if p.ctx.Err() != nil {
				return p.ctx.Err()
			}
		}
	}
	if report.Behavior.Status == "failed" {
		return errors.New("selected behavior tests failed; inspect script_report.behavior.cases")
	}
	return nil
}
