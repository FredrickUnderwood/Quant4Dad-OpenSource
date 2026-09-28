import { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import type { News } from '../types';
import { NEWS_SOURCE_LABEL } from '../types';
import Select from '../components/Select';

const PAGE_SIZE = 50;

function fmtTs(s?: string | null) {
  if (!s) return '—';
  // The backend returns RFC3339 with a timezone; truncate to minutes for display.
  return s.slice(0, 16).replace('T', ' ');
}

function sourceLabel(s: string) {
  return NEWS_SOURCE_LABEL[s] ?? s;
}

export default function NewsPage() {
  const [source, setSource] = useState('');
  const [keyword, setKeyword] = useState('');
  const [debounced, setDebounced] = useState('');
  const [items, setItems] = useState<News[] | null>(null);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [err, setErr] = useState('');
  const [detail, setDetail] = useState<News | null>(null);
  const [sources, setSources] = useState<string[]>([]);

  // The selectable sources come from whatever the backend has registered rather than being
  // hardcoded here, so plugging in your own source makes it appear automatically.
  useEffect(() => {
    api.getNewsSources()
      .then((r) => setSources((r.sources ?? []).map((s) => s.source)))
      .catch(() => setSources([]));
  }, []);

  // Debounce the keyword input, so not every keystroke hits the API.
  useEffect(() => {
    const t = setTimeout(() => setDebounced(keyword.trim()), 300);
    return () => clearTimeout(t);
  }, [keyword]);

  const reqIdRef = useRef(0);
  const load = (reset: boolean) => {
    const nextOffset = reset ? 0 : offset;
    if (reset) { setItems(null); setErr(''); } else { setLoadingMore(true); }
    const myReq = ++reqIdRef.current;
    api.listNews({ source: source || undefined, keyword: debounced || undefined, limit: PAGE_SIZE, offset: nextOffset })
      .then((r) => {
        if (myReq !== reqIdRef.current) return; // discard a stale response
        const batch = r.items ?? [];
        setTotal(r.total);
        setItems((prev) => (reset || prev === null ? batch : [...prev, ...batch]));
        setOffset(nextOffset + batch.length);
      })
      .catch((e) => setErr(String(e.message || e)))
      .finally(() => setLoadingMore(false));
  };

  // Reset the list whenever the filter changes.
  useEffect(() => { load(true); /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [source, debounced]);

  const hasMore = items !== null && items.length < total;

  return (
    <section>
      <h2>资讯 <span className="muted">共 {total}</span></h2>

      <div className="row" style={{ marginBottom: 16, alignItems: 'flex-end', gap: 12 }}>
        <div style={{ maxWidth: 200 }}>
          <label>来源</label>
          <Select
            value={source}
            onChange={setSource}
            placeholder="全部"
            style={{ width: '100%' }}
            options={[
              { value: '', label: '全部来源' },
              ...sources.map((s) => ({ value: s, label: sourceLabel(s) })),
            ]}
          />
        </div>
        <div style={{ flex: 1, maxWidth: 320 }}>
          <label>关键词</label>
          <input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="标题搜索…"
            style={{ width: '100%' }}
          />
        </div>
        <div style={{ flex: '0 0 auto' }}>
          <button className="secondary" onClick={() => load(true)}>刷新</button>
        </div>
      </div>

      {err && <div className="error">{err}</div>}

      {items === null ? (
        <div className="empty">载入中…</div>
      ) : items.length === 0 ? (
        <div className="empty">暂无资讯</div>
      ) : (
        <>
          <ul className="news-list">
            {items.map((n) => (
              <li key={n.id} className="news-item" onClick={() => setDetail(n)} style={{ cursor: 'pointer' }}>
                <div className="news-meta">
                  <span className="tag">{sourceLabel(n.source)}</span>
                  <span className="mono muted">{fmtTs(n.published_at)}</span>
                </div>
                <div className="news-title">{n.title || n.content}</div>
              </li>
            ))}
          </ul>
          {hasMore && (
            <div style={{ textAlign: 'center', marginTop: 16 }}>
              <button className="secondary" disabled={loadingMore} onClick={() => load(false)}>
                {loadingMore ? '加载中…' : '加载更多'}
              </button>
            </div>
          )}
        </>
      )}

      {detail && <NewsDetailDrawer news={detail} onClose={() => setDetail(null)} />}
    </section>
  );
}

function NewsDetailDrawer({ news, onClose }: { news: News; onClose: () => void }) {
  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <div className="drawer" onClick={(e) => e.stopPropagation()}>
        <div className="drawer-head">
          <h3>{sourceLabel(news.source)}</h3>
          <button className="ghost" onClick={onClose}>关闭 ✕</button>
        </div>
        <div className="drawer-body">
          <div className="muted mono" style={{ marginBottom: 8 }}>{fmtTs(news.published_at)}</div>
          <h4 style={{ marginTop: 0 }}>{news.title}</h4>
          <p style={{ whiteSpace: 'pre-wrap', lineHeight: 1.7 }}>{news.content}</p>
          {news.url && (
            <p style={{ marginTop: 16 }}>
              <a href={news.url} target="_blank" rel="noreferrer">查看原文 ↗</a>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
