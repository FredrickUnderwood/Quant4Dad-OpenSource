import { useEffect, useState } from 'react';
import { api } from '../api';
import { costInput } from './models';
import { SettingsHead, settingsError } from './IntegrationSettings';
import type { CostInput, CostModel } from './types';

const blankCost = (): CostInput => ({ name: '', is_default: false, commission_rate: 0, min_commission: 0, stamp_duty_rate: 0, slippage_bps: 0 });
export function CostSettings({ workers }: { workers?: number }) {
  const [items, setItems] = useState<CostModel[] | null>(null), [draft, setDraft] = useState<CostInput | null>(null), [editing, setEditing] = useState<number | null>(null);
  const [busy, setBusy] = useState(false), [error, setError] = useState(''), [notice, setNotice] = useState('');
  const load = async () => { setItems(await api.listCosts()); };
  useEffect(() => { void load().catch(e => setError(settingsError(e))); }, []);
  const operation = async (work: () => Promise<void>) => { setBusy(true); setError(''); setNotice(''); try { await work(); } catch (e) { setError(settingsError(e)); } finally { setBusy(false); } };
  const edit = (value: CostModel | null) => { setEditing(value?.id ?? null); setDraft(value ? { ...value } : blankCost()); setError(''); setNotice(''); };
  const number = (name: keyof Pick<CostInput, 'commission_rate' | 'min_commission' | 'stamp_duty_rate' | 'slippage_bps'>, label: string, multiplier = 1, max?: number) => {
    if (!draft) return null;
    const value = draft[name] * multiplier;
    return <div><label>{label}</label><input aria-label={label} type="number" min={0} max={max} step="any" value={Number.isNaN(value) ? '' : value} onChange={e => setDraft({ ...draft, [name]: e.target.value === '' ? NaN : Number(e.target.value) / multiplier })} /></div>;
  };
  return <section id="backtest" className="settings-module">
    <SettingsHead number="04" title="回测费用与运行参数">管理可重复使用的费用模型；未指定模型的回测使用默认费用配置。</SettingsHead>
    <p className="muted">费率和滑点是回测假设，请按你的交易条件填写。页面百分比会转换为接口使用的小数费率；1 bps = 0.01%。</p>
    {items && items.length > 0 && <div className="settings-table-scroll"><table><thead><tr><th>名称</th><th>佣金率</th><th>最低佣金</th><th>卖出印花税率</th><th>滑点</th><th>操作</th></tr></thead><tbody>{items.map(item => <tr key={item.id}>
      <td>{item.name}{item.is_default && <span className="tag settings-default">默认</span>}</td><td>{(item.commission_rate * 100).toLocaleString(undefined, { maximumFractionDigits: 8 })}%</td><td>{item.min_commission}</td><td>{(item.stamp_duty_rate * 100).toLocaleString(undefined, { maximumFractionDigits: 8 })}%</td><td>{item.slippage_bps} bps</td>
      <td><div className="settings-actions compact"><button className="secondary small" disabled={busy || draft !== null} onClick={() => edit(item)}>编辑</button><button className="secondary small" disabled={busy || item.is_default || draft !== null} onClick={() => void operation(async () => { await api.defaultCost(item.id); await load(); setNotice('默认费用模型已更新。'); })}>设为默认</button><button className="ghost small" disabled={busy || item.is_default || draft !== null} onClick={() => { if (window.confirm(`删除费用模型“${item.name}”？默认模型不能删除。`)) void operation(async () => { await api.deleteCost(item.id); await load(); setNotice('费用模型已删除。'); }); }}>删除</button></div></td>
    </tr>)}</tbody></table></div>}
    {items?.length === 0 && <p className="empty">暂无费用模型，可以新增后设为默认。</p>}
    {items === null && !error && <p className="empty">正在读取费用模型…</p>}
    {draft ? <fieldset className="settings-fields settings-cost-editor" disabled={busy}><h3>{editing === null ? '新增费用模型' : '编辑费用模型'}</h3><div><label>费用模型名称</label><input aria-label="费用模型名称" maxLength={64} value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} /></div>
      <div className="row">{number('commission_rate', '佣金率（%）', 100, 100)}{number('min_commission', '最低佣金（账户货币）')}{number('stamp_duty_rate', '卖出印花税率（%）', 100, 100)}{number('slippage_bps', '滑点（bps）', 1, 10000)}</div>
      <div className="settings-actions"><button onClick={() => void operation(async () => { const body = costInput(draft); if (editing === null) await api.createCost(body); else await api.updateCost(editing, body); await load(); setDraft(null); setEditing(null); setNotice('费用模型已保存。'); })}>保存费用模型</button><button className="secondary" onClick={() => { setDraft(null); setEditing(null); setError(''); }}>取消编辑</button></div>
    </fieldset> : <div className="settings-actions"><button className="secondary" disabled={busy || items === null} onClick={() => edit(null)}>新增费用模型</button><button className="ghost" disabled={busy} onClick={() => void operation(load)}>刷新费用模型</button></div>}
    {error && <p className="error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    <p className="muted">回测并发：{workers ?? '读取部署信息后显示'}。由启动配置 <code>backtest.worker_pool</code> 控制，修改后需重启 API；不会在此页面即时调整正在运行的任务。<a href="#deployment">查看部署配置指引</a></p>
  </section>;
}
