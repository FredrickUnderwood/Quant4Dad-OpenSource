import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { api } from '../api';
import type { Instrument } from '../types';

interface Props {
  value: string[];                       // the selected instrument codes
  onChange: (codes: string[]) => void;
}

// Instrument search with multi-select. The backend's /api/v1/instruments?keyword= matches
// loosely on code or name. Focusing or typing in the input opens the candidate list, clicking
// one adds it as a chip, and anything already selected drops out of the candidates.
export default function StockPicker({ value, onChange }: Props) {
  const [kw, setKw] = useState('');
  const [items, setItems] = useState<Instrument[]>([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [hover, setHover] = useState(-1);
  // A code -> name cache for the selected items, used to label the chips, since getStrategy
  // returns codes alone.
  const [nameCache, setNameCache] = useState<Record<string, string>>({});

  const boxRef = useRef<HTMLDivElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const popRef = useRef<HTMLDivElement | null>(null);
  const [popStyle, setPopStyle] = useState<React.CSSProperties>({});

  // Debounced search: only fire a request 200ms after typing stops.
  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    const t = setTimeout(() => {
      api.searchInstruments(kw, 30)
        .then(res => { if (!cancelled) setItems(res.items || []); })
        .catch(() => { if (!cancelled) setItems([]); })
        .finally(() => { if (!cancelled) setLoading(false); });
    }, 200);
    return () => { cancelled = true; clearTimeout(t); };
  }, [kw]);

  // Fetch names separately for any selected code missing from the cache, so its chip can be
  // labelled.
  useEffect(() => {
    const missing = value.filter(c => !(c in nameCache));
    if (missing.length === 0) return;
    let cancelled = false;
    Promise.all(missing.map(c => api.getInstrument(c).catch(() => null))).then(rs => {
      if (cancelled) return;
      const patch: Record<string, string> = {};
      rs.forEach((it, i) => { if (it) patch[missing[i]] = it.name; });
      if (Object.keys(patch).length) setNameCache(prev => ({ ...prev, ...patch }));
    });
    return () => { cancelled = true; };
  }, [value]); // eslint-disable-line react-hooks/exhaustive-deps

  // Close on an outside click or Esc.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      const t = e.target as Node;
      if (boxRef.current?.contains(t) || popRef.current?.contains(t)) return;
      setOpen(false);
    };
    document.addEventListener('mousedown', onDoc);
    return () => document.removeEventListener('mousedown', onDoc);
  }, [open]);

  const reposition = useCallback(() => {
    if (!boxRef.current) return;
    const r = boxRef.current.getBoundingClientRect();
    setPopStyle({
      position: 'fixed', left: r.left, top: r.bottom + 4,
      width: r.width, maxHeight: 320,
    });
  }, []);

  useLayoutEffect(() => {
    if (!open) return;
    reposition();
  }, [open, kw, items.length, value.length, reposition]);

  // Keep the popup positioned on scroll and resize, using capture to catch inner scrollable
  // containers.
  useEffect(() => {
    if (!open) return;
    const onScroll = () => reposition();
    const onResize = () => reposition();
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  }, [open, reposition]);

  const selectedSet = new Set(value);
  const candidates = items.filter(it => !selectedSet.has(it.code));

  const add = (it: Instrument) => {
    if (selectedSet.has(it.code)) return;
    setNameCache(prev => ({ ...prev, [it.code]: it.name }));
    onChange([...value, it.code]);
    setKw('');
    inputRef.current?.focus();
  };

  const remove = (code: string) => {
    onChange(value.filter(c => c !== code));
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setOpen(true); setHover(h => Math.min(candidates.length - 1, h + 1)); }
    if (e.key === 'ArrowUp')   { e.preventDefault(); setHover(h => Math.max(0, h - 1)); }
    if (e.key === 'Enter')     {
      e.preventDefault();
      if (hover >= 0 && candidates[hover]) add(candidates[hover]);
      else if (candidates[0]) add(candidates[0]);
    }
    if (e.key === 'Escape')    { setOpen(false); }
    if (e.key === 'Backspace' && !kw && value.length) {
      e.preventDefault();
      remove(value[value.length - 1]);
    }
  };

  return (
    <div ref={boxRef} className="q-stock-picker">
      <div className="q-stock-chips">
        {value.map(code => (
          <span key={code} className="q-chip">
            <span className="q-chip-code">{code}</span>
            {nameCache[code] && <span className="q-chip-name">{nameCache[code]}</span>}
            <button type="button" className="q-chip-x" onClick={() => remove(code)} title="移除">✕</button>
          </span>
        ))}
        <input
          ref={inputRef}
          className="q-stock-input"
          value={kw}
          onChange={e => { setKw(e.target.value); setOpen(true); setHover(0); }}
          onFocus={() => setOpen(true)}
          onKeyDown={onKey}
          placeholder={value.length === 0 ? '搜索股票名称或代码，例如「茅台」或「600519」' : '继续添加…'}
        />
      </div>

      {open && (
        <div ref={popRef} className="q-stock-pop" style={popStyle}>
          {loading && <div className="q-select-empty">检索中…</div>}
          {!loading && candidates.length === 0 && (
            <div className="q-select-empty">{kw ? '没有匹配到股票' : '输入关键字搜索 A 股'}</div>
          )}
          {!loading && candidates.map((it, i) => (
            <div
              key={it.code}
              role="option"
              className={'q-stock-opt' + (i === hover ? ' active' : '')}
              onMouseEnter={() => setHover(i)}
              onClick={() => add(it)}
            >
              <span className="q-stock-opt-code">{it.code}</span>
              <span className="q-stock-opt-name">{it.name}</span>
              {it.industry && <span className="q-stock-opt-ind">{it.industry}</span>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
