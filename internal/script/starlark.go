// Package script hosts code-mode (Starlark) trading strategies. A compiled
// Program implements engine.Decider, so script strategies plug into the same
// backtest framework as config-mode rules — the engine, broker, portfolio and
// metrics are all reused unchanged.
//
// Script contract: the program must define a top-level function on_bar(ctx)
// that returns buy(...) / sell(...) / None for the current symbol & bar.
// Starlark is sandboxed (no IO/network), deterministic, and pure-Go, so it adds
// no runtime/deployment dependency.
package script

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.starlark.net/starlark"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/engine"
)

// Program is a compiled script strategy. It is compiled once and reused for
// every (symbol, bar) call within a backtest.
type Program struct {
	onBar starlark.Callable
	ctx   context.Context
}

const (
	MaxCodeBytes      = 16 << 10
	MaxExecutionSteps = 100000
)

// compile-time assertion that Program satisfies the engine seam.
var _ engine.Decider = (*Program)(nil)

// Compile parses & loads a Starlark strategy, validating that it defines an
// on_bar function. Returns a friendly error suitable for surfacing to the user
// at strategy-save time (syntax errors, missing on_bar, etc).
func Compile(code string) (*Program, error) {
	return CompileContext(context.Background(), code)
}

// CompileContext binds compilation and subsequent decisions to the caller's
// lifetime. Each invocation also has its own interpreter step budget.
func CompileContext(ctx context.Context, code string) (*Program, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if code == "" {
		return nil, errors.New("script code is empty")
	}
	if len(code) > MaxCodeBytes {
		return nil, errors.New("script code exceeds 16384 UTF-8 bytes")
	}
	thread, stop := executionThread(ctx, "compile")
	defer stop()
	globals, err := starlark.ExecFile(thread, "strategy.star", []byte(code), predeclared())
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	fn, ok := globals["on_bar"]
	if !ok {
		return nil, errors.New("script must define a function: def on_bar(ctx)")
	}
	callable, ok := fn.(*starlark.Function)
	if !ok || callable.NumParams() != 1 || callable.NumKwonlyParams() != 0 || callable.HasVarargs() || callable.HasKwargs() {
		return nil, errors.New("on_bar must be a function with exactly one positional parameter: def on_bar(ctx)")
	}
	globals.Freeze()
	return &Program{onBar: callable, ctx: ctx}, nil
}

func executionThread(ctx context.Context, name string) (*starlark.Thread, func() bool) {
	// Script print output is not application logging or execution evidence.
	thread := &starlark.Thread{Name: name, Print: func(*starlark.Thread, string) {}}
	thread.SetMaxExecutionSteps(MaxExecutionSteps)
	thread.SetLocal("context", ctx)
	stop := context.AfterFunc(ctx, func() { thread.Cancel("execution cancelled") })
	return thread, stop
}

