import { useEffect, useState } from 'react';
import { agentAPI } from './api';
import type { AgentApproval, LiveTool } from './types';
import { ToolResult } from './ToolResult';

export const toolLabels: Record<string, string> = {
  execute_python: 'Python 行情研究',
  query_kline: '准备 K 线数据', read_kline_file: '分段读取 K 线', analyze_kline: '行情统计与 K 线图', latest_bar_date: '查询最新行情', get_instrument: '查询标的信息', list_instruments: '检索标的', get_data_coverage: '查询数据覆盖范围',
  list_indicators: '查询指标', list_strategies: '查询策略列表', get_strategy: '读取策略', validate_strategy: '校验策略', create_strategy: '创建策略', update_strategy: '更新策略',
  list_cost_models: '查询费用模型', run_backtest: '提交回测', list_backtests: '查询回测任务', get_backtest_job: '读取回测状态', get_backtest_report: '读取回测报告',
  list_news: '查询资讯', get_news: '读取资讯', list_events: '查询事件', get_event: '读取事件', get_pipeline_node_types: '查询节点类型', list_pipelines: '查询流水线',
  get_pipeline: '读取流水线', validate_pipeline: '校验流水线', create_pipeline: '创建草稿流水线', update_pipeline: '更新流水线', dry_run_pipeline_safe: '安全预执行', set_pipeline_status: '启停流水线',
};
const statuses = { proposed: '待执行', waiting_approval: '等待审批', started: '执行中', completed: '已完成', failed: '未完成', unknown: '结果待核对' };
const reasons: Record<string, string> = { agent_run_budget_exceeded: '本轮调用预算不足，工具未执行。', agent_capability_expired: '本轮运行已超时。', agent_configuration_unavailable: '运行配置已变化。', agent_approval_denied: '已拒绝本次调用，对话可以继续。', agent_approval_timeout: '审批已超时。', agent_tool_outcome_unknown: '执行结果无法确认，不会自动重复执行。', agent_resource_version_conflict: '目标内容已变化，请重新读取后提交。', agent_tool_invalid_arguments: '调用参数未通过校验，请修正后再试。' };

export function ToolCard({ tool, runID, terminal }: { tool: LiveTool; runID: string; terminal: boolean }) {
  const [expanded, setExpanded] = useState(['analyze_kline', 'execute_python'].includes(tool.name.replace(/^mcp__q4d__/, '')));
  const [approval, setApproval] = useState<AgentApproval>(), [error, setError] = useState(''), [busy, setBusy] = useState(false), [checked, setChecked] = useState(false);
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (!tool.approval || terminal || tool.status !== 'waiting_approval') return;
    const abort = new AbortController(); let pending = false;
    const poll = async () => {
      setNow(Date.now()); if (pending) return; pending = true;
      try {
        const value = await agentAPI.approval(tool.approval!.id, abort.signal);
        if (value.run_id !== runID || value.tool_call_id !== tool.id || value.arguments_hash !== tool.approval!.hash || value.risk !== tool.approval!.risk || value.tool_name !== tool.name) throw new Error('审批记录与本次调用不一致。');
        if (!abort.signal.aborted) { setApproval(value); setError(''); }
      } catch (e) { if (!abort.signal.aborted) { setApproval(undefined); setError((e as Error).message); } }
      finally { pending = false; }
    };
    void poll(); const timer = setInterval(poll, 2000);
    return () => { abort.abort(); clearInterval(timer); };
  }, [tool.id, tool.approval?.id, runID, terminal, tool.status]);
  const decide = async (decision: 'allow_once' | 'reject') => {
    if (!approval) return;
    setBusy(true); setError('');
    try { const result = await agentAPI.decide(approval.id, decision); setApproval(result.approval); }
    catch (e) { setError((e as Error).message); }
    finally { setBusy(false); }
  };
  const waiting = tool.status === 'waiting_approval' && !terminal;
  const expired = now >= Date.parse(approval?.expires_at ?? tool.approval?.expires ?? '');
  const canDecide = waiting && approval && !expired && ['pending', 'approved', 'rejected'].includes(approval.status);
  const running = !terminal && ['proposed', 'started'].includes(tool.status);
  return <div className="assistant-tool-card" data-running={running}>
    <button type="button" className="assistant-tool-head" aria-expanded={expanded || waiting} onClick={() => setExpanded(value => !value)}>
      <span className={`assistant-tool-icon ${running ? 'is-running' : ''}`} aria-hidden="true">{running ? '' : tool.status === 'completed' ? '✓' : tool.status === 'failed' ? '!' : '◇'}</span>
      <span>{toolLabels[tool.name.replace(/^mcp__q4d__/, '')] ?? tool.name}</span><span className="assistant-tool-status">{terminal && ['proposed', 'started', 'waiting_approval'].includes(tool.status) ? '已中断' : statuses[tool.status]}</span><span className="assistant-tool-chevron" aria-hidden="true">{expanded || waiting ? '⌄' : '›'}</span>
    </button>
    {(expanded || waiting) && <div className="assistant-tool-details">
    {tool.arguments && <details open={waiting}><summary>调用参数</summary><pre>{tool.arguments}</pre></details>}
    {tool.argumentsOmitted && <p className="muted">参数未完整展示{waiting ? '，请拒绝后缩小修改范围再提交' : ''}。</p>}
    {['completed', 'failed', 'unknown'].includes(tool.status) && <ToolResult tool={tool} runID={runID} />}
    {waiting && <div className="assistant-approval">
      <p>{tool.approval?.risk === 'R3' ? '此操作会启用自动化，或修改正在运行的流水线。请核对完整参数。' : '此操作会保存或修改数据，请核对完整参数。'}</p>
      <p className="muted">{expired ? '审批已过期' : approval?.status === 'approved' ? '已允许一次，等待执行' : approval?.status === 'rejected' ? '已拒绝，等待对话继续' : '尚未执行'} · {tool.approval?.expires ? new Date(tool.approval.expires).toLocaleTimeString() : ''}</p>
      {tool.approval?.risk === 'R3' && approval?.status === 'pending' && <label><input type="checkbox" checked={checked} onChange={e => setChecked(e.target.checked)} /> 我已核对参数和自动化影响</label>}
      <div className="assistant-actions">
        <button disabled={!canDecide || busy || tool.argumentsOmitted || approval?.status === 'rejected' || (tool.approval?.risk === 'R3' && approval?.status === 'pending' && !checked)} onClick={() => decide('allow_once')}>{approval?.status === 'approved' ? '重试送达批准' : '允许一次'}</button>
        <button className="secondary" disabled={!canDecide || busy || approval?.status === 'approved'} onClick={() => decide('reject')}>{approval?.status === 'rejected' ? '重试送达拒绝' : '拒绝本次调用'}</button>
      </div>
      {error && <p className="error" role="alert">{error}</p>}
    </div>}
    </div>}
    {tool.code && <p className="assistant-tool-note">{reasons[tool.code] ?? '本次调用未完成，详情可在会话回复中查看。'}</p>}
  </div>;
}
