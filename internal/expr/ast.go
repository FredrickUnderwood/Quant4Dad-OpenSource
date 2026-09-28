package expr

import (
	"errors"
	"math"
)

// EvalContext is supplied per bar (per symbol) by the engine. Indicator values
// resolve via Indicators[alias] -> output map -> current index.
type EvalContext struct {
	Index       int
	Indicators  map[string]map[string][]float64 // alias -> output name -> series
	OHLCV       map[string]float64              // open/high/low/close/volume at Index
	OHLCVPrev   map[string]float64              // same at Index-1, nil at Index==0
	Symbol      string
	HasPosition bool
	EntryPrice  float64
	DaysHeld    int
}

// Resolve a non-OHLCV / non-context name to a series value at index i.
// Supports "alias" or "alias.output". Returns NaN when missing.
func (c *EvalContext) seriesValueAt(name string, i int) float64 {
	if i < 0 {
		return math.NaN()
	}
	alias, sub := name, "value"
	for k := 0; k < len(name); k++ {
		if name[k] == '.' {
			alias = name[:k]
			sub = name[k+1:]
			break
		}
	}
	outs, ok := c.Indicators[alias]
	if !ok {
		return math.NaN()
	}
	series, ok := outs[sub]
	if !ok {
		return math.NaN()
	}
	if i >= len(series) {
		return math.NaN()
	}
	return series[i]
}

// Node is one parsed expression.
type Node interface {
	Eval(ctx *EvalContext) (any, error)
}

// ---------------------------------------------------------------------------
// Logical
// ---------------------------------------------------------------------------

type AndNode struct{ Children []Node }
type OrNode struct{ Children []Node }
type NotNode struct{ Child Node }

func (n AndNode) Eval(ctx *EvalContext) (any, error) {
	for _, c := range n.Children {
		v, err := c.Eval(ctx)
		if err != nil {
			return nil, err
		}
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("all: child did not return bool")
		}
		if !b {
			return false, nil
		}
	}
	return true, nil
}

func (n OrNode) Eval(ctx *EvalContext) (any, error) {
	for _, c := range n.Children {
		v, err := c.Eval(ctx)
		if err != nil {
			return nil, err
		}
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("any: child did not return bool")
		}
		if b {
			return true, nil
		}
	}
	return false, nil
}

func (n NotNode) Eval(ctx *EvalContext) (any, error) {
	v, err := n.Child.Eval(ctx)
	if err != nil {
		return nil, err
	}
	b, ok := v.(bool)
	if !ok {
		return nil, errors.New("not: child did not return bool")
	}
	return !b, nil
}

// ---------------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------------

type CmpOp string

const (
	OpGT  CmpOp = "gt"
	OpLT  CmpOp = "lt"
	OpGTE CmpOp = "gte"
	OpLTE CmpOp = "lte"
	OpEQ  CmpOp = "eq"
)

type CmpNode struct {
	Op       CmpOp
	Lhs, Rhs Node
}

func (n CmpNode) Eval(ctx *EvalContext) (any, error) {
	lv, err := n.Lhs.Eval(ctx)
	if err != nil {
		return nil, err
	}
	rv, err := n.Rhs.Eval(ctx)
	if err != nil {
		return nil, err
	}
	l, ok := toFloat(lv)
	if !ok {
		return false, nil
	}
	r, ok := toFloat(rv)
	if !ok {
		return false, nil
	}
	if math.IsNaN(l) || math.IsNaN(r) {
		return false, nil
	}
	switch n.Op {
	case OpGT:
		return l > r, nil
	case OpLT:
		return l < r, nil
	case OpGTE:
		return l >= r, nil
	case OpLTE:
		return l <= r, nil
	case OpEQ:
		return l == r, nil
	}
	return false, errors.New("unknown cmp op")
}

// ---------------------------------------------------------------------------
// Cross
// ---------------------------------------------------------------------------

type CrossDir int

const (
	CrossUp CrossDir = iota
	CrossDown
)

