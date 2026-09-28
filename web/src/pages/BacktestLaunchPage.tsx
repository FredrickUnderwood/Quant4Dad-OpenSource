import { useEffect, useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { api } from '../api';
import type { Strategy } from '../types';

const today = () => new Date().toISOString().slice(0, 10);

// A dedicated page for launching a backtest, replacing the earlier chain of prompt() dialogs.
// URL：/strategies/:id/backtest
export default function BacktestLaunchPage() {
  const { id } = useParams();
  const sid = Number(id);
  const nav = useNavigate();
  const [strategy, setStrategy] = useState<Strategy | null>(null);
  const [err, setErr] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const [capital, setCapital] = useState('100000');
  const [start, setStart] = useState('2018-01-01');
  const [end, setEnd] = useState(today());

  useEffect(() => {
    if (!sid) return;
    api.getStrategy(sid).then(setStrategy).catch(e => setErr(String(e.message || e)));
  }, [sid]);

  const universeText = useMemo(
    () => (strategy?.universe || []).join(', '),
    [strategy],
  );

  const submit = async () => {
    setErr('');
    const cap = parseFloat(capital);
    if (!Number.isFinite(cap) || cap <= 0) { setErr('初始资金需要是正数'); return; }
    if (!/^\d{4}-\d{2}-\d{2}$/.test(start)) { setErr('开始日期格式 YYYY-MM-DD'); return; }
    if (!/^\d{4}-\d{2}-\d{2}$/.test(end))   { setErr('结束日期格式 YYYY-MM-DD'); return; }
    if (start > end) { setErr('开始日期不能晚于结束日期'); return; }
    setSubmitting(true);
    try {
      const job = await api.createBacktest({
        strategy_id: sid,
        initial_capital: cap,
        start_date: start,
        end_date: end,
      });
      nav(`/backtests/${job.id}`);
    } catch (e: any) {
      setErr(e.message || String(e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section>
      <h2>启动回测 {strategy && <span className="muted">#{strategy.id} · {strategy.name}</span>}</h2>
      {err && <p className="error">{err}</p>}

      {strategy && (
        <div className="row" style={{ marginBottom: 8 }}>
          <div>
            <label>策略</label>
            <div className="mono" style={{ paddingTop: 4 }}>{strategy.name}</div>
          </div>
          <div>
            <label>周期</label>
            <div className="mono" style={{ paddingTop: 4 }}>{strategy.period}</div>
          </div>
          <div style={{ flex: 2 }}>
            <label>标的</label>
            <div className="mono" style={{ paddingTop: 4, wordBreak: 'break-all' }}>
              {universeText || <span className="muted">（空，需要先去编辑器添加）</span>}
            </div>
          </div>
        </div>
      )}

      <div className="row">
        <div>
          <label>初始资金（元）</label>
          <input type="number" min={0} step={1000} value={capital} onChange={e => setCapital(e.target.value)} />
        </div>
        <div>
          <label>开始日期</label>
          <input type="date" value={start} onChange={e => setStart(e.target.value)} />
        </div>
        <div>
          <label>结束日期</label>
          <input type="date" value={end} onChange={e => setEnd(e.target.value)} />
        </div>
      </div>

      <div style={{ marginTop: 20 }}>
        <button disabled={submitting || !strategy} onClick={submit}>
          {submitting ? '提交中…' : '开始回测'}
        </button>{' '}
        <button className="secondary" onClick={() => nav('/strategies')}>取消</button>
      </div>
    </section>
  );
}
