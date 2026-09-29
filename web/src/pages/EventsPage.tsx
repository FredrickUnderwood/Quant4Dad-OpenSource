import { useEffect, useMemo, useState } from 'react';
import { api } from '../api';
import type { Pipeline, PipelineEvent, EventStatus } from '../types';
import Select from '../components/Select';

const STATUS_LABEL: Record<EventStatus, string> = {
  processing: '处理中', passed: '通过', dropped: '已丢弃', failed: '失败',
};

function EventStatusTag({ s }: { s: EventStatus }) {
  const cls = s === 'passed' ? 'success' : s === 'failed' || s === 'dropped' ? 'failed' : 'running';
  return <span className={`tag ${cls}`}>{STATUS_LABEL[s] ?? s}</span>;
}

function fmtTs(s?: string | null) {
  if (!s) return '—';
  return s.slice(0, 19).replace('T', ' ');
}

export default function EventsPage() {
  const [pipelines, setPipelines] = useState<Pipeline[]>([]);
  const [pipelineId, setPipelineId] = useState<string>('');
  const [status, setStatus] = useState<string>('');
  const [start, setStart] = useState<string>('');
  const [end, setEnd] = useState<string>('');
  const [items, setItems] = useState<PipelineEvent[] | null>(null);
  const [total, setTotal] = useState(0);
  const [err, setErr] = useState('');
  const [exporting, setExporting] = useState(false);
  const [detail, setDetail] = useState<PipelineEvent | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const pipelineName = useMemo(() => {
    const m: Record<number, string> = {};
    pipelines.forEach((p) => { m[p.id] = p.name; });
    return m;
  }, [pipelines]);

  useEffect(() => {
    api.listPipelines().then(setPipelines).catch(() => { /* the filter is optional, so a failure is ignored */ });
  }, []);

  const load = () => {
    setItems(null);
    api.listEvents({
      pipeline_id: pipelineId ? Number(pipelineId) : undefined,
      status: status || undefined,
      start: start || undefined,
      end: end || undefined,
      limit: 100,
    })
      .then((r) => { setItems(r.items ?? []); setTotal(r.total); })
      .catch((e) => setErr(String(e.message || e)));
  };
  useEffect(load, [pipelineId, status, start, end]);

  const doExport = (format: 'csv' | 'excel') => {
    setErr('');
    setExporting(true);
    api.exportEvents({
      pipeline_id: pipelineId ? Number(pipelineId) : undefined,
      status: status || undefined,
      start: start || undefined,
      end: end || undefined,
      format,
    })
      .catch((e) => setErr(String(e.message || e)))
      .finally(() => setExporting(false));
  };

  const openDetail = (id: number) => {
    setDetailLoading(true);
    setDetail(null);
    api.getEvent(id)
      .then(setDetail)
      .catch((e) => setErr(String(e.message || e)))
      .finally(() => setDetailLoading(false));
  };

  return (
    <section>
      <h2>事件记录 <span className="muted">共 {total}</span></h2>

      <div className="row" style={{ marginBottom: 16, alignItems: 'flex-end' }}>
        <div style={{ maxWidth: 240 }}>
          <label>流水线</label>
          <Select
            value={pipelineId}
            onChange={setPipelineId}
            placeholder="全部"
            style={{ width: '100%' }}
            options={[{ value: '', label: '全部流水线' }, ...pipelines.map((p) => ({ value: String(p.id), label: p.name }))]}
          />
        </div>
        <div style={{ maxWidth: 200 }}>
          <label>状态</label>
          <Select
            value={status}
            onChange={setStatus}
            placeholder="全部"
            style={{ width: '100%' }}
            options={[
              { value: '', label: '全部状态' },
              { value: 'passed', label: '通过' },
              { value: 'dropped', label: '已丢弃' },
              { value: 'failed', label: '失败' },
              { value: 'processing', label: '处理中' },
            ]}
          />
        </div>
        <div style={{ maxWidth: 160 }}>
          <label>开始日期</label>
          <input type="date" value={start} max={end || undefined} onChange={(e) => setStart(e.target.value)} style={{ width: '100%' }} />
        </div>
        <div style={{ maxWidth: 160 }}>
          <label>结束日期</label>
          <input type="date" value={end} min={start || undefined} onChange={(e) => setEnd(e.target.value)} style={{ width: '100%' }} />
        </div>
        <div className="inline" style={{ flex: '0 0 auto', gap: 8 }}>
          <button className="secondary" onClick={load}>刷新</button>
          <button className="secondary" disabled={exporting} onClick={() => doExport('csv')}>
            {exporting ? '导出中…' : '导出 CSV'}
          </button>
          <button className="secondary" disabled={exporting} onClick={() => doExport('excel')}>
            {exporting ? '导出中…' : '导出 Excel'}
          </button>
        </div>
      </div>

      {err && <div className="error">{err}</div>}

      {items === null ? (
        <div className="empty">载入中…</div>
      ) : items.length === 0 ? (
        <div className="empty">暂无事件</div>
      ) : (
        <div className="table-scroll">
          <table>
            <thead>
              <tr>
                <th>事件</th>
                <th>流水线</th>
                <th>来源</th>
                <th>状态</th>
                <th>接收时间</th>
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={e.id} style={{ cursor: 'pointer' }} onClick={() => openDetail(e.id)}>
                  <td className="mono">#{e.id}<div className="muted">{e.event_uid.slice(0, 12)}</div></td>
                  <td>{pipelineName[e.pipeline_id] ?? `#${e.pipeline_id}`}</td>
                  <td>{e.source}</td>
                  <td>
                    <EventStatusTag s={e.status} />
                    {e.dropped_at_node && <div className="muted">@ {e.dropped_at_node}</div>}
                  </td>
                  <td className="mono">{fmtTs(e.received_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {(detail || detailLoading) && (
        <EventDetailDrawer
          loading={detailLoading}
          evt={detail}
          pipelineName={detail ? (pipelineName[detail.pipeline_id] ?? `#${detail.pipeline_id}`) : ''}
          onClose={() => setDetail(null)}
        />
      )}
    </section>
  );
}

function EventDetailDrawer({
  loading, evt, pipelineName, onClose,
}: {
  loading: boolean;
  evt: PipelineEvent | null;
  pipelineName: string;
  onClose: () => void;
}) {
  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <div className="drawer" onClick={(e) => e.stopPropagation()}>
        <div className="drawer-head">
          <h3>{evt ? `事件 #${evt.id}` : '载入中…'}</h3>
          <button className="ghost" onClick={onClose}>关闭 ✕</button>
        </div>

        {loading || !evt ? (
          <div className="empty">载入中…</div>
        ) : (
          <div className="drawer-body">
            <div className="muted" style={{ marginBottom: 4 }}>{pipelineName} · 来源 {evt.source}</div>
            <div className="inline" style={{ marginBottom: 12 }}>
              <EventStatusTag s={evt.status} />
              {evt.dropped_at_node && <span className="muted">在 {evt.dropped_at_node} 丢弃</span>}
            </div>
            {evt.error && <div className="error" style={{ marginBottom: 12 }}>{evt.error}</div>}

            <h4>节点血缘</h4>
            {(evt.traces ?? []).length === 0 ? (
              <p className="muted">无</p>
            ) : (
              <div className="table-scroll">
                <table>
                  <thead><tr><th>节点</th><th>动作</th><th>耗时</th></tr></thead>
                  <tbody>
                    {(evt.traces ?? []).map((t) => (
                      <tr key={t.id}>
                        <td>{t.node_key}<div className="muted">{t.node_type}</div></td>
                        <td>
                          {t.action === 'drop'
                            ? <span className="tag failed">丢弃</span>
                            : t.error
                              ? <span className="tag warn">降级通过</span>
                              : <span className="tag success">通过</span>}
                          {t.error && <div className="error" style={{ marginTop: 4 }}>{t.error}</div>}
                        </td>
                        <td className="mono">{t.latency_ms}ms</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}

            {(evt.ai_results ?? []).length > 0 && (
              <>
                <h4 style={{ marginTop: 16 }}>AI 分析结果</h4>
                {(evt.ai_results ?? []).map((r) => (
                  <div key={r.id} className="ai-result-card">
                    <div className="inline" style={{ justifyContent: 'space-between' }}>
                      <span><strong>{r.node_key}</strong> · {r.provider} / {r.model}</span>
                      <span className="muted mono">{r.tokens_prompt}+{r.tokens_completion} tok · {r.latency_ms}ms</span>
                    </div>
                    <details style={{ marginTop: 6 }}>
                      <summary className="muted">提示词快照</summary>
                      <pre>{r.prompt_snapshot}</pre>
                    </details>
                    <div className="muted" style={{ marginTop: 6 }}>输出</div>
                    <pre>{JSON.stringify(r.output, null, 2)}</pre>
                  </div>
                ))}
              </>
            )}

            <h4 style={{ marginTop: 16 }}>原始 payload</h4>
            <pre>{JSON.stringify(evt.raw_payload, null, 2)}</pre>
            <h4 style={{ marginTop: 12 }}>最终 payload</h4>
            <pre>{JSON.stringify(evt.final_payload, null, 2)}</pre>
          </div>
        )}
      </div>
    </div>
  );
}
