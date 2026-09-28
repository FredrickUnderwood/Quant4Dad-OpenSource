import { Fragment, useEffect, useState } from 'react';
import { api } from '../api';
import type { BarPeriod, DataSyncJob, DataSyncFailure, DataCoverageSummary, AutoSyncStatus } from '../types';
import Select from '../components/Select';

function StatusTag({ s }: { s: string }) {
  const cls = s === 'succeed' ? 'success' : s === 'failed' ? 'failed' : 'running';
  return <span className={`tag ${cls}`}>{s}</span>;
}

const PERIOD_LABEL: Record<BarPeriod, string> = { '1d': '日线', '1w': '周线', '1mo': '月线' };

function fmtDate(s?: string | null) {
  if (!s) return '—';
  return s.slice(0, 10);
}

function fmtTs(s?: string | null) {
  if (!s) return '—';
  return s.slice(0, 19).replace('T', ' ');
}

function Stat({ label, value, tone }: { label: string; value: number | string; tone?: 'ok' | 'warn' | 'bad' | 'muted' }) {
  const color = tone === 'ok'   ? 'var(--up)'
              : tone === 'warn' ? 'var(--warn)'
              : tone === 'bad'  ? 'var(--down)'
              : tone === 'muted' ? 'var(--muted)'
              : 'var(--ink)';
  return (
    <div style={{ minWidth: 96 }}>
      <div className="muted" style={{ marginBottom: 4 }}>{label}</div>
      <div style={{ fontFamily: 'JetBrains Mono, ui-monospace, monospace', fontSize: 22, color, fontWeight: 500 }}>{value}</div>
    </div>
  );
}

function CoverageRow({ s }: { s: DataCoverageSummary }) {
  const scanning = s.scan_status === 'running';
  return (
    <div style={{
      display: 'flex', gap: 32, alignItems: 'center', flexWrap: 'wrap',
      padding: '12px 0', borderTop: '1px solid var(--hair)',
    }}>
      <div style={{ minWidth: 80 }}>
        <div style={{ fontFamily: 'Fraunces, serif', fontSize: 18 }}>{PERIOD_LABEL[s.period] ?? s.period}</div>
        <div className="muted">{s.period}</div>
      </div>
      <Stat label="标的数" value={s.total_instruments} />
      <Stat label="完整"   value={s.complete_count} tone={s.complete_count > 0 ? 'ok' : 'muted'} />
      <Stat label="滞后"   value={s.stale_count}    tone={s.stale_count    > 0 ? 'warn' : 'muted'} />
      <Stat label="无数据" value={s.empty_count}    tone={s.empty_count    > 0 ? 'bad'  : 'muted'} />
      <Stat label="最新bar" value={fmtDate(s.latest_bar_date)} />
      <div style={{ marginLeft: 'auto', textAlign: 'right' }}>
        {scanning
          ? <span className="tag running">扫描中</span>
          : <span className="muted">{fmtTs(s.scanned_at)}{s.scan_duration_ms ? `（${(s.scan_duration_ms / 1000).toFixed(1)}s）` : ''}</span>}
        {s.last_error
          ? <div className="muted" style={{ color: 'var(--down)', marginTop: 4 }}>{s.last_error}</div>
          : null}
      </div>
    </div>
  );
}

function FailureDetail({ taskId }: { taskId: number }) {
  const [items, setItems] = useState<DataSyncFailure[] | null>(null);
  const [err, setErr] = useState('');

  useEffect(() => {
    let alive = true;
    api.getDataSyncFailures(taskId, 5)
      .then(r => { if (alive) setItems(r); })
      .catch(e => { if (alive) setErr(e.message); });
    return () => { alive = false; };
  }, [taskId]);

  if (err) return <div className="muted" style={{ color: 'var(--down)' }}>加载失败详情出错：{err}</div>;
  if (items === null) return <div className="muted">加载中…</div>;
  if (items.length === 0) return <div className="muted">暂无失败记录详情。</div>;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div className="muted">失败样例（前 5 条）</div>
      {items.map(f => (
        <div key={f.id} style={{ borderTop: '1px solid var(--hair)', paddingTop: 8 }}>
          <div style={{ display: 'flex', gap: 12, alignItems: 'baseline', flexWrap: 'wrap' }}>
            <span style={{ fontFamily: 'JetBrains Mono, ui-monospace, monospace' }}>{f.code}</span>
            {f.http_status ? <span className="tag failed">HTTP {f.http_status}</span> : null}
            <span style={{ color: 'var(--down)' }}>{f.reason}</span>
          </div>
          {f.response
            ? <pre style={{
                marginTop: 6, whiteSpace: 'pre-wrap', wordBreak: 'break-all',
                background: 'var(--panel, rgba(0,0,0,0.04))', padding: 8, borderRadius: 6,
                fontFamily: 'JetBrains Mono, ui-monospace, monospace', fontSize: 12,
                maxHeight: 180, overflow: 'auto',
              }}>{f.response}</pre>
            : <div className="muted" style={{ marginTop: 4 }}>无响应内容（请求未完成）</div>}
        </div>
      ))}
    </div>
  );
}

