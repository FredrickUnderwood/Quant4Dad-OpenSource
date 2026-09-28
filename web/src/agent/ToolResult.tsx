import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { agentAPI } from './api';
import type { AgentToolResult, LiveTool } from './types';
import { resultFailureText, resultLink, resultText } from './tool-result.ts';
import { klineAnalysisResult } from './kline-analysis.ts';
import { KlineAnalysis } from './KlineAnalysis';
import { pythonAnalysisResult } from './python-analysis';
import { PythonAnalysis } from './PythonAnalysis';

export function ToolResult({ tool, runID }: { tool: LiveTool; runID: string }) {
  const [value, setValue] = useState<AgentToolResult>(), [error, setError] = useState(''), [attempt, setAttempt] = useState(0), [busy, setBusy] = useState(false);
  useEffect(() => {
    const abort = new AbortController();
    setBusy(true); setError(''); setValue(undefined);
    agentAPI.toolResult(runID, tool.id, abort.signal).then(result => {
      if (result.run_id !== runID || result.tool_call_id !== tool.id || result.tool_name !== tool.name.replace(/^mcp__q4d__/, '')) throw new Error('工具结果与本次调用不一致。');
      if (!abort.signal.aborted) setValue(result);
    }).catch(e => { if (!abort.signal.aborted) setError((e as Error).message); }).finally(() => { if (!abort.signal.aborted) setBusy(false); });
    return () => abort.abort();
  }, [tool.id, tool.name, tool.status, runID, attempt]);
  const link = value && resultLink(value);
  const analysis = value && klineAnalysisResult(value);
  const python = value && pythonAnalysisResult(value);
  const result = value?.result?.data as { valid?: boolean; errors?: Array<{ path: string; message: string }> } | undefined;
  const validationFailed = value?.tool_name === 'validate_strategy' && result?.valid === false;
  return <div className="assistant-tool-result">
    {busy && <p className="muted">正在读取工具记录…</p>}
    {analysis && <KlineAnalysis analysis={analysis} />}
    {python && <PythonAnalysis analysis={python} />}
    {validationFailed && <div className="assistant-tool-note"><p>策略校验未通过，需要修正：</p><ul>{result?.errors?.slice(0, 32).map((issue, i) => <li key={i}>{issue.path}：{issue.message}</li>)}</ul></div>}
    {value?.result && <details><summary>查看执行结果</summary><pre>{resultText(value)}</pre></details>}
    {link && <Link to={link.href}>{link.label}</Link>}
    {value?.status === 'expired' && <p className="muted">结果内容已超过保留期，执行记录仍保留。</p>}
    {value?.status === 'failed' && <p className="muted">执行记录：{resultFailureText(value)}</p>}
    {value && ['executing', 'pending_approval'].includes(value.status) && <p className="muted">执行记录尚在核对，稍后可刷新结果。</p>}
    {error && <p className="error" role="alert">{error}</p>}
    {(error || value?.status === 'executing' || value?.status === 'pending_approval') && <button className="secondary" disabled={busy} onClick={() => setAttempt(n => n + 1)}>刷新执行记录</button>}
  </div>;
}
