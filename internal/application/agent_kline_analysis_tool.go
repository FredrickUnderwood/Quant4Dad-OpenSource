package application

import (
	"context"

	"github.com/quant4dad/internal/service"
)

func agentAnalyzeKlineTool(files *service.AgentKlineFileService, profiles []string) ToolDefinition {
	fileID := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	date := map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}
	metric := map[string]any{"type": "string", "enum": []string{"close_return", "high_return"}}
	comparison := map[string]any{"type": "string", "enum": []string{"gte", "gt", "lte", "lt"}}
	number := map[string]any{"type": "number"}
	boolean := map[string]any{"type": "boolean"}
	rowValue := map[string]any{"oneOf": []any{stringSchema(), number}}
	table := func(columns int) map[string]any {
		return objectSchema(map[string]any{"columns": arraySchema(stringSchema(), columns, columns), "rows": arraySchema(arraySchema(rowValue, columns, columns), 0, 500)}, "columns", "rows")
	}
	chart := table(6)
	chart["properties"].(map[string]any)["kind"] = map[string]any{"type": "string", "const": "candlestick_v1"}
	chart["required"] = []string{"kind", "columns", "rows"}
	properties := map[string]any{
		"kind": map[string]any{"type": "string", "const": "kline_analysis_v1"}, "file_id": fileID,
		"code": stringSchema(), "period": stringSchema(), "requested_start": stringSchema(), "requested_end": stringSchema(),
		"first_date": stringSchema(), "last_date": stringSchema(), "data_as_of": stringSchema(),
		"truncated": boolean, "coverage_verified": map[string]any{"type": "boolean", "const": false},
		"price_basis":       map[string]any{"type": "string", "const": "stored_ohlc"},
		"adjustment_status": map[string]any{"type": "string", "const": "unverified"},
		"volume_unit":       map[string]any{"type": "string", "const": "unspecified"},
		"metric":            metric, "comparison": comparison, "threshold_pct": number,
		"bar_count": intSchema(0, 500), "eligible_count": intSchema(0, 500), "excluded_count": intSchema(0, 500), "matched_count": intSchema(0, 500),
		"match_rate_pct": map[string]any{"oneOf": []any{number, map[string]any{"type": "null"}}},
		"warnings":       arraySchema(stringSchema(), 0, 10), "matches": table(5), "chart": chart,
	}
	required := []string{"kind", "file_id", "code", "period", "requested_start", "requested_end", "first_date", "last_date", "data_as_of", "truncated", "coverage_verified", "price_basis", "adjustment_status", "volume_unit", "metric", "comparison", "threshold_pct", "bar_count", "eligible_count", "excluded_count", "matched_count", "match_rate_pct", "warnings", "matches", "chart"}
	return readTool("analyze_kline", "一次分析 query_kline 文件的全部所选 K 线，确定性计算涨跌幅阈值命中表并生成带命中标记的 K 线/成交量图。默认 threshold_pct=4、comparison=gte(大于等于)、metric=close_return；gt 表示严格大于，lte/lt 表示小于等于/严格小于；下跌至少4%用 threshold_pct=-4、comparison=lte，下跌超过4%用 lt。close_return=(本根收盘/上一条可用收盘-1)*100，high_return 改用本根最高价，均为库中价格而非官方或复权涨跌幅。start_date/end_date 含首尾；有前置收盘价才计入分母。无需先分页读原始行，结果包含全部命中。必须报告缺失前收、截断、实际日期及未核验覆盖/复权限制；图表与统计由同一份数据生成。",
		objectSchema(map[string]any{"file_id": fileID, "start_date": date, "end_date": date, "threshold_pct": map[string]any{"type": "number", "minimum": -100, "maximum": 1000}, "comparison": comparison, "metric": metric}, "file_id"),
		objectSchema(properties, required...), profiles,
		func(ctx context.Context, raw []byte) (any, error) {
			var in service.AnalyzeKlineInput
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return files.Analyze(ctx, in)
		})
}
