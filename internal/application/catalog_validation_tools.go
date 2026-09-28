package application

import (
	"context"
	"github.com/quant4dad/internal/expr"
	"github.com/quant4dad/internal/script"
	"github.com/quant4dad/internal/service"
)

func CatalogValidationToolDefinitions(st *service.StrategyService, pl *service.PipelineService, market *service.InstrumentService, ind *service.IndicatorService) []ToolDefinition {
	defs := []ToolDefinition{}
	if st != nil {
		tests := arraySchema(enumSchema(script.MA3CrossUpEntry, script.DailyRoundTrip100), 0, 2)
		tests["uniqueItems"] = true
		tests["description"] = "Only applicable to mode=script (Starlark). Configuration strategies MUST omit this field or use []; never select a script suite merely because configuration rules have similar intent. Optional server-owned behavior suites; select only when they match the user's intent. ma3_cross_up_entry checks flat-position entry on a true close/MA3 upward crossing (previous close <= previous MA3 and current close > current MA3), needs four bars, includes current close in current MA3, and imposes no exit or sizing rule. flat_buy_100_exit_after_one_day checks buying exactly 100 shares when flat, no action on entry day, then selling all after >=1 calendar day including losing positions. Tests use independent synthetic portfolios without fills, not a backtest. Omitted suites mean behavior is unverified."
		defs = append(defs, readTool("validate_strategy", "校验配置或 Starlark 脚本策略，返回结构化检查报告。脚本执行编译及64根行情×4种持仓试跑，报告实际信号数；branch_coverage_measured=false，不能声称覆盖全部分支。仅脚本模式按用户意图选择behavior_tests服务端固定行为测试；配置模式必须省略或传空数组，未选择时明确行为未验证。valid=false时遵守遇错停止要求，不自行修正、重试或保存；仅在用户允许修正时修正后重检。valid=true返回validation_id，创建/更新必须携带并保存完全相同的定义，任何字段变动都需重新校验。凭据有效30分钟，仅本会话使用；不代替R2审批。不会保存策略或执行真实数据回测。", strategyValidationInputSchema(tests), strategyValidationOutputSchema(), []string{"strategy_lab"}, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				Strategy      service.StrategyInput `json:"strategy"`
				BehaviorTests []string              `json:"behavior_tests"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return st.CheckAgent(ctx, in.Strategy, in.BehaviorTests)
		}))
	}
	if pl != nil {
		defs = append(defs, readTool("validate_pipeline", "校验与 create_pipeline/update_pipeline 相同的流水线定义，不含 status；创建时自动为 draft，启停使用 set_pipeline_status。不保存，不运行任何节点。", objectSchema(map[string]any{"pipeline": pipelineInputSchema()}, "pipeline"), objectSchema(map[string]any{"valid": map[string]any{"type": "boolean"}}, "valid"), []string{"pipeline_builder"}, func(_ context.Context, raw []byte) (any, error) {
			var in struct {
				Pipeline service.PipelineInput `json:"pipeline"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			if err := pl.ValidateAgent(in.Pipeline); err != nil {
				return nil, err
			}
			return map[string]any{"valid": true}, nil
		}))
	}
	if market != nil {
		defs = append(defs, readTool("get_data_coverage", "查询本地 K 线的最早/最新日期与总数。日期跨度不表示交易日完整性；不触发同步。", objectSchema(map[string]any{"code": codeSchema(), "period": periodSchema()}, "code"), objectSchema(map[string]any{"code": stringSchema(), "period": periodSchema(), "count": intSchema(0, 9007199254740991), "first_date": stringSchema(), "last_date": stringSchema()}, "code", "period", "count", "first_date", "last_date"), []string{"research", "strategy_lab"}, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				Code   string `json:"code"`
				Period string `json:"period"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return market.DataCoverage(ctx, in.Code, in.Period)
		}))
	}
	if ind != nil {
		defs = append(defs, readTool("list_indicators", "一次返回全部已注册指标：count 是指标总数，items 包含各指标的参数与输出定义；无分页。", objectSchema(map[string]any{}), objectSchema(map[string]any{"count": map[string]any{"type": "integer", "minimum": 0}, "items": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "count", "items"), []string{"strategy_lab"}, func(_ context.Context, raw []byte) (any, error) {
			var in struct{}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return indicatorListData(ind.List()), nil
		}))
	}
	return defs
}

// Keep the product indicator API unchanged; the Agent list carries an explicit
// count and always serializes items as an array, including an empty registry.
func indicatorListData(items []map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"count": len(items), "items": items}
}
func textSchema(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
func arraySchema(items map[string]any, min, max int) map[string]any {
	return map[string]any{"type": "array", "items": items, "minItems": min, "maxItems": max}
}
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func businessObjectSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "maxProperties": 128}
}
func strategyValidationInputSchema(tests map[string]any) map[string]any {
	// Keep the root object shape for model providers, with disjoint mode branches.
	root := objectSchema(map[string]any{"strategy": strategyInputSchema(), "behavior_tests": tests}, "strategy")
	config, scriptInput := strategyInputSchema(), strategyInputSchema()
	for i, input := range []map[string]any{config, scriptInput} {
		props := input["properties"].(map[string]any)
		props["body"] = props["body"].(map[string]any)["oneOf"].([]any)[i]
	}
	root["oneOf"] = []any{
		objectSchema(map[string]any{"strategy": config, "behavior_tests": arraySchema(stringSchema(), 0, 0)}, "strategy"),
		objectSchema(map[string]any{"strategy": scriptInput, "behavior_tests": tests}, "strategy"),
	}
	return root
}

func strategyInputSchema() map[string]any {
	size := map[string]any{"oneOf": []any{map[string]any{"type": "string", "const": "all"}, objectSchema(map[string]any{"pct_of_cash": map[string]any{"type": "number", "minimum": 0, "maximum": 1}, "pct_of_position": map[string]any{"type": "number", "minimum": 0, "maximum": 1}, "shares": intSchema(1, 100000000), "fixed_cash": map[string]any{"type": "number", "minimum": 0, "maximum": 1000000000}})}}
	indicator := objectSchema(map[string]any{"alias": textSchema(64), "type": textSchema(64), "params": map[string]any{"type": "object", "additionalProperties": true, "maxProperties": 16, "description": "Use only parameters declared in list_indicators parameter_schema.properties for the selected indicator type. Required fields and defaults come from that same schema; do not transfer parameters or defaults between indicator types. Unknown parameters are rejected."}}, "alias", "type", "params")
	rule := objectSchema(map[string]any{"name": textSchema(128), "when": expr.Schema(), "then": objectSchema(map[string]any{"action": enumSchema("buy", "sell"), "size": size}, "action", "size")}, "name", "when", "then")
	execution := objectSchema(map[string]any{"fill_at": enumSchema("next_open", "close")}, "fill_at")
	configBody := objectSchema(map[string]any{"mode": enumSchema("config"), "indicators": arraySchema(indicator, 0, 32), "rules": arraySchema(rule, 0, 32), "execution": execution}, "indicators", "rules", "execution")
	code := textSchema(script.MaxCodeBytes)
	code["minLength"] = 1
	code["description"] = "Starlark source, at most 16384 UTF-8 bytes; not Python. Define def on_bar(ctx) with one positional parameter, returning buy(...), sell(...) or None. ctx exposes index (0-based), date, open/high/low/close/volume, has_position, entry_price (0 when flat), days_held (calendar days), history(field,n) (oldest first, includes current bar, may be shorter than n), pnl_pct() (fraction or None when flat). history fields: open/high/low/close/volume. sum(iterable) is available. shares counts individual shares (股). Orders take exactly one size: buy/sell(\"all\"), buy(pct_of_cash=0.5), sell(pct_of_position=0.5), shares=100 or fixed_cash=10000; sizes must be finite and positive, fractions <=1, integer shares <=100000000, fixed_cash <=1000000000. No IO, network, imports/load, mutable global state or recursion. Compilation and each on_bar call have a 100000-step limit. Guard history length and None, compute indicators in the script, and prefer next_open execution."
	// Stored StrategyBody values serialize unused slices as null; permit that
	// representation for read/update round trips while rejecting mixed logic.
	empty := map[string]any{"anyOf": []any{arraySchema(map[string]any{"type": "object"}, 0, 0), map[string]any{"type": "null"}}}
	scriptBody := objectSchema(map[string]any{"mode": enumSchema("script"), "lang": enumSchema("starlark"), "code": code, "indicators": empty, "rules": empty, "execution": execution}, "mode", "code", "execution")
	body := map[string]any{"oneOf": []any{configBody, scriptBody}}
	return objectSchema(map[string]any{"name": textSchema(128), "description": textSchema(4096), "universe": arraySchema(codeSchema(), 1, 20), "period": periodSchema(), "body": body}, "name", "universe", "period", "body")
}
func pipelineInputSchema() map[string]any {
	node := objectSchema(map[string]any{"node_key": textSchema(64), "type": textSchema(64), "name": textSchema(128), "config": businessObjectSchema(), "pos_x": intSchema(-100000, 100000), "pos_y": intSchema(-100000, 100000)}, "node_key", "type", "config")
	edge := objectSchema(map[string]any{"from_node_key": textSchema(64), "to_node_key": textSchema(64), "condition": map[string]any{"anyOf": []any{businessObjectSchema(), map[string]any{"type": "null"}}}}, "from_node_key", "to_node_key")
	return objectSchema(map[string]any{"name": textSchema(128), "description": textSchema(4096), "nodes": arraySchema(node, 1, 64), "edges": arraySchema(edge, 0, 128), "sources": arraySchema(textSchema(64), 0, 32)}, "name", "nodes", "edges")
}

func strategyValidationOutputSchema() map[string]any {
	diagnostic := objectSchema(map[string]any{
		"code": stringSchema(), "phase": enumSchema("compile", "synthetic_smoke", "behavior"), "severity": enumSchema("error", "warning"), "path": stringSchema(), "message": stringSchema(),
		"line": intSchema(1, 16384), "column": intSchema(1, 16384), "bar_index": intSchema(0, 511), "position_state": stringSchema(),
	}, "code", "phase", "severity", "path", "message")
	// String and object are disjoint. oneOf preserves the accepted values and
	// is supported by the pinned DSH output-schema subset (anyOf is not).
	size := map[string]any{"oneOf": []any{enumSchema("all"), objectSchema(map[string]any{"shares": intSchema(1, 100000000), "fixed_cash": map[string]any{"type": "number", "exclusiveMinimum": 0}, "pct_of_cash": map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1}, "pct_of_position": map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1}})}}
	signal := objectSchema(map[string]any{"action": enumSchema("buy", "sell", "none"), "size": size}, "action")
	behaviorCase := objectSchema(map[string]any{"suite": stringSchema(), "case": stringSchema(), "passed": map[string]any{"type": "boolean"}, "expected": signal, "actual": signal, "error": stringSchema()}, "suite", "case", "passed", "expected")
	report := objectSchema(map[string]any{
		"checker_revision": stringSchema(), "compile": enumSchema("passed", "failed", "not_run"),
		"smoke":                    objectSchema(map[string]any{"status": enumSchema("passed", "failed", "not_run"), "bars": intSchema(64, 64), "position_states": arraySchema(stringSchema(), 4, 4), "attempted": intSchema(0, 256), "passed": intSchema(0, 256), "signals": objectSchema(map[string]any{"buy": intSchema(0, 256), "sell": intSchema(0, 256), "none": intSchema(0, 256)}, "buy", "sell", "none")}, "status", "bars", "position_states", "attempted", "passed", "signals"),
		"behavior":                 objectSchema(map[string]any{"status": enumSchema("passed", "failed", "not_requested", "not_run"), "suites": arraySchema(stringSchema(), 0, 2), "cases": arraySchema(behaviorCase, 0, 11)}, "status", "suites", "cases"),
		"branch_coverage_measured": map[string]any{"type": "boolean", "const": false}, "diagnostics": arraySchema(diagnostic, 0, 16),
	}, "checker_revision", "compile", "smoke", "behavior", "branch_coverage_measured", "diagnostics")
	contract := objectSchema(map[string]any{"id": stringSchema(), "mode": enumSchema("script"), "entry": stringSchema(), "sizing": stringSchema(), "exit": stringSchema()}, "id", "mode", "entry", "sizing", "exit")
	return objectSchema(map[string]any{"mode": enumSchema("config", "script"), "behavior_contracts": arraySchema(contract, 0, 2), "valid": map[string]any{"type": "boolean"}, "errors": arraySchema(objectSchema(map[string]any{"path": stringSchema(), "message": stringSchema()}, "path", "message"), 0, 32), "checks": arraySchema(stringSchema(), 0, 8), "warnings": arraySchema(stringSchema(), 0, 8), "checker_revision": stringSchema(), "definition_digest": stringSchema(), "script_report": report, "validation_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}, "validation_expires_at": stringSchema()}, "valid", "errors", "checks", "warnings", "checker_revision")
}
