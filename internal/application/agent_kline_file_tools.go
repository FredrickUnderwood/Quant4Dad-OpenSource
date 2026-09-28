package application

import (
	"context"

	"github.com/quant4dad/internal/service"
)

func AgentKlineFileToolDefinitions(market *service.InstrumentService, files *service.AgentKlineFileService) []ToolDefinition {
	if market == nil || files == nil {
		return nil
	}
	profiles := []string{"research", "strategy_lab"}
	fileID := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	descriptor := objectSchema(map[string]any{
		"file_id": fileID, "file": stringSchema(), "code": stringSchema(), "period": stringSchema(), "count": intSchema(0, 500),
		"first_date": stringSchema(), "last_date": stringSchema(), "data_as_of": stringSchema(), "truncated": map[string]any{"type": "boolean"},
		"expires_at": stringSchema(), "page_size": intSchema(1, 100),
		"price_basis": stringSchema(), "adjustment_status": stringSchema(), "adjustment_source": stringSchema(),
	}, "file_id", "file", "code", "period", "count", "first_date", "last_date", "data_as_of", "truncated", "expires_at", "page_size")
	query := readTool("query_kline", "查询本地 K 线并保存到当前会话的 tmp/kline 临时文件（24 小时有效），只返回文件编号、日期范围和条数。涨幅阈值统计优先用 analyze_kline 一次生成统计表和 K 线图；仅需检查原始行时用 read_kline_file 分段读取。按日期升序保存区间最新 limit 根（默认 120，最多 500），并在可用时保留首根之前的收盘价作为统计基准；truncated=true 表示更早数据未包含，不触发同步。price_basis=stored_ohlc始终表示原始价格；adjustment_status=verified且adjustment_source=tushare.fund_adj表示这些日线因子已接入，前复权分析用execute_python的adjustment=qfq和end_date，不要在脚本中重复复权。", queryKlineInputSchema(), descriptor, profiles, func(ctx context.Context, raw []byte) (any, error) {
		var in service.QueryKlineInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		result, err := market.QueryKline(ctx, in)
		if err != nil {
			return nil, err
		}
		return files.Save(ctx, result)
	})
	query.MaxResult = 4096
	read := readTool("read_kline_file", "分段读取 query_kline 生成的当前会话临时文件。offset 从 0 开始，limit 默认 50、最多 100；按 columns 解释 rows，按 next_offset 继续，has_more=false 表示读完。逐段记录观察，不要在回答中复述全部原始行。文件过期时重新 query_kline。",
		objectSchema(map[string]any{"file_id": fileID, "offset": intSchema(0, 500), "limit": intSchema(1, 100)}, "file_id"),
		objectSchema(map[string]any{"file_id": fileID, "code": stringSchema(), "period": stringSchema(), "offset": intSchema(0, 500), "count": intSchema(0, 100),
			"price_basis": stringSchema(), "adjustment_status": stringSchema(), "adjustment_source": stringSchema(),
			"total": intSchema(0, 500), "next_offset": intSchema(0, 500), "has_more": map[string]any{"type": "boolean"},
			"columns": arraySchema(stringSchema(), 8, 8), "rows": arraySchema(arraySchema(map[string]any{"oneOf": []any{stringSchema(), map[string]any{"type": "number"}}}, 8, 8), 0, 100),
		}, "file_id", "code", "period", "offset", "count", "total", "next_offset", "has_more", "columns", "rows"), profiles,
		func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				FileID string `json:"file_id"`
				Offset int    `json:"offset"`
				Limit  *int   `json:"limit"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			limit := service.KlineFilePageSize
			if in.Limit != nil {
				limit = *in.Limit
			}
			return files.Read(ctx, in.FileID, in.Offset, limit)
		})
	read.MaxResult = 32 << 10
	definitions := []ToolDefinition{query, read, agentAnalyzeKlineTool(files, profiles)}
	if files.PythonAvailable() {
		definitions = append(definitions, agentPythonTool(files, profiles))
	}
	return definitions
}
