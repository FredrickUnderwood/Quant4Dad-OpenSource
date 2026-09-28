package script

import (
	"errors"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Bump when checks or server-owned expectations change. Validation receipts bind it.
const CheckerRevision = "strategy-check-v1"

type Diagnostic struct {
	Code          string `json:"code"`
	Phase         string `json:"phase"`
	Severity      string `json:"severity"`
	Path          string `json:"path"`
	Message       string `json:"message"`
	Line          int32  `json:"line,omitempty"`
	Column        int32  `json:"column,omitempty"`
	BarIndex      *int   `json:"bar_index,omitempty"`
	PositionState string `json:"position_state,omitempty"`
}

type SmokeReport struct {
	Status         string         `json:"status"`
	Bars           int            `json:"bars"`
	PositionStates []string       `json:"position_states"`
	Attempted      int            `json:"attempted"`
	Passed         int            `json:"passed"`
	Signals        map[string]int `json:"signals"`
}

type Report struct {
	CheckerRevision        string         `json:"checker_revision"`
	Compile                string         `json:"compile"`
	Smoke                  SmokeReport    `json:"smoke"`
	Behavior               BehaviorReport `json:"behavior"`
	BranchCoverageMeasured bool           `json:"branch_coverage_measured"`
	Diagnostics            []Diagnostic   `json:"diagnostics"`
}

func newReport(suites []string) Report {
	status := "not_requested"
	if len(suites) > 0 {
		status = "not_run"
	}
	return Report{CheckerRevision: CheckerRevision, Compile: "not_run", Smoke: SmokeReport{Status: "not_run", Bars: 64, PositionStates: []string{"flat", "held_flat", "held_profit", "held_loss"}, Signals: map[string]int{"buy": 0, "sell": 0, "none": 0}}, Behavior: BehaviorReport{Status: status, Suites: append([]string{}, suites...), Cases: []BehaviorResult{}}, Diagnostics: []Diagnostic{}}
}

func diagnostic(code, phase string, err error) Diagnostic {
	d := Diagnostic{Code: code, Phase: phase, Severity: "error", Path: "body.code", Message: err.Error()}
	var parse syntax.Error
	var eval *starlark.EvalError
	if errors.As(err, &parse) {
		d.Line, d.Column = parse.Pos.Line, parse.Pos.Col
	}
	if errors.As(err, &eval) {
		for i := len(eval.CallStack) - 1; i >= 0; i-- {
			p := eval.CallStack[i].Pos
			if p.Line > 0 {
				d.Line, d.Column = p.Line, p.Col
				break
			}
		}
	}
	if len([]rune(d.Message)) > 1024 {
		d.Message = string([]rune(d.Message)[:1024])
	}
	return d
}
