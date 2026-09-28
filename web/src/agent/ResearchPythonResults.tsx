import { useEffect, useRef, useState } from 'react';
import { agentAPI } from './api';
import { pythonAnalysisResult, pythonResultCards } from './python-analysis';
import { PythonAnalysis } from './PythonAnalysis';
import { resultText } from './tool-result';
import type { AgentToolResult, LiveTool } from './types';

/** One visible chart per instrument/range, at the end of the answer. Only
 * authorized persisted results are rendered, never model-authored chart text. */
export function ResearchPythonResults({ runID, tools }: { runID: string; tools: LiveTool[] }) {
  const cache = useRef(new Map<string, AgentToolResult>());
  const [values, setValues] = useState<AgentToolResult[]>([]);
  const [error, setError] = useState(''), [busy, setBusy] = useState(true), [attempt, setAttempt] = useState(0);
  const ids = tools.map(tool => tool.id).join(',');
  useEffect(() => {
    const abort = new AbortController(), calls = ids.split(',').filter(Boolean);
    setBusy(true); setError('');
    void Promise.allSettled(calls.filter(id => !cache.current.has(id)).map(async id => {
      const value = await agentAPI.toolResult(runID, id, abort.signal);
      if (value.run_id !== runID || value.tool_call_id !== id || value.tool_name !== 'execute_python') throw new Error('研究结果与本轮调用不一致。');
      if (!pythonAnalysisResult(value)) throw new Error(value.status === 'expired' ? '研究结果已超过保留期。' : '研究结果无法展示，请重新载入。');
      if (!abort.signal.aborted) cache.current.set(id, value);
    })).then(results => {
      if (abort.signal.aborted) return;
      setValues(calls.flatMap(id => cache.current.has(id) ? [cache.current.get(id)!] : []));
      const failed = results.find(result => result.status === 'rejected');
      if (failed?.status === 'rejected') setError(failed.reason instanceof Error ? failed.reason.message : '研究图表未能载入。');
      setBusy(false);
    });
    return () => abort.abort();
  }, [runID, ids, attempt]);
  const cards = pythonResultCards(values);
  return <section className="assistant-research-overview" aria-label="研究图表与统计结果">
    <h3>研究图表与统计结果</h3>
    {busy && <p className="muted" role="status">正在载入研究结果…</p>}
    {error && <p className="error" role="alert">{error} <button className="secondary" onClick={() => setAttempt(n => n + 1)}>重新载入图表</button></p>}
    {cards.map((a, index) => <PythonAnalysis key={`${a.source_sha256}-${a.input_sha256}-${index}`} analysis={a} />)}
    {!!values.length && <details className="assistant-kline-sources"><summary>查看计算记录（{values.length} 项）</summary>{values.map(value => <details key={value.tool_call_id}><summary>{pythonAnalysisResult(value)?.title || 'Python 计算'} · {pythonAnalysisResult(value)?.status}</summary><pre>{resultText(value)}</pre></details>)}</details>}
  </section>;
}
