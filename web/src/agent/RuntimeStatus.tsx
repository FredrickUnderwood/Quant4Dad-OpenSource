import { useEffect, useState } from 'react';
import { agentAPI } from './api';
import type { AgentRuntimeStatus } from './types';
import { toolLabels } from './ToolCard';

export function RuntimeStatus({ profile }: { profile: string }) {
  const [value, setValue] = useState<AgentRuntimeStatus>(), [attempt, setAttempt] = useState(0), [busy, setBusy] = useState(false), [error, setError] = useState('');
  useEffect(() => {
    const abort = new AbortController(); setBusy(true); setError('');
    agentAPI.status(abort.signal).then(result => { if (!abort.signal.aborted) setValue(result); })
      .catch(e => { if (!abort.signal.aborted) { setValue(undefined); setError((e as Error).message); } })
      .finally(() => { if (!abort.signal.aborted) setBusy(false); });
    return () => abort.abort();
  }, [attempt]);
  const labels: Record<string, string> = { ready: '助手服务已连接', unavailable: '助手服务暂不可用', incompatible: '助手服务版本尚未匹配', configuration_mismatch: '助手配置尚未匹配，请完成 API 和 Agent 发布', synchronizing: '模型配置正在同步', model_check_required: '助手服务已连接，请检查模型连接' };
  const tools = value?.profiles.find(p => p.id === profile)?.tools ?? [];
  return <details className="assistant-runtime-status"><summary>{busy ? '正在检查助手服务…' : labels[value?.status ?? ''] ?? '助手连接状态未知'}{tools.length ? ` · ${tools.filter(t => t.available).length}/${tools.length} 项工具可用` : ''}</summary>
    {error && <p className="muted">{error}</p>}
    {value?.runtime && <p className="muted">Runtime {value.runtime.version} · DSH {value.runtime.dsh_version} · Adapter {value.runtime.adapter_version}</p>}
    {value?.profile_source && <p className="muted">提示词配置：{value.profile_source === 'repository' ? '随发布版本更新' : '使用显式配置'}{value.profiles_aligned !== undefined && ` · ${value.profiles_aligned ? 'API 与助手配置一致' : 'API 与助手配置不一致'}`}</p>}
    <ul>{tools.map(tool => <li key={tool.name}>{toolLabels[tool.name] ?? '工具'} · {tool.available ? '可用' : tool.reason === 'profile_tools_disabled' ? '当前配置未启用工具' : '业务功能尚未接入'}</li>)}</ul>
    <button className="secondary" disabled={busy} onClick={() => setAttempt(n => n + 1)}>刷新服务状态</button>
  </details>;
}
