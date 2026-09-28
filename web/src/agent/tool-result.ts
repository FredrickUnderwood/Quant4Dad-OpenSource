import type { AgentToolResult } from './types.ts';

export function resultText(value: AgentToolResult): string {
  const text = JSON.stringify(value.result?.data, null, 2) ?? '';
  return text.length <= 131072 ? text : text.slice(0, 131072) + '\n（展示已截断）';
}

// Links are constructed from fixed product routes and safe integer IDs. Tool
// output remains escaped text; returned URLs/HTML never become navigation.
export function resultLink(value: AgentToolResult): { href: string; label: string } | undefined {
  if (value.status !== 'succeeded' || !value.result?.untrusted_data || typeof value.result.data !== 'object' || value.result.data === null) return;
  const data = value.result.data as Record<string, unknown>;
  const id = value.tool_name === 'run_backtest' ? data.job_id : data.id;
  if (typeof id !== 'number' || !Number.isSafeInteger(id) || id < 1) return;
  switch (value.tool_name) {
    case 'create_strategy': case 'update_strategy': return { href: `/strategies/${id}/edit`, label: '查看策略' };
    case 'create_pipeline': case 'update_pipeline': case 'set_pipeline_status': return { href: `/pipelines/${id}/edit`, label: '查看流水线' };
    case 'run_backtest': return { href: `/backtests/${id}`, label: '查看回测任务' };
  }
}

export function resultFailureText(value: AgentToolResult): string {
  if (value.execution_stage === 'pre_dispatch') {
    return value.error_code === 'agent_tool_context_or_arguments_rejected'
      ? '执行前校验未通过，工具未执行。'
      : '请求在执行前被拒绝，工具未执行。';
  }
  return value.error_code === 'tool_outcome_unknown' ? '结果无法确认，不会自动重复执行。' : '本次调用未完成。';
}
