import { useEffect, useState } from 'react';
import { api } from '../api';
import { agentAPI } from './api';
import type { AgentModel } from './types';
import type { LLMProviderMasked } from '../types';

const reasons: Record<string, string> = { ready: '已就绪', probe_required: '需要检查连接', disabled: '未启用', credential_revoked: '密钥已撤销', context_window_required: '请填写上下文上限', max_output_tokens_required: '请填写输出上限', probe_expired: '检查结果已过期', invalid_provider_config: '模型配置不兼容' };
export function AgentModelSettings() {
  const [providers, setProviders] = useState<Record<string, LLMProviderMasked>>({}), [models, setModels] = useState<AgentModel[]>([]), [provider, setProvider] = useState(''), [revision, setRevision] = useState('');
  const [visible, setVisible] = useState(false), [enabled, setEnabled] = useState(true), [contextWindow, setContextWindow] = useState(0), [maxOutput, setMaxOutput] = useState(0), [reasoning, setReasoning] = useState('');
  const [credential, setCredential] = useState<'keep' | 'replace' | 'revoke'>('keep'), [key, setKey] = useState(''), [busy, setBusy] = useState(false), [notice, setNotice] = useState('');
  const load = async () => {
    const [view, catalog] = await Promise.all([api.getLLMProviders(), agentAPI.models()]);
    setProviders(view.providers); setRevision(view.revision); setModels(catalog.models); setVisible(true); setProvider(old => view.providers[old] ? old : Object.keys(view.providers)[0] ?? '');
  };
  useEffect(() => { void load().catch(() => {}); }, []);
  useEffect(() => {
    const p = providers[provider]; if (!p) return;
    const model = models.find(m => m.provider === provider);
    setEnabled(p.agent?.enabled !== false); setContextWindow(p.agent?.context_window || model?.context_window || 0); setMaxOutput(p.agent?.max_output_tokens || model?.max_output_tokens || 0);
    setReasoning(p.agent?.reasoning_effort ?? ''); setCredential('keep'); setKey('');
  }, [provider, providers]);
  if (!visible) return null;
  const p = providers[provider], model = models.find(m => m.provider === provider);
  const operation = async (fn: () => Promise<void>) => { setBusy(true); setNotice(''); try { await fn(); } catch (e) { setNotice((e as Error).message); } finally { setBusy(false); } };
  const save = () => operation(async () => {
    if (!p || (enabled && credential !== 'revoke' && (!Number.isSafeInteger(contextWindow) || !Number.isSafeInteger(maxOutput) || contextWindow <= maxOutput || maxOutput < 1))) throw new Error('请填写有效的上下文和输出上限；输出上限必须小于上下文上限。');
    if (credential === 'replace' && !key.trim()) throw new Error('请输入替换密钥。');
    await agentAPI.configureModel(provider, revision, { ...(credential === 'revoke' ? {} : { agent: enabled ? { enabled, protocol: p.type === 'anthropic' ? 'anthropic-messages' : 'openai-completions', context_window: contextWindow, max_output_tokens: maxOutput, reasoning_effort: reasoning } : { enabled: false } }), api_key_update: credential === 'replace' ? { action: 'replace', value: key } : { action: credential } });
    setKey(''); await load(); setNotice('配置已保存，请检查模型连接。');
  });
  return <section className="settings-module" aria-label="Agent 模型设置">
    <h3>Agent 模型与运行状态</h3><p className="muted">模型地址和密钥与流水线共用。修改配置会让旧的连接检查结果失效。</p>
    <div className="row"><div><label>已保存的模型配置</label><select value={provider} disabled={busy} onChange={e => { setProvider(e.target.value); setNotice(''); }}>{Object.entries(providers).map(([id, value]) => <option key={id} value={id}>{id} · {value.default_model}</option>)}</select></div><button className="secondary" disabled={busy} onClick={() => operation(load)}>重新加载</button></div>
    {p && <>
      <p role="status">状态：{model ? reasons[model.status === 'ready' ? 'ready' : model.reason] ?? '检查未通过' : '尚未检查'}{p.credential_revoked ? ' · 密钥已撤销' : p.has_api_key ? ' · 已配置密钥' : ' · 无密钥'}</p>
      <label className="agent-model-toggle"><input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} disabled={busy} /> 允许助手使用此模型</label>
      {model?.limits_source === 'pinned_catalog' && <p className="muted">已填入内置模型目录的默认上限。使用代理或自定义部署时，可按服务实际限制调整。</p>}
      <div className="row"><div><label>上下文上限（token）</label><input type="number" min={2} max={100000000} value={contextWindow || ''} onChange={e => setContextWindow(Number(e.target.value))} disabled={busy} /></div><div><label>单次输出上限（token）</label><input type="number" min={1} max={10000000} value={maxOutput || ''} onChange={e => setMaxOutput(Number(e.target.value))} disabled={busy} /></div><div><label>推理强度（可选）</label><input value={reasoning} maxLength={32} onChange={e => setReasoning(e.target.value)} disabled={busy} /></div></div>
      <div className="row"><div><label>密钥操作</label><select value={credential} disabled={busy} onChange={e => { setCredential(e.target.value as typeof credential); setKey(''); }}><option value="keep">保留现有密钥</option><option value="replace">替换密钥</option><option value="revoke">撤销密钥（同时影响流水线）</option></select></div>{credential === 'replace' && <div><label>新密钥</label><input type="password" autoComplete="new-password" value={key} onChange={e => setKey(e.target.value)} disabled={busy} /></div>}</div>
      <div className="assistant-actions" style={{ marginTop: 12 }}><button disabled={busy} onClick={save}>{credential === 'revoke' ? '撤销该模型密钥' : '保存 Agent 配置'}</button><button className="secondary" disabled={busy} onClick={() => operation(async () => { const result = await agentAPI.probe(provider); await load(); setNotice(result.status === 'ready' ? '模型连接与工具调用检查通过。' : '检查未通过，请检查模型配置。'); })}>检查模型连接</button></div>
    </>}
    {notice && <p role="status">{notice}</p>}
  </section>;
}
