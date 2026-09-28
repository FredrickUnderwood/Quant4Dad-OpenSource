package script

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
)

// Check runs deterministic contract probes, not a backtest or a proof of the
// strategy's intent. Every probe has an independent portfolio; no orders fill.
// Long warmups and unvisited branches still require review and real-data tests.
func Check(ctx context.Context, code string) error {
	_, err := Inspect(ctx, code, nil)
	return err
}

// Inspect returns execution evidence, not a proof of path coverage or intent.
// Named behavior suites have server-owned expectations; scripts cannot edit them.
func Inspect(ctx context.Context, code string, suites []string) (Report, error) {
	report := newReport(suites)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := ValidateSuites(suites); err != nil {
		return report, err
	}
	program, err := CompileContext(ctx, code)
	if err != nil {
		report.Compile = "failed"
		report.Diagnostics = append(report.Diagnostics, diagnostic("script_compile_failed", "compile", err))
		return report, err
	}
	report.Compile = "passed"
	const symbol = "script-check"
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]*domain.Bar, 64)
	for i := range bars {
		// Flat, rising and falling prices, with zero-volume bars included.
		price := 100.0
		if i >= 16 && i < 40 {
			price += float64(i - 16)
		} else if i >= 40 {
			price = 124 - 2*float64(i-40)
		}
		volume := 1000.0
		if i%16 == 0 {
			volume = 0
		}
		bars[i] = &domain.Bar{Code: symbol, Period: domain.Bar1d, Date: start.AddDate(0, 0, i), Open: price, High: price + 1, Low: price - 1, Close: price, Volume: volume}
	}
	for i, bar := range bars {
		// Only the prefix is provided, even though ctx.history already prevents
		// future access. Cover flat, breakeven, profitable and losing positions.
		sd := &engine.SymbolData{Symbol: symbol, Bars: bars[:i+1]}
		for _, state := range []struct {
			name  string
			entry float64
		}{{"flat", 0}, {"held_flat", bar.Close}, {"held_profit", bar.Close / 1.25}, {"held_loss", bar.Close / 0.8}} {
			pf := engine.NewPortfolio(100000)
			if state.entry > 0 {
				pf.Positions[symbol] = &engine.Position{Symbol: symbol, Qty: 100, EntryPrice: state.entry, EntryDate: start}
			}
			report.Smoke.Attempted++
			signal, err := program.OnBar(sd, i, pf)
			if err != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				report.Smoke.Status = "failed"
				d := diagnostic("script_smoke_failed", "synthetic_smoke", err)
				d.BarIndex, d.PositionState = &i, state.name
				report.Diagnostics = append(report.Diagnostics, d)
				if ctx.Err() != nil {
					return report, ctx.Err()
				}
				return report, errors.New("synthetic smoke test (bar " + strconv.Itoa(i) + ", " + state.name + "): " + err.Error())
			}
			report.Smoke.Passed++
			action := "none"
			if signal != nil {
				action = string(signal.Side)
			}
			report.Smoke.Signals[action]++
		}
	}
	report.Smoke.Status = "passed"
	if report.Smoke.Signals["buy"]+report.Smoke.Signals["sell"] == 0 {
		report.Diagnostics = append(report.Diagnostics, Diagnostic{Code: "script_no_signal_observed", Phase: "synthetic_smoke", Severity: "warning", Path: "body.code", Message: "All 256 smoke invocations returned None. No buy or sell signal was observed; entry and exit behavior is unverified. A warmup longer than 64 bars is not exercised by this smoke test."})
	}
	err = runBehaviors(program, suites, &report)
	return report, err
}