// CrossNode supports two argument forms: two refs (alias-vs-alias) or one ref
// vs a constant. The crossing is detected against the previous bar's values.
type CrossNode struct {
	Dir CrossDir
	A   Node
	B   Node
}

func (n CrossNode) Eval(ctx *EvalContext) (any, error) {
	if ctx.Index == 0 {
		return false, nil
	}
	aNow, ok := evalAt(n.A, ctx, ctx.Index)
	if !ok {
		return false, nil
	}
	aPrev, ok := evalAt(n.A, ctx, ctx.Index-1)
	if !ok {
		return false, nil
	}
	bNow, ok := evalAt(n.B, ctx, ctx.Index)
	if !ok {
		return false, nil
	}
	bPrev, ok := evalAt(n.B, ctx, ctx.Index-1)
	if !ok {
		return false, nil
	}
	if math.IsNaN(aNow) || math.IsNaN(aPrev) || math.IsNaN(bNow) || math.IsNaN(bPrev) {
		return false, nil
	}
	switch n.Dir {
	case CrossUp:
		return aPrev <= bPrev && aNow > bNow, nil
	case CrossDown:
		return aPrev >= bPrev && aNow < bNow, nil
	}
	return false, nil
}

// evalAt resolves the numeric value of a node at a specific bar index.
// Refs use the index directly; constants ignore it.
func evalAt(n Node, ctx *EvalContext, idx int) (float64, bool) {
	switch x := n.(type) {
	case RefNode:
		return x.valueAt(ctx, idx), true
	case ConstNode:
		return x.Value, true
	}
	// Fallback: only the current index value is available.
	v, err := n.Eval(ctx)
	if err != nil {
		return 0, false
	}
	f, ok := toFloat(v)
	return f, ok
}

// ---------------------------------------------------------------------------
// Leaves
// ---------------------------------------------------------------------------

type ConstNode struct{ Value float64 }

func (n ConstNode) Eval(ctx *EvalContext) (any, error) { return n.Value, nil }

type BoolNode struct{ Value bool }

func (n BoolNode) Eval(ctx *EvalContext) (any, error) { return n.Value, nil }

// RefNode references either an indicator alias (optionally with "alias.sub"),
// or an OHLCV field at the current bar.
type RefNode struct{ Name string }

func (n RefNode) Eval(ctx *EvalContext) (any, error) {
	return n.valueAt(ctx, ctx.Index), nil
}

func (n RefNode) valueAt(ctx *EvalContext, idx int) float64 {
	switch n.Name {
	case "open", "high", "low", "close", "volume":
		if idx == ctx.Index {
			return ctx.OHLCV[n.Name]
		}
		if idx == ctx.Index-1 && ctx.OHLCVPrev != nil {
			return ctx.OHLCVPrev[n.Name]
		}
		return math.NaN()
	}
	return ctx.seriesValueAt(n.Name, idx)
}

// ---------------------------------------------------------------------------
// Context calls — boolean / numeric helpers about portfolio state
// ---------------------------------------------------------------------------

type CallNode struct {
	Op  string
	Arg Node // optional
}

func (n CallNode) Eval(ctx *EvalContext) (any, error) {
	switch n.Op {
	case "has_position":
		return ctx.HasPosition, nil
	case "pnl_pct":
		if !ctx.HasPosition || ctx.EntryPrice == 0 {
			return math.NaN(), nil
		}
		return (ctx.OHLCV["close"] - ctx.EntryPrice) / ctx.EntryPrice, nil
	case "days_held":
		return float64(ctx.DaysHeld), nil
	case "ratio_from_entry":
		if !ctx.HasPosition || ctx.EntryPrice == 0 {
			return math.NaN(), nil
		}
		field := "close"
		if n.Arg != nil {
			if r, ok := n.Arg.(RefNode); ok {
				field = r.Name
			}
		}
		v, ok := ctx.OHLCV[field]
		if !ok {
			return math.NaN(), nil
		}
		return v / ctx.EntryPrice, nil
	}
	return nil, errors.New("unknown context call: " + n.Op)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
