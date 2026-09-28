package application

import (
	"context"
	"unicode/utf8"

	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/repository"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/strictjson"
)

var marketProfiles = []string{"external", "research", "strategy_lab"}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	result := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func intSchema(min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}
func decodeToolArgs(args []byte, target any) error {
	if len(args) == 0 {
		args = []byte("{}")
	}
	if strictjson.DecodeNullable(args, target, 64<<10) != nil {
		return service.ErrToolInput
	}
	return nil
}
func codeSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": `^(sh|sz|bj)\.[0-9]{6}$`}
}
func periodSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"1d", "1w", "1mo"}, "default": "1d"}
}
func barSchema() map[string]any {
	properties := map[string]any{"code": stringSchema(), "period": stringSchema(), "date": stringSchema()}
	properties["adj_source"] = stringSchema()
	required := []string{"code", "period", "date"}
	for _, name := range []string{"open", "high", "low", "close", "volume", "amount", "adj_factor"} {
		properties[name] = map[string]any{"type": "number"}
		required = append(required, name)
	}
	return objectSchema(properties, required...)
}
func queryKlineInputSchema() map[string]any {
	return objectSchema(map[string]any{"code": codeSchema(), "period": periodSchema(), "start": map[string]any{"type": "string", "maxLength": 10}, "end": map[string]any{"type": "string", "maxLength": 10}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "default": 120}}, "code")
}
func MarketToolDefinitions(svc *service.InstrumentService) []ToolDefinition {
	if svc == nil {
		return nil
	}
	return []ToolDefinition{
		readTool("query_kline", "查询本地股票及已同步 ETF 的 K 线，按日期升序返回区间内最新 120 根，最多 500 根。数据不会触发同步；truncated 表示更早数据未返回。",
			queryKlineInputSchema(),
			objectSchema(map[string]any{"code": stringSchema(), "period": stringSchema(), "count": intSchema(0, 500), "bars": map[string]any{"type": "array", "maxItems": 500, "items": barSchema()}, "truncated": map[string]any{"type": "boolean"}, "data_as_of": stringSchema()}, "code", "period", "count", "bars", "truncated", "data_as_of"), marketProfiles,
			func(ctx context.Context, args []byte) (any, error) {
				var in service.QueryKlineInput
				if err := decodeToolArgs(args, &in); err != nil {
					return nil, err
				}
				result, err := svc.QueryKline(ctx, in)
				if err != nil {
					return nil, err
				}
				// Internal analysis baselines stay in the owned snapshot; preserve
				// the external MCP's bounded, versioned query response contract.
				return map[string]any{"code": result.Code, "period": result.Period, "count": result.Count, "bars": result.Bars, "truncated": result.Truncated, "data_as_of": result.DataAsOf}, nil
			}),
		readTool("latest_bar_date", "查询本地已入库的最新 K 线，不加载完整历史。",
			objectSchema(map[string]any{"code": codeSchema(), "period": periodSchema()}, "code"),
			objectSchema(map[string]any{"code": stringSchema(), "period": stringSchema(), "exists": map[string]any{"type": "boolean"}, "latest_date": stringSchema(), "latest_bar": barSchema()}, "code", "period", "exists"), marketProfiles,
			func(ctx context.Context, args []byte) (any, error) {
				var in struct {
					Code   string `json:"code"`
					Period string `json:"period"`
				}
				if err := decodeToolArgs(args, &in); err != nil {
					return nil, err
				}
				bar, err := svc.LatestBar(ctx, in.Code, in.Period)
				if err != nil {
					return nil, err
				}
				if in.Period == "" {
					in.Period = "1d"
				}
				result := map[string]any{"code": in.Code, "period": in.Period, "exists": bar != nil}
				if bar != nil {
					result["latest_date"] = bar.Date.Format("2006-01-02")
					result["latest_bar"] = bar
				}
				return result, nil
			}),
		readTool("list_instruments", "分页检索本地股票及已同步 ETF 的代码与名称，asset_type 区分股票和 ETF；默认 20 条，最多 100 条。",
			objectSchema(map[string]any{"keyword": map[string]any{"type": "string", "maxLength": 128}, "asset_type": map[string]any{"type": "string", "enum": []string{"stock", "etf"}}, "page": intSchema(1, 10000), "size": intSchema(1, 100)}),
			objectSchema(map[string]any{"total": map[string]any{"type": "integer"}, "count": intSchema(0, 100), "page": intSchema(1, 10000), "size": intSchema(1, 100), "has_more": map[string]any{"type": "boolean"}, "items": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "object"}}}, "total", "count", "page", "size", "has_more", "items"), marketProfiles,
			func(ctx context.Context, args []byte) (any, error) {
				var in struct {
					AssetType domain.AssetType `json:"asset_type"`
					Keyword   string           `json:"keyword"`
					Page      *int             `json:"page"`
					Size      *int             `json:"size"`
				}
				if err := decodeToolArgs(args, &in); err != nil {
					return nil, err
				}
				page, size := 1, 20
				if in.Page != nil {
					page = *in.Page
				}
				if in.Size != nil {
					size = *in.Size
				}
				if page < 1 || page > 10000 || size < 1 || size > 100 || utf8.RuneCountInString(in.Keyword) > 128 ||
					(in.AssetType != "" && in.AssetType != domain.AssetStock && in.AssetType != domain.AssetETF) {
					return nil, service.ErrToolInput
				}
				items, total, err := svc.List(ctx, repository.ListInstrumentsFilter{Keyword: in.Keyword, AssetType: in.AssetType, Page: page, Size: size})
				if err != nil {
					return nil, err
				}
				return map[string]any{"total": total, "count": len(items), "items": items, "page": page, "size": size, "has_more": int64(page*size) < total}, nil
			}),
		readTool("get_instrument", "按精确代码查询本地股票或已同步 ETF 的元信息。", objectSchema(map[string]any{"code": codeSchema()}, "code"), map[string]any{"type": "object"}, marketProfiles,
			func(ctx context.Context, args []byte) (any, error) {
				var in struct {
					Code string `json:"code"`
				}
				if err := decodeToolArgs(args, &in); err != nil {
					return nil, err
				}
				if !service.ValidInstrumentCode(in.Code) {
					return nil, service.ErrToolInput
				}
				return svc.GetByCode(ctx, in.Code)
			}),
	}
}
