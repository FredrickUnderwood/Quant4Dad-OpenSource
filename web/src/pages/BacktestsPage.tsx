import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';
import type { BacktestJob } from '../types';

function StatusTag({ s }: { s: string }) {
  const cls = s === 'succeed' ? 'success' : s === 'failed' ? 'failed' : 'running';
  return <span className={`tag ${cls}`}>{s}</span>;
}

export default function BacktestsPage() {
  const [items, setItems] = useState<BacktestJob[]>([]);

  const reload = () => { api.listBacktests(50).then(setItems).catch(() => {}); };

  useEffect(() => {
    reload();
    const t = setInterval(reload, 3000);
    return () => clearInterval(t);
  }, []);

  return (
    <section>
      <h2>回测任务</h2>
      {items.length === 0 ? (
        <p className="empty">还没有回测任务。去策略页面点「回测」按钮启动一次吧。</p>
      ) : (
        <table>
          <thead><tr><th>ID</th><th>策略 ID</th><th>初始资金</th><th>时间段</th><th>状态</th><th>开始</th><th>结束</th><th>操作</th></tr></thead>
          <tbody>
            {items.map(j => (
              <tr key={j.id}>
                <td>{j.id}</td>
                <td>{j.strategy_id}</td>
                <td>{j.initial_capital.toLocaleString()}</td>
                <td>{(j.start_date || '').slice(0, 10)} ~ {(j.end_date || '').slice(0, 10)}</td>
                <td><StatusTag s={j.status} /></td>
                <td>{(j.started_at || '').slice(0, 19).replace('T', ' ')}</td>
                <td>{(j.finished_at || '').slice(0, 19).replace('T', ' ')}</td>
                <td><Link to={`/backtests/${j.id}`}><button className="secondary small">查看</button></Link></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