// OnBar invokes on_bar(ctx) for one symbol on one bar and translates the
// returned value into an engine signal (nil = no action).
func (p *Program) OnBar(sd *engine.SymbolData, idx int, pf *engine.Portfolio) (*engine.Signal, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	thread, stop := executionThread(p.ctx, "on_bar")
	defer stop()
	ctx := &ctxValue{sd: sd, idx: idx, pf: pf}
	res, err := starlark.Call(thread, p.onBar, starlark.Tuple{ctx}, nil)
	if p.ctx.Err() != nil {
		return nil, p.ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	switch v := res.(type) {
	case starlark.NoneType:
		return nil, nil
	case *signalValue:
		sig := v.sig
		return &sig, nil
	default:
		return nil, fmt.Errorf("on_bar must return buy(...) / sell(...) / None, got %s", res.Type())
	}
}

// predeclared returns the global builtins visible to every script: the order
// helpers buy/sell, plus sum (not part of Starlark's universe but ubiquitous in
// indicator math like averages).
func predeclared() starlark.StringDict {
	return starlark.StringDict{
		"buy":  starlark.NewBuiltin("buy", builtinOrder(domain.TradeSideBuy)),
		"sell": starlark.NewBuiltin("sell", builtinOrder(domain.TradeSideSell)),
		"sum":  starlark.NewBuiltin("sum", builtinSum),
	}
}

// builtinSum adds Python-style sum(iterable, start=0) for numeric iterables.
func builtinSum(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var iterable starlark.Iterable
	var start starlark.Value
	if err := starlark.UnpackArgs("sum", args, kwargs, "iterable", &iterable, "start?", &start); err != nil {
		return nil, err
	}
	total := 0.0
	if start != nil {
		f, ok := starlark.AsFloat(start)
		if !ok {
			return nil, fmt.Errorf("sum: start must be a number, got %s", start.Type())
		}
		total = f
	}
	iter := iterable.Iterate()
	defer iter.Done()
	var x starlark.Value
	for count := 0; iter.Next(&x); count++ {
		// Host builtins do not advance the interpreter's instruction counter.
		if count >= MaxExecutionSteps {
			return nil, errors.New("sum: iterable exceeds the execution limit")
		}
		if ctx, ok := thread.Local("context").(context.Context); ok && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f, ok := starlark.AsFloat(x)
		if !ok {
			return nil, fmt.Errorf("sum: element must be a number, got %s", x.Type())
		}
		total += f
	}
	return starlark.Float(total), nil
}

// builtinOrder builds the buy/sell builtin. Accepts an optional positional size
// ("all") plus keyword sizing that maps onto domain.SizeSpec — the same sizing
// the broker already understands. Examples:
//
//	buy(pct_of_cash=0.5)   buy("all")   buy(shares=100)   buy(fixed_cash=1e4)
//	sell("all")            sell(pct_of_position=0.5)
func builtinOrder(side domain.TradeSide) func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var (
			positional    starlark.Value
			all           bool
			pctOfCash     starlark.Value
			pctOfPosition starlark.Value
			shares        int
			fixedCash     starlark.Value
		)
		if err := starlark.UnpackArgs(string(side), args, kwargs,
			"size?", &positional,
			"all?", &all,
			"pct_of_cash?", &pctOfCash,
			"pct_of_position?", &pctOfPosition,
			"shares?", &shares,
			"fixed_cash?", &fixedCash,
		); err != nil {
			return nil, err
		}
		if positional != nil {
			s, ok := starlark.AsString(positional)
			if !ok || s != "all" {
				return nil, fmt.Errorf("%s: positional size must be the string \"all\"", side)
			}
			all = true
		}
		spec := domain.SizeSpec{
			All:    all,
			Shares: shares,
		}
		for _, field := range []struct {
			name   string
			value  starlark.Value
			target *float64
		}{{"pct_of_cash", pctOfCash, &spec.PctOfCash}, {"pct_of_position", pctOfPosition, &spec.PctOfPosition}, {"fixed_cash", fixedCash, &spec.FixedCash}} {
			if field.value == nil {
				continue
			}
			value, ok := starlark.AsFloat(field.value)
			if !ok {
				return nil, errors.New(field.name + " must be a number")
			}
			*field.target = value
		}
		if len(args)+len(kwargs) != 1 {
			return nil, errors.New(string(side) + ": specify exactly one size")
		}
		if err := validateSize(side, spec); err != nil {
			return nil, err
		}
		return &signalValue{sig: engine.Signal{Side: side, Size: spec, Rule: string(side)}}, nil
	}
}

func validateSize(side domain.TradeSide, spec domain.SizeSpec) error {
	for _, value := range []float64{spec.PctOfCash, spec.PctOfPosition, spec.FixedCash} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return errors.New("order size must be finite and positive")
		}
	}
	if spec.PctOfCash > 1 || spec.PctOfPosition > 1 || spec.Shares < 0 || spec.Shares > 100000000 || spec.FixedCash > 1000000000 {
		return errors.New("order size out of bounds: fraction <= 1, shares <= 100000000, fixed_cash <= 1000000000")
	}
	if (side == domain.TradeSideBuy && spec.PctOfPosition != 0) || (side == domain.TradeSideSell && spec.PctOfCash != 0) {
		return errors.New("buy supports pct_of_cash; sell supports pct_of_position")
	}
	if !spec.All && spec.PctOfCash == 0 && spec.PctOfPosition == 0 && spec.Shares == 0 && spec.FixedCash == 0 {
		return errors.New("order size must be positive or all")
	}
	return nil
}

// ---------------------------------------------------------------------------
// signalValue — opaque Starlark wrapper around an engine.Signal
// ---------------------------------------------------------------------------

type signalValue struct{ sig engine.Signal }

func (v *signalValue) String() string        { return "signal(" + string(v.sig.Side) + ")" }
func (v *signalValue) Type() string          { return "signal" }
func (v *signalValue) Freeze()               {}
func (v *signalValue) Truth() starlark.Bool  { return starlark.True }
func (v *signalValue) Hash() (uint32, error) { return 0, errors.New("signal is unhashable") }

