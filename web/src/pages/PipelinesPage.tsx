import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { api } from '../api';
import type { Pipeline, PipelineStatus } from '../types';

const STATUS_LABEL: Record<PipelineStatus, string> = {
  draft: '草稿', enabled: '已启用', disabled: '已停用',
};

export function StatusTag({ s }: { s: PipelineStatus | string }) {
  const cls = s === 'enabled' ? 'success' : s === 'disabled' ? 'failed' : 'running';
  return <span className={`tag ${cls}`}>{STATUS_LABEL[s as PipelineStatus] ?? s}</span>;
}

function fmtTs(s?: string | null) {
  if (!s) return '—';
  return s.slice(0, 19).replace('T', ' ');
}

export default function PipelinesPage() {
  const nav = useNavigate();
  const [items, setItems] = useState<Pipeline[] | null>(null);
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState<number | null>(null);

  const load = () => {
    api.listPipelines()
      .then(setItems)
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, []);

  const toggle = async (p: Pipeline) => {
    const next: PipelineStatus = p.status === 'enabled' ? 'disabled' : 'enabled';
    setBusy(p.id);
    try {
      await api.setPipelineStatus(p.id, next, p.version);
      load();
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (p: Pipeline) => {
    if (!window.confirm(`删除流水线「${p.name}」？此操作不可撤销。`)) return;
    setBusy(p.id);
    try {
      await api.deletePipeline(p.id);
      load();
    } catch (e) {
      setErr(String((e as Error).message || e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section>
      <h2>
        事件流水线
        <span className="inline" style={{ float: 'right', gap: 10 }}>
          <button onClick={() => nav('/pipelines/new')}>新建流水线</button>
        </span>
      </h2>

      {err && <div className="error">{err}</div>}

      {items === null ? (
        <div className="empty">载入中…</div>
      ) : items.length === 0 ? (
        <div className="empty">还没有流水线 — 点「新建流水线」开始编排</div>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th style={{ width: '42%' }}>名称</th>
                <th>状态</th>
                <th>版本</th>
                <th>更新时间</th>
                <th style={{ textAlign: 'right' }}>操作</th>
              </tr>
            </thead>
            <tbody>
              {items.map((p) => (
                <tr key={p.id} style={{ cursor: 'pointer' }} onClick={() => nav(`/pipelines/${p.id}/edit`)}>
                  <td>
                    <div style={{ fontWeight: 600 }}>{p.name}</div>
                    {p.description && (
                      <div className="muted" style={{ whiteSpace: 'normal', wordBreak: 'break-word', lineHeight: 1.5 }}>
                        {p.description}
                      </div>
                    )}
                  </td>
                  <td><StatusTag s={p.status} /></td>
                  <td className="mono">v{p.version}</td>
                  <td className="mono">{fmtTs(p.updated_at)}</td>
                  <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }} onClick={(e) => e.stopPropagation()}>
                    <span className="inline" style={{ gap: 6, justifyContent: 'flex-end', flexWrap: 'nowrap' }}>
                      <button className="secondary small" disabled={busy === p.id} onClick={() => toggle(p)}>
                        {p.status === 'enabled' ? '停用' : '启用'}
                      </button>
                      <button className="secondary small" onClick={() => nav(`/pipelines/${p.id}/edit`)}>编辑</button>
                      <button className="danger small" disabled={busy === p.id} onClick={() => remove(p)}>删除</button>
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
