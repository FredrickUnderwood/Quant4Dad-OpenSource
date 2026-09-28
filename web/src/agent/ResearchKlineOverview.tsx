import { useEffect, useMemo, useRef, useState } from 'react';
import { agentAPI } from './api';
import { klineAnalysisResult } from './kline-analysis';
import { klineOverviews } from './kline-overview';
import { KlineAnalysis } from './KlineAnalysis';
import { resultText } from './tool-result';
import type { AgentToolResult, LiveTool } from './types';

/** Mounted once per run. Artifacts remain authorized by run + call identity;
 * the transcript and model-supplied text are never used as chart data. */
export function ResearchKlineOverview({ runID, tools }: { runID: string; tools: LiveTool[] }) {
  const cache = useRef(new Map<string, AgentToolResult>());
  const [values, setValues] = useState<AgentToolResult[]>([]);
  const [error, setError] = useState(''), [busy, setBusy] = useState(true), [attempt, setAttempt] = useState(0);
  const ids = tools.map(tool => tool.id).join(',');
  useEffect(() => {
    const abort = new AbortController();
    const calls = ids.split(',');
    setBusy(true); setError('');
    void Promise.allSettled(calls.filter(id => !cache.current.has(id)).map(async id => {
      const value = await agentAPI.toolResult(runID, id, abort.signal);
      if (value.run_id !== runID || value.tool_call_id !== id || value.tool_name !== 'analyze_kline') throw new Error('行情结果与本轮调用不一致。');
      if (!klineAnalysisResult(value)) throw new Error(value.status === 'expired' ? '部分行情结果已超过保留期，无法生成完整总图。' : '部分行情结果无法读取，尚不能生成完整总图。');
      if (!abort.signal.aborted) cache.current.set(id, value);
    })).then(results => {
      if (abort.signal.aborted) return;
      setValues(calls.flatMap(id => cache.current.has(id) ? [cache.current.get(id)!] : []));
      const failed = results.find(result => result.status === 'rejected');
      if (failed?.status === 'rejected') setError(failed.reason instanceof Error ? failed.reason.message : '行情图表未能载入。');
      setBusy(false);
    });
    return () => abort.abort();
  }, [runID, ids, attempt]);
  const groups = useMemo(() => klineOverviews(values.flatMap(value => {
    const a = klineAnalysisResult(value); return a ? [a] : [];
  })), [values]);
  return <div className="assistant-research-overview">
    {busy && <p className="muted" role="status">正在汇总行情图表…</p>}
    {error && <p className="error" role="alert">{error} <button type="button" className="secondary" onClick={() => setAttempt(n => n + 1)}>重新载入图表</button></p>}
    {!busy && !error && groups.map((group, i) => <KlineAnalysis key={i} analysis={group.analysis} segments={group.segments} />)}
    {!!values.length && <details className="assistant-kline-sources"><summary>查看分段执行记录（{values.length} 项）</summary>{values.map(value => <details key={value.tool_call_id}><summary>{tools.find(tool => tool.id === value.tool_call_id)?.arguments || '行情分析'}</summary><pre>{resultText(value)}</pre></details>)}</details>}
  </div>;
}