export default function DataSyncPage() {
  const [mode, setMode] = useState<'incremental' | 'full'>('incremental');
  const [period, setPeriod] = useState<BarPeriod>('1d');
  const [start, setStart] = useState('');
  const [end, setEnd] = useState('');
  const [codes, setCodes] = useState('');
  const [msg, setMsg] = useState('');
  const [items, setItems] = useState<DataSyncJob[]>([]);
  const [coverage, setCoverage] = useState<DataCoverageSummary[]>([]);
  const [scanning, setScanning] = useState(false);
  const [scanMsg, setScanMsg] = useState('');
  const [auto, setAuto] = useState<AutoSyncStatus | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);

  const reload = () => { api.listDataSync(20).then(setItems).catch(() => {}); };
  const reloadCoverage = () => {
    api.listCoverage().then(r => {
      setCoverage(r.items);
      setScanning(r.running);
    }).catch(() => {});
  };

  const reloadAuto = () => { api.getAutoSyncStatus().then(setAuto).catch(() => {}); };

  useEffect(() => {
    reload();
    reloadCoverage();
    reloadAuto();
    const t = setInterval(() => {
      reload();
      reloadCoverage();
      reloadAuto();
    }, 3000);
    return () => clearInterval(t);
  }, []);

  const triggerScan = async () => {
    setScanMsg('');
    try {
      await api.triggerCoverageScan();
      setScanning(true);
      setScanMsg('已触发扫描，结果会自动刷新；耗时取决于已有数据量。');
      reloadCoverage();
    } catch (e: any) {
      setScanMsg('触发失败：' + e.message);
    }
  };

  const submit = async () => {
    setMsg('');
    try {
      await api.startDataSync({
        mode, period,
        start_date: start || undefined,
        end_date: end || undefined,
        codes: codes.split(',').map(s => s.trim()).filter(Boolean),
      });
      setMsg('已启动，下方表格会自动刷新');
      reload();
    } catch (e: any) {
      setMsg('启动失败：' + e.message);
    }
  };

  return (
    <>
      <section>
        <h2>数据接入</h2>
        <p className="muted">请先导入自有 CSV 行情，或安装数据源扩展。本项目不内置行情采集器；已有数据可直接用于覆盖度扫描、分析和回测。</p>
        <a href="https://github.com/FredrickUnderwood/Quant4Dad-OpenSource/blob/master/docs/data-import.md" target="_blank" rel="noreferrer">查看数据导入说明</a>
      </section>
      <section>
        <h2>
          每日自动同步
          <span className="muted" style={{ marginLeft: 8 }}>仅日线增量 / 后台进程</span>
        </h2>
        {auto ? (
          <div style={{
            display: 'flex', gap: 32, flexWrap: 'wrap', alignItems: 'center',
            padding: '8px 0',
          }}>
            <div>
              <span className={`tag ${auto.enabled ? 'success' : 'failed'}`}>
                {auto.enabled ? '已开启' : '已关闭'}
              </span>
              <span className="muted" style={{ marginLeft: 8 }}>每天 {auto.daily_time} 本地时间</span>
            </div>
            <div>
              <div className="muted">下次执行</div>
              <div style={{ fontFamily: 'JetBrains Mono, ui-monospace, monospace' }}>
                {fmtTs(auto.next_run_at)}
              </div>
            </div>
            <div>
              <div className="muted">上次执行</div>
              <div style={{ fontFamily: 'JetBrains Mono, ui-monospace, monospace' }}>
                {fmtTs(auto.last_run_at)}
                {auto.last_task_id ? <span className="muted" style={{ marginLeft: 8 }}>#{auto.last_task_id}</span> : null}
              </div>
            </div>
            {auto.last_error
              ? <div className="muted" style={{ color: 'var(--down)' }}>{auto.last_error}</div>
              : null}
          </div>
        ) : <div className="muted">加载中…</div>}
      </section>

      <section>
        <h2>
          数据覆盖度
          <span className="muted" style={{ marginLeft: 8 }}>定时扫描 bar 表 / 不影响同步</span>
        </h2>
        <div style={{ marginBottom: 8, display: 'flex', alignItems: 'center', gap: 12 }}>
          <button onClick={triggerScan} disabled={scanning}>
            {scanning ? '扫描中…' : '立即重新扫描'}
          </button>
          <span className="muted">{scanMsg}</span>
        </div>
        {coverage.length === 0
          ? <div className="muted" style={{ padding: '12px 0' }}>暂无扫描结果，等待首次定时扫描完成或点击上方按钮触发。</div>
          : coverage.map(c => <CoverageRow key={c.id} s={c} />)}
      </section>

      <section>
        <h2>新建同步任务</h2>
        <div className="row">
          <div>
            <label>模式</label>
            <Select
              value={mode}
              onChange={v => setMode(v as any)}
              options={[
                { value: 'incremental', label: '增量（从上次拉到今天）' },
                { value: 'full',        label: '全量（按起止日期重拉）' },
              ]}
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <label>周期</label>
            <Select
              value={period}
              onChange={v => setPeriod(v as BarPeriod)}
              options={[
                { value: '1d',  label: '日线' },
                { value: '1w',  label: '周线' },
                { value: '1mo', label: '月线' },
              ]}
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <label>开始日期（全量时；留空用默认）</label>
            <input type="date" value={start} onChange={e => setStart(e.target.value)} />
          </div>
          <div>
            <label>结束日期（留空到今天）</label>
            <input type="date" value={end} onChange={e => setEnd(e.target.value)} />
          </div>
        </div>
        <div style={{ marginTop: 12 }}>
          <label>指定代码（逗号分隔；留空 = 全 A 股）</label>
          <input value={codes} onChange={e => setCodes(e.target.value)} placeholder="sh.600519,sz.000001" style={{ width: '100%' }} />
        </div>
        <div style={{ marginTop: 16 }}>
          <button onClick={submit}>开始同步</button>{' '}
          <span className="muted">{msg}</span>
        </div>
      </section>

      <section>
        <h2>同步历史</h2>
        <table>
          <thead><tr><th>ID</th><th>模式</th><th>周期</th><th>状态</th><th>进度</th><th>开始</th><th>结束</th></tr></thead>
          <tbody>
            {items.map(t => {
              const pct = t.total > 0 ? Math.round((t.done + t.failed) / t.total * 100) : 0;
              const isOpen = expanded === t.id;
              return (
                <Fragment key={t.id}>
                  <tr>
                    <td>{t.id}</td>
                    <td>{t.mode}</td>
                    <td>{t.period}</td>
                    <td><StatusTag s={t.status} /></td>
                    <td>
                      {pct}% ({t.done}/{t.total}
                      {t.failed
                        ? <>, <button
                            onClick={() => setExpanded(isOpen ? null : t.id)}
                            style={{
                              padding: 0, border: 'none', background: 'none', cursor: 'pointer',
                              color: 'var(--down)', textDecoration: 'underline',
                            }}
                          >失败 {t.failed} {isOpen ? '▴' : '▾'}</button></>
                        : null})
                    </td>
                    <td>{(t.started_at || '').slice(0, 19).replace('T', ' ')}</td>
                    <td>{(t.finished_at || '').slice(0, 19).replace('T', ' ')}</td>
                  </tr>
                  {isOpen
                    ? <tr>
                        <td colSpan={7} style={{ background: 'var(--panel, rgba(0,0,0,0.02))' }}>
                          <FailureDetail taskId={t.id} />
                        </td>
                      </tr>
                    : null}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </section>
    </>
  );
}
