import type { IndicatorSpec, IndicatorMeta } from '../types';
import Select from './Select';

interface Props {
  value: IndicatorSpec[];
  onChange: (next: IndicatorSpec[]) => void;
  metas: IndicatorMeta[];
}

// Default param hints per indicator type. Server-side calculators all read
// params with safe fallbacks, so these are only for prefilling the UI.
const DEFAULT_PARAMS: Record<string, Record<string, number | string>> = {
  MA:   { period: 20 },
  EMA:  { period: 20 },
  MACD: { fast: 12, slow: 26, signal: 9 },
  RSI:  { period: 14 },
  KDJ:  { period: 9, k: 3, d: 3 },
  BOLL: { period: 20, k: 2 },
  ATR:  { period: 14 },
};

// Short blurbs shown on hovering an indicator type, to convey what each one means.
const INDICATOR_DESC: Record<string, string> = {
  MA:   '简单移动平均线 (Moving Average)：最近 N 个收盘价的算术平均，用于平滑价格、刻画趋势。',
  EMA:  '指数移动平均 (Exponential MA)：对近期价格赋更高权重，对趋势变化反应比 MA 更灵敏。',
  MACD: 'MACD 异同移动平均：短期 EMA 与长期 EMA 之差及其信号线，用于识别趋势启动 / 反转。',
  RSI:  '相对强弱指数 (RSI)：在 0~100 区间度量近期涨跌力量对比，>70 视作超买、<30 视作超卖。',
  KDJ:  '随机指标 KDJ：用最高 / 最低 / 收盘价计算 K、D、J 三线，常用于震荡市抄底逃顶。',
  BOLL: '布林带 (Bollinger Bands)：均线上下各 k 倍标准差画通道，用于度量波动率与价格相对位置。',
  ATR:  '平均真实波幅 (ATR)：N 日真实波幅的均值，反映波动强度，常用于止损位与仓位计算。',
};

export default function IndicatorList({ value, onChange, metas }: Props) {
  const knownTypes = metas.map(m => m.name).sort();

  const update = (i: number, patch: Partial<IndicatorSpec>) => {
    const next = value.slice();
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };

  const setType = (i: number, type: string) => {
    update(i, { type, params: { ...(DEFAULT_PARAMS[type.toUpperCase()] || {}) } });
  };

  const add = () => {
    const type = knownTypes[0] || 'MA';
    onChange([...value, { alias: `ind${value.length + 1}`, type, params: { ...(DEFAULT_PARAMS[type.toUpperCase()] || {}) } }]);
  };

  // Turn the type list into Select options plus their blurbs.
  const typeOptions = (current: string) => {
    const all = knownTypes.includes(current) ? knownTypes : [current, ...knownTypes];
    return all.map(t => ({
      value: t,
      label: t,
      description: INDICATOR_DESC[t.toUpperCase()] || '',
    }));
  };

  return (
    <>
      {value.length === 0 && <p className="muted">还没有指标。点下面「添加指标」开始。</p>}
      {value.map((ind, i) => {
        const paramKeys = Object.keys(ind.params || {});
        const desc = INDICATOR_DESC[ind.type.toUpperCase()] || '';
        return (
          <div key={i} className="indicator-card">
            <label style={{ margin: 0 }}>别名</label>
            <input
              style={{ width: 100 }} value={ind.alias}
              onChange={e => update(i, { alias: e.target.value })}
            />
            <label style={{ margin: 0 }}>类型</label>
            <Select
              compact
              value={ind.type}
              onChange={v => setType(i, v)}
              options={typeOptions(ind.type)}
              title={desc}
            />
            {desc && (
              <span className="q-indicator-hint" title={desc} aria-label="指标说明">
                ⓘ
                <span className="q-indicator-tip">{desc}</span>
              </span>
            )}
            {paramKeys.map(k => (
              <span key={k} className="inline">
                <label style={{ margin: 0 }}>{k}</label>
                <input
                  style={{ width: 70 }}
                  value={String(ind.params[k] ?? '')}
                  onChange={e => {
                    const raw = e.target.value;
                    const num = parseFloat(raw);
                    update(i, { params: { ...ind.params, [k]: isNaN(num) ? raw : num } });
                  }}
                />
              </span>
            ))}
            <span style={{ flex: 1 }} />
            <button className="ghost small" onClick={() => onChange(value.filter((_, j) => j !== i))}>✕</button>
          </div>
        );
      })}
      <button className="secondary small" onClick={add}>+ 添加指标</button>
    </>
  );
}
