import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';

export interface SelectOption {
  value: string;
  label: string;
  description?: string;
  // header: a non-selectable group heading, shown only, skipped by keyboard focus and clicks.
  header?: boolean;
}

interface Props {
  value: string;
  onChange: (v: string) => void;
  options: SelectOption[];
  // Fallback label when the value is absent from options, which historical data can contain
  // after a value is retired.
  placeholder?: string;
  // compact: the smaller style, for dense rows such as cond-node and indicator-card.
  compact?: boolean;
  // The trigger's minimum width, overriding the default.
  minWidth?: number;
  // Extra styles for the trigger, e.g. to fill the parent's width.
  style?: React.CSSProperties;
  disabled?: boolean;
  // The title, shown on hovering the trigger.
  title?: string;
}

// A custom dropdown replacing the native <select>, styled to the quant4dad theme.
// Behavior: clicking the trigger opens and closes the popup; selecting closes it and fires the
// callback; Esc closes it; an outside click closes it. The popup is positioned fixed, so a
// parent's overflow cannot clip it.
export default function Select({
  value, onChange, options, placeholder, compact, minWidth, style, disabled, title,
}: Props) {
  const [open, setOpen] = useState(false);
  const [hover, setHover] = useState<number>(-1);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const popRef = useRef<HTMLDivElement | null>(null);
  const [popStyle, setPopStyle] = useState<React.CSSProperties>({});

  // Index stepping that skips header entries.
  const stepHover = (cur: number, dir: 1 | -1): number => {
    const n = options.length;
    if (n === 0) return -1;
    let i = cur;
    for (let k = 0; k < n; k++) {
      i = (i + dir + n) % n;
      if (!options[i]?.header) return i;
    }
    return -1;
  };
  const firstSelectableFrom = (start: number): number => {
    for (let i = Math.max(0, start); i < options.length; i++) {
      if (!options[i]?.header) return i;
    }
    for (let i = 0; i < options.length; i++) {
      if (!options[i]?.header) return i;
    }
    return -1;
  };

  // Compute the popup's position: laid out against the trigger, with its width floored at min.
  // On the first open, popStyle must be computed in the same frame as setOpen. Otherwise the
  // popup enters normal document flow with style={} on that first render, paints somewhere just
  // below the input, and is then yanked back by useLayoutEffect — which looks like "the first
  // click doesn't line it up and the second one does".
  const computePopStyle = useCallback((): React.CSSProperties | null => {
    if (!triggerRef.current) return null;
    const r = triggerRef.current.getBoundingClientRect();
    const width = Math.max(r.width, 220);
    const maxH = 280;
    const spaceBelow = window.innerHeight - r.bottom;
    const below = spaceBelow > 160 || spaceBelow > maxH / 2;
    const top = below ? r.bottom + 4 : Math.max(8, r.top - 4 - maxH);
    return {
      position: 'fixed',
      left: r.left,
      top,
      width,
      maxHeight: maxH,
      transformOrigin: below ? 'top left' : 'bottom left',
    };
  }, []);

  const reposition = useCallback(() => {
    const s = computePopStyle();
    if (s) setPopStyle(s);
  }, [computePopStyle]);

  // Closing: an outside click, Esc, or keyboard navigation.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      const t = e.target as Node;
      if (popRef.current?.contains(t) || triggerRef.current?.contains(t)) return;
      setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
      if (e.key === 'ArrowDown') { e.preventDefault(); setHover(h => stepHover(h < 0 ? -1 : h, 1)); }
      if (e.key === 'ArrowUp')   { e.preventDefault(); setHover(h => stepHover(h < 0 ? options.length : h, -1)); }
      if (e.key === 'Enter' && hover >= 0 && !options[hover]?.header) {
        e.preventDefault();
        onChange(options[hover].value);
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', onDoc);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDoc);
      document.removeEventListener('keydown', onKey);
    };
  }, [open, hover, options, onChange]);

  // Stay aligned after the option list changes, or after an async initialization.
  useLayoutEffect(() => {
    if (!open) return;
    reposition();
  }, [open, options.length, reposition]);

  // Keep the popup positioned on scroll and resize, so it cannot come unstuck from the trigger.
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

  const cur = options.find(o => o.value === value && !o.header);
  const label = cur?.label ?? value ?? '';

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={`q-select-trigger${compact ? ' compact' : ''}${open ? ' open' : ''}`}
        style={{ minWidth: minWidth ?? (compact ? 100 : 140), ...style }}
        disabled={disabled}
        title={title}
        onClick={() => {
          if (disabled) return;
          if (!open) {
            const s = computePopStyle();
            if (s) setPopStyle(s);
            const idx = options.findIndex(o => o.value === value && !o.header);
            setHover(idx >= 0 ? idx : firstSelectableFrom(0));
            setOpen(true);
          } else {
            setOpen(false);
          }
        }}
      >
        <span className={`q-select-label${cur ? '' : ' placeholder'}`}>
          {label || placeholder || '请选择'}
        </span>
        <span className="q-select-chev" aria-hidden>▾</span>
      </button>

      {open && (
        <div ref={popRef} className="q-select-pop" style={popStyle} role="listbox">
          {options.length === 0 && <div className="q-select-empty">无可选项</div>}
          {options.map((o, i) => (
            o.header ? (
              <div key={`h-${i}`} className="q-select-group">{o.label}</div>
            ) : (
              <div
                key={o.value}
                role="option"
                aria-selected={o.value === value}
                className={
                  'q-select-opt' +
                  (o.value === value ? ' selected' : '') +
                  (i === hover ? ' active' : '')
                }
                onMouseEnter={() => setHover(i)}
                onClick={() => { onChange(o.value); setOpen(false); }}
              >
                <span className="q-select-opt-label">{o.label}</span>
                {o.description && (
                  <span className="q-select-opt-desc">{o.description}</span>
                )}
              </div>
            )
          ))}
        </div>
      )}
    </>
  );
}
