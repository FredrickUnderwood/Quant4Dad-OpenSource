package application

import (
	"context"
	"github.com/quant4dad/internal/domain"
	"github.com/quant4dad/internal/service"
)

func CatalogWriteToolDefinitions(svc *service.AgentMutationService, pl *service.PipelineService) []ToolDefinition {
	if svc == nil || pl == nil {
		return nil
	}
	defs := []ToolDefinition{}
	for _, update := range []bool{false, true} {
		name, description := "create_strategy", "审批后创建配置或 Starlark 脚本策略。先用 validate_strategy 校验，携带返回的 validation_id 并保持整份定义一致（含描述）；保存时验证凭据并再次进行结构检查及脚本合成试跑；所选行为测试结果通过定义摘要和检查器版本绑定。不会进行真实数据回测或下单。"
		props := map[string]any{"strategy": strategyInputSchema(), "validation_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "description": "Copy validation_id from a successful validate_strategy in this session. It binds the complete normalized definition and checker revision, expires after 30 minutes, and does not replace approval. Revalidate after changing any field."}}
		required := []string{"strategy", "validation_id"}
		if update {
			name = "update_strategy"
			description = "审批后按 expected_version 更新配置或 Starlark 脚本策略，版本冲突拒绝覆盖。先用 validate_strategy 校验，携带返回的 validation_id 并保持整份定义一致（含描述）；保存时验证凭据并再次进行结构检查及脚本合成试跑；所选行为测试结果通过定义摘要和检查器版本绑定。"
			props["id"] = idSchema()
			props["expected_version"] = intSchema(1, 2147483647)
			required = append(required, "id", "expected_version")
		}
		d := readTool(name, description, objectSchema(props, required...), mutationResultSchema(), []string{"strategy_lab"}, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				ID           int64                 `json:"id"`
				Version      int                   `json:"expected_version"`
				Strategy     service.StrategyInput `json:"strategy"`
				ValidationID string                `json:"validation_id"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return svc.Strategy(ctx, in.ID, in.Version, in.Strategy, in.ValidationID)
		})
		d.Risk = "R2"
		defs = append(defs, d)
	}
	for _, update := range []bool{false, true} {
		name, description := "create_pipeline", "审批后仅创建 draft 流水线，不启用自动化。"
		input := pipelineInputSchema()
		props := map[string]any{"pipeline": input}
		required := []string{"pipeline"}
		if update {
			name = "update_pipeline"
			description = "审批后按 expected_version 更新定义并保留启停状态；更新已启用目标需要 R3 审批。"
			props["id"] = idSchema()
			props["expected_version"] = intSchema(1, 2147483647)
			required = append(required, "id", "expected_version")
		}
		d := readTool(name, description, objectSchema(props, required...), mutationResultSchema(), []string{"pipeline_builder"}, func(ctx context.Context, raw []byte) (any, error) {
			var in struct {
				ID       int64                 `json:"id"`
				Version  int                   `json:"expected_version"`
				Pipeline service.PipelineInput `json:"pipeline"`
			}
			if err := decodeToolArgs(raw, &in); err != nil {
				return nil, err
			}
			return svc.Pipeline(ctx, in.ID, in.Version, in.Pipeline)
		})
		d.Risk = "R2"
		if update {
			d.ResolveRisk = func(ctx context.Context, raw []byte) (string, error) {
				var in struct {
					ID       int64                 `json:"id"`
					Version  int                   `json:"expected_version"`
					Pipeline service.PipelineInput `json:"pipeline"`
				}
				if err := decodeToolArgs(raw, &in); err != nil {
					return "", err
				}
				target, err := pl.AgentMetadata(ctx, in.ID)
				if err != nil {
					return "", err
				}
				if target.Version != in.Version {
					return "", domain.ErrResourceConflict
				}
				if target.Status == domain.PipelineStatusEnabled {
					return "R3", nil
				}
				return "R2", nil
			}
		}
		defs = append(defs, d)
	}
	d := readTool("set_pipeline_status", "审批后按 expected_version 启停流水线。enabled 是 R3，disabled 是 R2。", objectSchema(map[string]any{"id": idSchema(), "expected_version": intSchema(1, 2147483647), "status": enumSchema("enabled", "disabled")}, "id", "expected_version", "status"), mutationResultSchema(), []string{"pipeline_builder"}, func(ctx context.Context, raw []byte) (any, error) {
		var in struct {
			ID      int64  `json:"id"`
			Version int    `json:"expected_version"`
			Status  string `json:"status"`
		}
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return svc.Status(ctx, in.ID, in.Version, in.Status)
	})
	d.Risk = "R2"
	d.ResolveRisk = func(_ context.Context, raw []byte) (string, error) {
		var in struct {
			ID      int64  `json:"id"`
			Version int    `json:"expected_version"`
			Status  string `json:"status"`
		}
		if err := decodeToolArgs(raw, &in); err != nil {
			return "", err
		}
		if in.Status == "enabled" {
			return "R3", nil
		}
		return "R2", nil
	}
	defs = append(defs, d)
	return defs
}
func mutationResultSchema() map[string]any {
	return objectSchema(map[string]any{"id": idSchema(), "version": intSchema(1, 2147483647), "status": enumSchema("draft", "disabled", "enabled")}, "id", "version")
}

func SafePipelineToolDefinition(pl *service.PipelineService) ToolDefinition {
	d := readTool("dry_run_pipeline_safe", "预执行未保存的流水线，不落库，不发送邮件或飞书。AI 节点可能调用已配置模型。", objectSchema(map[string]any{"pipeline": pipelineInputSchema(), "sample_event": businessObjectSchema()}, "pipeline", "sample_event"), map[string]any{"type": "object"}, []string{"pipeline_builder"}, func(ctx context.Context, raw []byte) (any, error) {
		var in struct {
			Pipeline service.PipelineInput `json:"pipeline"`
			Sample   map[string]any        `json:"sample_event"`
		}
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		if err := pl.ValidateAgent(in.Pipeline); err != nil {
			return nil, err
		}
		return pl.Preview(ctx, in.Pipeline, in.Sample)
	})
	d.Risk = "R1"
	d.TimeoutMS = 30000
	return d
}
