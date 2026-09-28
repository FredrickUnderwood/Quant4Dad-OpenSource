package script

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quant4dad/internal/engine"
)

func TestScriptCheckFindsContractAndRuntimeErrors(t *testing.T) {
	for name, code := range map[string]string{
		"missing entry":    "x = 1",
		"builtin entry":    "on_bar = buy",
		"wrong signature":  "def on_bar(ctx, missing):\n    return None",
		"syntax":           "def on_bar(ctx)\n    return None",
		"load":             "load('external.star', 'signal')\ndef on_bar(ctx):\n    return signal",
		"unknown API":      "def on_bar(ctx):\n    return ctx.ma(20)",
		"return type":      "def on_bar(ctx):\n    return {'action': 'buy'}",
		"history warmup":   "def on_bar(ctx):\n    return buy('all') if ctx.history('close', 20)[-20] > 0 else None",
		"None arithmetic":  "def on_bar(ctx):\n    return sell('all') if ctx.pnl_pct() + 1 > 1 else None",
		"zero volume":      "def on_bar(ctx):\n    return buy(shares=int(1000 / ctx.volume))",
		"held loss branch": "def on_bar(ctx):\n    if ctx.has_position and ctx.pnl_pct() < 0:\n        return 'sell'\n    return None",
		"mutable global":   "values = []\ndef on_bar(ctx):\n    values.append(ctx.close)\n    return None",
	} {
		t.Run(name, func(t *testing.T) {
			if err := Check(context.Background(), code); err == nil {
				t.Fatal("invalid script passed")
			}
		})
	}
	if err := Check(context.Background(), maCrossScript); err != nil {
		t.Fatal("existing MA template rejected", err)
	}
}

func TestScriptOrdersRejectAmbiguousInvalidAndWrongSideSizes(t *testing.T) {
	for _, order := range []string{"buy()", "buy(pct_of_cash=0)", "buy(pct_of_cash=1.1)", "buy(shares=-1)", "buy(shares=1.5)", "sell(pct_of_cash=0.5)", "buy(pct_of_position=0.5)", "sell(all=False)", "buy('all', shares=100)", "buy(shares=100000001)", "buy(fixed_cash=1000000001)", "buy(fixed_cash=float('inf'))", "buy(pct_of_cash=float('nan'))"} {
		t.Run(order, func(t *testing.T) {
			if err := Check(context.Background(), "def on_bar(ctx):\n    return "+order); err == nil {
				t.Fatal("invalid order admitted")
			}
		})
	}
	for _, order := range []string{"buy('all')", "sell(all=True)", "buy(pct_of_cash=0.5)", "sell(pct_of_position=1)", "buy(shares=100)", "sell(fixed_cash=1000)"} {
		if err := Check(context.Background(), "def on_bar(ctx):\n    return "+order); err != nil {
			t.Fatal(order, err)
		}
	}
}

func TestScriptCompileAndExecutionBudgets(t *testing.T) {
	loop := "def expensive():\n    n = 0\n    for i in range(10000000):\n        n += i\n    return n\n"
	for _, code := range []string{
		loop + "x = expensive()\ndef on_bar(ctx):\n    return None",
		loop + "def on_bar(ctx):\n    expensive()\n    return None",
		"def on_bar(ctx):\n    sum(range(10000000))\n    return None",
	} {
		if err := Check(context.Background(), code); err == nil || (!strings.Contains(err.Error(), "steps") && !strings.Contains(err.Error(), "execution limit")) {
			t.Fatal("expensive script not bounded", err)
		}
	}
	if _, err := Compile(strings.Repeat("#", MaxCodeBytes+1)); err == nil {
		t.Fatal("oversized source accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	program, err := CompileContext(ctx, "def on_bar(ctx):\n    return None")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := CompileContext(ctx, "def on_bar(ctx):\n    return None"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled compilation continued", err)
	}
	if _, err := program.OnBar(&engine.SymbolData{Symbol: "test", Bars: mkBars(10)}, 0, engine.NewPortfolio(1000)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled execution continued", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := Check(ctx, maCrossScript); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("deadline ignored", err)
	}
}
