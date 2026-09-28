package application

import (
	"context"
	"github.com/quant4dad/internal/service"
)

func PipelineReadToolDefinitions(svc *service.PipelineService) []ToolDefinition {
	if svc == nil {
		return nil
	}
	profiles := []string{"external", "pipeline_builder"}
	return []ToolDefinition{
		readTool("list_pipelines", "分页查询流水线摘要，默认最新 20 条，最多 100 条。详情使用 get_pipeline。",
			objectSchema(map[string]any{"limit": intSchema(1, 100), "before_id": map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}}),
			objectSchema(map[string]any{"count": intSchema(0, 100), "items": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"type": "object"}}, "has_more": map[string]any{"type": "boolean"}, "next_before_id": map[string]any{"type": "integer"}}, "count", "items", "has_more", "next_before_id"), profiles,
			func(ctx context.Context, raw []byte) (any, error) {
				var in struct {
					Limit    *int   `json:"limit"`
					BeforeID *int64 `json:"before_id"`
				}
				if err := decodeToolArgs(raw, &in); err != nil {
					return nil, err
				}
				limit := 20
				before := int64(0)
				if in.Limit != nil {
					limit = *in.Limit
				}
				if in.BeforeID != nil {
					before = *in.BeforeID
					if before < 1 {
						return nil, service.ErrToolInput
					}
				}
				if limit < 1 || limit > 100 || before > 9007199254740991 {
					return nil, service.ErrToolInput
				}
				items, err := svc.ListPage(ctx, before, limit+1)
				if err != nil {
					return nil, err
				}
				more := len(items) > limit
				if more {
					items = items[:limit]
				}
				next := int64(0)
				if more {
					next = items[len(items)-1].ID
				}
				return map[string]any{"count": len(items), "items": items, "has_more": more, "next_before_id": next}, nil
			}),
		readTool("get_pipeline", "读取流水线完整定义，不执行节点或触达外部系统。", objectSchema(map[string]any{"id": map[string]any{"type": "integer", "minimum": 1, "maximum": 9007199254740991}}, "id"), map[string]any{"type": "object"}, profiles,
			func(ctx context.Context, raw []byte) (any, error) {
				var in struct {
					ID int64 `json:"id"`
				}
				if err := decodeToolArgs(raw, &in); err != nil {
					return nil, err
				}
				if in.ID < 1 || in.ID > 9007199254740991 {
					return nil, service.ErrToolInput
				}
				return svc.GetAgent(ctx, in.ID)
			}),
		readTool("get_pipeline_node_types", "读取已注册节点类型及配置 schema。", objectSchema(map[string]any{}), map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, profiles,
			func(_ context.Context, raw []byte) (any, error) {
				var in struct{}
				if err := decodeToolArgs(raw, &in); err != nil {
					return nil, err
				}
				return svc.NodeTypes(), nil
			}),
	}
}