// ---------------------------------------------------------------------------
// ctxValue — the `ctx` object passed to on_bar(ctx)
// ---------------------------------------------------------------------------

type ctxValue struct {
	sd  *engine.SymbolData
	idx int
	pf  *engine.Portfolio
}

func (c *ctxValue) String() string        { return "ctx" }
func (c *ctxValue) Type() string          { return "ctx" }
func (c *ctxValue) Freeze()               {}
func (c *ctxValue) Truth() starlark.Bool  { return starlark.True }
func (c *ctxValue) Hash() (uint32, error) { return 0, errors.New("ctx is unhashable") }

func (c *ctxValue) bar() *domain.Bar { return c.sd.Bars[c.idx] }

func (c *ctxValue) AttrNames() []string {
	return []string{
		"index", "date", "open", "high", "low", "close", "volume",
		"has_position", "entry_price", "days_held", "history", "pnl_pct",
	}
}

func (c *ctxValue) Attr(name string) (starlark.Value, error) {
	bar := c.bar()
	switch name {
	case "index":
		return starlark.MakeInt(c.idx), nil
	case "date":
		return starlark.String(bar.Date.Format("2006-01-02")), nil
	case "open":
		return starlark.Float(bar.Open), nil
	case "high":
		return starlark.Float(bar.High), nil
	case "low":
		return starlark.Float(bar.Low), nil
	case "close":
		return starlark.Float(bar.Close), nil
	case "volume":
		return starlark.Float(bar.Volume), nil
	case "has_position":
		return starlark.Bool(c.pf.Has(c.sd.Symbol)), nil
	case "entry_price":
		if pos := c.pf.Position(c.sd.Symbol); pos != nil {
			return starlark.Float(pos.EntryPrice), nil
		}
		return starlark.Float(0), nil
	case "days_held":
		if pos := c.pf.Position(c.sd.Symbol); pos != nil && pos.Qty > 0 {
			d := int(normalizeDate(bar.Date).Sub(normalizeDate(pos.EntryDate)).Hours() / 24)
			return starlark.MakeInt(d), nil
		}
		return starlark.MakeInt(0), nil
	case "history":
		return starlark.NewBuiltin("history", c.historyFn), nil
	case "pnl_pct":
		return starlark.NewBuiltin("pnl_pct", c.pnlPctFn), nil
	}
	return nil, nil // nil,nil => AttributeError raised by Starlark
}

// history(field, n) -> list of the last n `field` values up to & including the
// current bar (oldest first). Fewer than n bars available => shorter list.
func (c *ctxValue) historyFn(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var field string
	var n int
	if err := starlark.UnpackArgs("history", args, kwargs, "field", &field, "n", &n); err != nil {
		return nil, err
	}
	get, err := barField(field)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return starlark.NewList(nil), nil
	}
	start := c.idx - n + 1
	if start < 0 {
		start = 0
	}
	elems := make([]starlark.Value, 0, c.idx-start+1)
	for i := start; i <= c.idx; i++ {
		elems = append(elems, starlark.Float(get(c.sd.Bars[i])))
	}
	return starlark.NewList(elems), nil
}

// pnl_pct() -> (close-entry)/entry for the open position, or None if flat.
func (c *ctxValue) pnlPctFn(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("pnl_pct", args, kwargs); err != nil {
		return nil, err
	}
	pos := c.pf.Position(c.sd.Symbol)
	if pos == nil || pos.Qty == 0 || pos.EntryPrice == 0 {
		return starlark.None, nil
	}
	return starlark.Float((c.bar().Close - pos.EntryPrice) / pos.EntryPrice), nil
}

func barField(field string) (func(*domain.Bar) float64, error) {
	switch field {
	case "open":
		return func(b *domain.Bar) float64 { return b.Open }, nil
	case "high":
		return func(b *domain.Bar) float64 { return b.High }, nil
	case "low":
		return func(b *domain.Bar) float64 { return b.Low }, nil
	case "close":
		return func(b *domain.Bar) float64 { return b.Close }, nil
	case "volume":
		return func(b *domain.Bar) float64 { return b.Volume }, nil
	}
	return nil, fmt.Errorf("unknown field %q (want open/high/low/close/volume)", field)
}

// normalizeDate truncates to the calendar day, matching engine.normalizeDate so
// days_held is computed identically to config mode.
func normalizeDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
