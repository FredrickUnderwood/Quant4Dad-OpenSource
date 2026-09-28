package application

import (
	"context"
	"github.com/quant4dad/internal/service"
)

func AgentBacktestToolDefinition(svc *service.AgentMutationService, cost *service.CostService) ToolDefinition {
	input := objectSchema(map[string]any{"strategy_id": idSchema(), "expected_version": intSchema(1, 2147483647), "cost_id": idSchema(), "initial_capital": map[string]any{"type": "number", "minimum": 0.01, "maximum": 1000000000}, "start_date": textSchema(10), "end_date": textSchema(10)}, "strategy_id", "expected_version", "initial_capital", "start_date", "end_date")
	d := readTool("run_backtest", "提交使用固定策略和费用快照的本地回测，支持配置及 Starlark 脚本策略，立即返回 job_id；最多 20 个标的、每个 500 根 K 线、2 分钟计算，脚本逐根执行并受步数预算与取消控制。R1 工具，不需要一次性审批；用户请求回测后可按授权范围提交。不进行真实交易。", input, objectSchema(map[string]any{"job_id": idSchema(), "status": enumSchema("pending")}, "job_id", "status"), []string{"strategy_lab"}, func(ctx context.Context, raw []byte) (any, error) {
		var in service.AgentBacktestInput
		if err := decodeToolArgs(raw, &in); err != nil {
			return nil, err
		}
		return svc.Backtest(ctx, cost, in)
	})
	d.Risk = "R1"
	return d
}
