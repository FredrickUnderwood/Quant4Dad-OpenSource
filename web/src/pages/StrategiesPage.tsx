import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { api } from '../api';
import type { Strategy } from '../types';

export default function StrategiesPage() {
  const [items, setItems] = useState<Strategy[]>([]);
  const [err, setErr] = useState('');
  const nav = useNavigate();

  const reload = () => {
    api.listStrategies().then(setItems).catch(e => setErr(String(e.message || e)));
  };
  useEffect(reload, []);

  const onDelete = async (id: number) => {
    if (!confirm('确定删除这条策略？')) return;
    await api.deleteStrategy(id);
    reload();
  };

  const onBacktest = (s: Strategy) => nav(`/strategies/${s.id}/backtest`);

  // body may be an object or the JSON string the backend returned; only mode is needed, for the
  // badge.
  const modeOf = (s: Strategy): 'script' | 'config' => {
    try {
      const body: any = typeof s.body === 'string' ? JSON.parse(s.body) : s.body;
      return body?.mode === 'script' ? 'script' : 'config';
    } catch { return 'config'; }
  };

  return (
    <section>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h2 style={{ border: 'none', padding: 0, margin: 0 }}>策略列表</h2>
        <Link to="/strategies/new"><button>+ 新建策略</button></Link>
      </div>
      {err && <p className="error">{err}</p>}
      {items.length === 0 ? (
        <p className="empty">还没有策略，点右上角「新建策略」开始可视化编排吧。</p>
      ) : (
        <table>
          <thead><tr><th>ID</th><th>名称</th><th>模式</th><th>标的</th><th>周期</th><th>更新时间</th><th>操作</th></tr></thead>
          <tbody>
            {items.map(s => (
              <tr key={s.id}>
                <td>{s.id}</td>
                <td>{s.name}</td>
                <td>{modeOf(s) === 'script'
                  ? <span className="tag running">脚本</span>
                  : <span className="tag">配置</span>}</td>
                <td>{(s.universe || []).join(', ')}</td>
                <td>{s.period}</td>
                <td>{(s.updated_at || '').slice(0, 19).replace('T', ' ')}</td>
                <td>
                  <Link to={`/strategies/${s.id}/edit`}><button className="secondary small">编辑</button></Link>{' '}
                  <button className="small" onClick={() => onBacktest(s)}>回测</button>{' '}
                  <button className="danger small" onClick={() => onDelete(s.id)}>删除</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
