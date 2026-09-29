import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import { archiveDraft, archiveUpdate, marketDraft, marketUpdate } from './models';
import type { ArchiveSettingsInput, ArchiveStatus, IntegrationsView, IntegrationUpdate, MarketSettingsInput, SecretUpdate } from './types';

export const SETTINGS_DOCS = 'https://github.com/FredrickUnderwood/Quant4Dad-OpenSource/blob/master/docs/';
export function settingsError(error: unknown): string {
  const message = error instanceof Error ? error.message : '';
  try { const body = JSON.parse(message); return typeof body.message === 'string' ? body.message : '操作未完成，请稍后重试。'; }
  catch { return message || '操作未完成，请稍后重试。'; }
}
export function useIntegrationSettings() {
  const [view, setView] = useState<IntegrationsView | null>(null), [error, setError] = useState(''), [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const perform = async <T,>(work: () => Promise<T>): Promise<T> => {
    if (lock.current) throw new Error('请等待当前设置操作完成。');
    lock.current = true; setBusy(true);
    try { return await work(); } finally { lock.current = false; setBusy(false); }
  };
  const reload = async () => {
    setError('');
    try { const next = await perform(api.getIntegrations); setView(next); return next; }
    catch (e) { setError(settingsError(e)); throw e; }
  };
  useEffect(() => { void reload().catch(() => {}); }, []);
  const save = (body: IntegrationUpdate) => perform(async () => { const next = await api.setIntegrations(body); setView(next); return next; });
  return { view, error, busy, reload, save, perform };
}
export type IntegrationState = ReturnType<typeof useIntegrationSettings>;

export function SettingsHead({ number, title, children }: { number: string; title: string; children?: ReactNode }) {
  return <div className="module-head"><span className="module-no">{number}</span><div><h2>{title}</h2>{children && <p className="muted module-sub">{children}</p>}</div></div>;
}
function SecretField({ label, configured, value, onChange }: { label: string; configured: boolean; value: SecretUpdate; onChange: (value: SecretUpdate) => void }) {
  return <div className="settings-secret"><label>{label} <span className="muted">{configured ? '已配置' : '未配置'}</span></label>
    <select aria-label={`${label}操作`} value={value.action} onChange={e => onChange({ action: e.target.value as SecretUpdate['action'] })}>
      <option value="keep">保留现有凭据</option><option value="replace">替换凭据</option><option value="clear">清除凭据</option>
    </select>
    {value.action === 'replace' && <input aria-label={`新${label}`} type="password" autoComplete="new-password" value={value.value ?? ''} placeholder="填写新值；不会显示已保存的凭据" onChange={e => onChange({ action: 'replace', value: e.target.value })} />}
    {value.action === 'clear' && <p className="muted">保存后移除此凭据，依赖它的连接可能不可用。</p>}
  </div>;
}
function LoadFailure({ state }: { state: IntegrationState }) {
  return <div className="empty">{state.error ? <p className="error" role="alert">{state.error}</p> : <p>正在读取设置…</p>}<button className="secondary" disabled={state.busy} onClick={() => void state.reload().catch(() => {})}>重新读取</button></div>;
}
function NumberField({ label, value, minimum = 0, maximum, onChange }: { label: string; value: number; minimum?: number; maximum?: number; onChange: (value: number) => void }) {
  return <div><label>{label}</label><input aria-label={label} type="number" min={minimum} max={maximum} step={1} value={Number.isNaN(value) ? '' : value} onChange={e => onChange(e.target.value === '' ? NaN : Number(e.target.value))} /></div>;
}

export function MarketSettings({ state, children }: { state: IntegrationState; children?: ReactNode }) {
  const [draft, setDraft] = useState<MarketSettingsInput | null>(null), [dirty, setDirty] = useState(false), [notice, setNotice] = useState(''), [error, setError] = useState('');
  useEffect(() => { if (state.view && !dirty) setDraft(marketDraft(state.view.market)); }, [state.view, dirty]);
  const edit = (patch: Partial<MarketSettingsInput>) => { setDraft(old => old ? { ...old, ...patch } : old); setDirty(true); setNotice(''); };
  const operation = async (work: () => Promise<void>) => { setError(''); setNotice(''); try { await work(); } catch (e) { setError(settingsError(e)); } };
  const save = () => operation(async () => {
    if (!draft || !state.view) return;
    const next = await state.save(marketUpdate(state.view.revision, draft));
    setDraft(marketDraft(next.market)); setDirty(false); setNotice('数据接入配置已保存，将用于下一次采集和调度；运行中的任务继续使用原配置。');
  });
  return <section id="market" className="settings-module">
    <SettingsHead number="01" title="数据接入">选择手动 CSV、Tushare 官方 API，或你提供的 HTTP 数据接口。</SettingsHead>
    {!draft || !state.view ? <LoadFailure state={state} /> : <>
      {!state.view.background_enabled && <p className="settings-notice">本进程已禁用后台任务。可以保存配置，但自动采集需在部署配置中恢复后台运行。</p>}
      <fieldset className="settings-fields" disabled={state.busy}>
        <div className="row"><div><label>行情来源</label><select aria-label="行情来源" value={draft.provider} onChange={e => { const provider = e.target.value as MarketSettingsInput['provider']; edit({ provider, auto_sync: { ...draft.auto_sync, enabled: provider === 'manual' ? false : draft.auto_sync.enabled } }); }}>
          <option value="manual">手动 CSV</option><option value="tushare">Tushare 官方 API</option><option value="http">自有 HTTP 接口</option>{[...new Set([...(state.view.registered_providers ?? []), draft.provider])].filter(name => !['manual', 'tushare', 'http'].includes(name)).map(name => <option key={name} value={name}>{name}（已安装扩展）</option>)}
        </select></div></div>
        {draft.provider === 'manual' && <p className="muted">手动模式不会抓取网络行情。使用已有 CSV 导入命令，已有数据仍可分析与回测。<a href={`${SETTINGS_DOCS}data-import.md`} target="_blank" rel="noreferrer">查看 CSV 格式和导入方式</a></p>}
        {draft.provider === 'tushare' && <><p className="muted">使用你自己的 Tushare token；可访问的数据和调用额度由你的账号权限决定。</p><SecretField label="Tushare token" configured={state.view.market.tushare.has_token} value={draft.tushare.token} onChange={token => edit({ tushare: { token } })} /></>}
        {!['manual', 'tushare'].includes(draft.provider) && <>
          <div><label>{draft.provider === 'http' ? 'HTTP 接口根地址' : '扩展接口地址（按扩展约定）'}</label><input aria-label="HTTP 接口根地址" type="url" placeholder="https://data.example.com/api" value={draft.http.base_url} onChange={e => edit({ http: { ...draft.http, base_url: e.target.value } })} /></div>
          <p className="muted">已保存 token 时，修改接口地址必须同时选择替换或清除 token，避免把旧凭据发送到新服务。</p>
          <SecretField label={draft.provider === 'http' ? 'HTTP Bearer token（可选）' : '扩展 token（可选）'} configured={state.view.market.http.has_token} value={draft.http.token} onChange={token => edit({ http: { ...draft.http, token } })} />
          {draft.provider === 'http' ? <details className="settings-details"><summary>自有接口需要提供什么？</summary><p>接口提供 <code>GET /instruments</code> 和 <code>GET /bars</code>，接收 <code>cursor</code>、<code>limit=1000</code>；行情请求还包含 <code>code</code>、<code>period</code>、<code>start</code>、<code>end</code>。返回 <code>{'{items: [...], next_cursor: ""}'}</code>，日期使用 YYYY-MM-DD。可选 token 通过 Bearer 请求头发送。</p><p>这里接入你提供的数据服务，并不内置免费的外站采集器。接口不可伪造官方复权因子来源；不跟随重定向。<a href={`${SETTINGS_DOCS}data-provider.md`} target="_blank" rel="noreferrer">完整字段与接口契约</a></p></details> : <p className="muted">地址、token 与返回格式由此扩展的实现约定；通用 HTTP 接口协议不适用于所有扩展。</p>}
        </>}
        <details className="settings-details"><summary>采集范围与频率</summary><div className="row">
          <NumberField label="首次回补年数" minimum={1} maximum={50} value={draft.initial_years} onChange={initial_years => edit({ initial_years })} />
          <NumberField label="采集并发" minimum={1} maximum={32} value={draft.concurrency} onChange={concurrency => edit({ concurrency })} />
          <NumberField label="每分钟请求上限（0 为不限）" maximum={60000} value={draft.rate_limit_per_min} onChange={rate_limit_per_min => edit({ rate_limit_per_min })} />
        </div><label className="inline settings-toggle"><input type="checkbox" checked={draft.include_etf} onChange={e => edit({ include_etf: e.target.checked })} />包含 ETF（需要来源支持及相应权限）</label></details>
        <div className="row settings-schedule"><div><label className="inline settings-toggle"><input type="checkbox" checked={draft.auto_sync.enabled} disabled={draft.provider === 'manual'} onChange={e => edit({ auto_sync: { ...draft.auto_sync, enabled: e.target.checked } })} />每日自动同步日线</label></div><div><label>每日执行时刻（服务时区）</label><input aria-label="每日行情同步时刻" type="time" value={draft.auto_sync.daily_time} onChange={e => edit({ auto_sync: { ...draft.auto_sync, daily_time: e.target.value } })} /></div></div>
      </fieldset>
      <div className="settings-actions"><button disabled={state.busy || !dirty} onClick={save}>保存数据接入</button><button className="secondary" disabled={state.busy || dirty || state.view.market.provider === 'manual'} onClick={() => void operation(async () => { const result = await state.perform(() => api.testMarket(state.view!.revision)); setNotice(result.message || '已保存的数据源连接检查通过。'); })}>检查已保存的数据源</button><button className="ghost" disabled={state.busy} onClick={() => void operation(async () => { const next = await state.reload(); setDraft(marketDraft(next.market)); setDirty(false); })}>{dirty ? '放弃修改并重载' : '刷新配置'}</button><Link to="/datasync">查看采集任务</Link></div>
      <p className="muted">连接检查只读取首个标的目录页（Tushare 为一条 stock_basic），不保证 ETF、其他数据集或全部额度可用。修改后先保存再检查。</p>
      {error && <p className="error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    </>}
    {children}
  </section>;
}

function timeLabel(value?: string | null) { return value ? new Date(value).toLocaleString() : '—'; }
export function ArchiveSettings({ state }: { state: IntegrationState }) {
  const [draft, setDraft] = useState<ArchiveSettingsInput | null>(null), [dirty, setDirty] = useState(false), [status, setStatus] = useState<ArchiveStatus | null>(null);
  const [notice, setNotice] = useState(''), [error, setError] = useState(''), [statusError, setStatusError] = useState(''), [running, setRunning] = useState(false);
  useEffect(() => { if (state.view && !dirty) setDraft(archiveDraft(state.view.archive)); }, [state.view, dirty]);
  const refreshStatus = async () => { try { setStatus(await api.getArchiveStatus()); setStatusError(''); } catch { setStatusError('归档运行状态暂不可用，请刷新后重试。'); } };
  useEffect(() => { void refreshStatus(); const timer = setInterval(() => void refreshStatus(), 10000); return () => clearInterval(timer); }, []);
  const edit = (patch: Partial<ArchiveSettingsInput>) => { setDraft(old => old ? { ...old, ...patch } : old); setDirty(true); setNotice(''); };
  const operation = async (work: () => Promise<void>) => { setError(''); setNotice(''); try { await work(); } catch (e) { setError(settingsError(e)); } };
  const busy = state.busy || running;
  const run = () => {
    if (!status?.configured || status.running || dirty || busy) return;
    if (!window.confirm(`将归档超过 ${state.view?.archive.retention_days ?? status.retention_days} 天的整日已终态事件、执行轨迹和 AI 结果，验证 OSS 对象后删除对应本地记录。该操作不备份行情，也没有自动恢复功能。确认立即归档并清理？`)) return;
    void operation(async () => { setRunning(true); try { const result = await api.runArchive(); setNotice(`归档完成：${result.days} 天、${result.events} 条事件，已清理对应本地记录。`); } finally { setRunning(false); await refreshStatus(); } });
  };
  return <section id="archive" className="settings-module">
    <SettingsHead number="03" title="OSS 事件归档">将过期的已终态事件及其执行轨迹、AI 结果归档到 OSS，验证后清理本地记录。</SettingsHead>
    <p className="settings-notice">仅处理事件数据，不包含行情、资讯、策略、数据库全量备份或恢复。启用定时归档或手动执行都可能删除过期本地事件。</p>
    {!draft || !state.view ? <LoadFailure state={state} /> : <>
      {!state.view.background_enabled && <p className="muted">后台任务已禁用，定时归档不会运行；手动归档仍会执行清理。</p>}
      <fieldset className="settings-fields" disabled={busy || status?.running}>
        <div className="row"><div><label>OSS Endpoint</label><input aria-label="OSS Endpoint" value={draft.oss.endpoint} placeholder="oss-cn-hangzhou.aliyuncs.com" onChange={e => edit({ oss: { ...draft.oss, endpoint: e.target.value } })} /></div><div><label>Region</label><input aria-label="OSS Region" value={draft.oss.region} placeholder="cn-hangzhou" onChange={e => edit({ oss: { ...draft.oss, region: e.target.value } })} /></div></div>
        <div className="row"><div><label>Bucket</label><input aria-label="OSS Bucket" value={draft.oss.bucket} onChange={e => edit({ oss: { ...draft.oss, bucket: e.target.value } })} /></div><div><label>对象路径前缀</label><input aria-label="OSS 对象路径前缀" value={draft.oss.prefix} onChange={e => edit({ oss: { ...draft.oss, prefix: e.target.value } })} /></div></div>
        <p className="muted">已保存 OSS 凭据时，修改 Endpoint 必须为两项凭据都选择替换或清除。仅修改 Bucket、Region 或前缀不需要重新输入密钥。</p>
        <div className="row"><SecretField label="OSS AccessKey ID" configured={state.view.archive.oss.has_access_key_id} value={draft.oss.access_key_id} onChange={access_key_id => edit({ oss: { ...draft.oss, access_key_id } })} /><SecretField label="OSS AccessKey Secret" configured={state.view.archive.oss.has_access_key_secret} value={draft.oss.access_key_secret} onChange={access_key_secret => edit({ oss: { ...draft.oss, access_key_secret } })} /></div>
        <div className="row settings-schedule"><div><label className="inline settings-toggle"><input type="checkbox" checked={draft.enabled} onChange={e => edit({ enabled: e.target.checked })} />启用每日事件归档与本地清理</label></div><NumberField label="本地保留天数" minimum={1} maximum={36500} value={draft.retention_days} onChange={retention_days => edit({ retention_days })} /><div><label>每日归档时刻（服务时区）</label><input aria-label="每日归档时刻" type="time" value={draft.daily_time} onChange={e => edit({ daily_time: e.target.value })} /></div></div>
        <details className="settings-details"><summary>归档处理参数</summary><div className="row"><NumberField label="每批处理事件数" minimum={1} maximum={2000} value={draft.batch_size} onChange={batch_size => edit({ batch_size })} /><NumberField label="批次间隔（毫秒）" maximum={60000} value={draft.batch_sleep_ms} onChange={batch_sleep_ms => edit({ batch_sleep_ms })} /></div></details>
      </fieldset>
      <div className="settings-actions"><button disabled={busy || status?.running || !dirty} onClick={() => void operation(async () => { const next = await state.save(archiveUpdate(state.view!.revision, draft)); setDraft(archiveDraft(next.archive)); setDirty(false); setNotice('事件归档配置已保存。'); await refreshStatus(); })}>保存 OSS 与归档配置</button><button className="secondary" disabled={busy || dirty || !status?.configured} onClick={() => void operation(async () => { const result = await state.perform(() => api.testOSS(state.view!.revision)); setNotice(result.message || 'OSS 桶读取检查通过。'); })}>检查已保存的 OSS 连接</button><button className="ghost" disabled={busy} onClick={() => void operation(async () => { const next = await state.reload(); setDraft(archiveDraft(next.archive)); setDirty(false); await refreshStatus(); })}>{dirty ? '放弃修改并重载' : '刷新配置与状态'}</button></div>
      <p className="muted">连接检查仅调用 GetBucketInfo 读取桶信息，不上传或删除对象，也不证明具备归档所需的上传与读取对象权限。</p>
      {status && <div className="settings-status"><span>OSS：{status.configured ? '已配置' : '未配置'}</span><span>任务：{running || status.running ? '正在运行' : status.enabled ? '定时归档已开启' : '定时归档已关闭'}</span><span>下次：{timeLabel(status.next_run_at)}</span><span>上次：{timeLabel(status.last_run_at)}</span><span>上次归档：{status.last_days} 天 / {status.last_events} 条事件</span></div>}
      {statusError && <p className="error" role="alert">{statusError}</p>}{status?.last_error && <p className="error" role="alert">上次归档失败：{status.last_error}</p>}
      <div className="settings-actions"><button className="secondary danger" disabled={busy || dirty || !status?.configured || status.running || !!statusError} onClick={run}>{running || status?.running ? '归档正在运行…' : '立即归档并清理本地过期事件'}</button></div>
      {error && <p className="error" role="alert">{error}</p>}{notice && <p role="status">{notice}</p>}
    </>}
  </section>;
}
