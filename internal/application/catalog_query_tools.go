package application

import (
	"context"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/service"
)

func CatalogQueryToolDefinitions(svc *service.AgentCatalogQueryService) []ToolDefinition {
	if svc == nil {
		return nil
	}
	newsTimeDescription := "published_at 是库中保存的发布时间字段；结果未返回入库时间或该记录时间的来源，不能仅凭年份或排序推断时间来源、原因或事件实际发生时间。"
	defs := []ToolDefinition{}
	for _, spec := range []struct {
		kind, description string
		profiles          []string
	}{
		{"strategies", "分页读取策略摘要，详情使用 get_strategy。", []string{"strategy_lab"}},
		{"cost_models", "分页读取本地回测费用模型。", []string{"strategy_lab"}},
		{"backtests", "分页读取回测任务状态，不返回完整交易或净值序列。", []string{"strategy_lab"}},
		{"news", "按 id 降序分页读取已存储资讯摘要，不保证发布时间顺序，不触发外部采集。count 是本页条数，不是页数或全库总数；分页次数依据实际列表调用。has_more 表示仍有更小 id 的记录。" + newsTimeDescription, []string{"research", "pipeline_builder"}},
	} {
		defs = append(defs, readTool("list_"+spec.kind, spec.description, pageInputSchema(), map[string]any{"type": "object"}, spec.profiles, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				Limit  *int  `json:"limit"`
				Before int64 `json:"before_id"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			limit := 20
			if in.Limit != nil {
				limit = *in.Limit
			}
			if limit < 1 || limit > 100 || in.Before < 0 {
				return nil, service.ErrToolInput
			}
			value, err := svc.List(ctx, spec.kind, in.Before, limit+1)
			if err != nil {
				return nil, err
			}
			// Normalize driver scalar types before interpreting the keyset cursor.
			data, err := sonic.Marshal(value)
			if err != nil {
				return nil, service.ErrToolUnavailable
			}
			var items []map[string]any
			if sonic.Unmarshal(data, &items) != nil {
				return nil, service.ErrToolUnavailable
			}
			more := len(items) > limit
			if more {
				items = items[:limit]
			}
			var next any = int64(0)
			if more {
				next = items[len(items)-1]["id"]
			}
			return map[string]any{"items": items, "count": len(items), "has_more": more, "next_before_id": next}, nil
		}))
	}
	for _, spec := range []struct {
		kind, description string
		profiles          []string
	}{
		{"strategy", "读取策略定义及 version；写入必须携带该版本。", []string{"strategy_lab"}},
		{"news", "读取资讯正文，最多 8192 字符，content_truncated 标识截断。" + newsTimeDescription, []string{"research", "pipeline_builder"}},
		{"event", "读取流水线事件摘要与最多 8192 字符结果正文，不读取模型提示词或内部错误。", []string{"research", "pipeline_builder"}},
		{"backtest_job", "读取回测任务状态，不返回内部错误或堆栈。", []string{"strategy_lab"}},
		{"backtest_report", "读取回测指标摘要，完整交易与净值请查看产品回测页面。", []string{"strategy_lab"}},
	} {
		defs = append(defs, readTool("get_"+spec.kind, spec.description, objectSchema(map[string]any{"id": idSchema()}, "id"), map[string]any{"type": "object"}, spec.profiles, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				ID int64 `json:"id"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return svc.Get(ctx, spec.kind, in.ID)
		}))
	}
	return defs
}
func idSchema() map[string]any { return intSchema(1, 9007199254740991) }
func pageInputSchema() map[string]any {
	return objectSchema(map[string]any{"limit": intSchema(1, 100), "before_id": idSchema()})
}
