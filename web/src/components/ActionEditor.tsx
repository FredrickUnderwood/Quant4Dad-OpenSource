import type { ActionSpec, SizeSpec } from '../types';
import Select from './Select';

interface Props {
  value: ActionSpec;
  onChange: (next: ActionSpec) => void;
}

type SizeMode = 'all' | 'pct_of_cash' | 'pct_of_position' | 'shares' | 'fixed_cash';

const BUY_MODES: { value: SizeMode; label: string }[] = [
  { value: 'all',         label: '全部仓位 (all)' },
  { value: 'pct_of_cash', label: '按现金比例 (pct_of_cash)' },
  { value: 'fixed_cash',  label: '固定金额 (fixed_cash)' },
  { value: 'shares',      label: '固定股数 (shares)' },
];

const SELL_MODES: { value: SizeMode; label: string }[] = [
  { value: 'all',             label: '清仓 (all)' },
  { value: 'pct_of_position', label: '按持仓比例 (pct_of_position)' },
  { value: 'fixed_cash',      label: '固定金额 (fixed_cash)' },
  { value: 'shares',          label: '固定股数 (shares)' },
];

function sizeMode(s: SizeSpec): SizeMode {
  if (s === 'all') return 'all';
  if ('pct_of_cash' in s) return 'pct_of_cash';
  if ('pct_of_position' in s) return 'pct_of_position';
  if ('shares' in s) return 'shares';
  if ('fixed_cash' in s) return 'fixed_cash';
  return 'all';
}

function sizeValue(s: SizeSpec): number {
  if (s === 'all') return 0;
  if ('pct_of_cash' in s) return s.pct_of_cash;
  if ('pct_of_position' in s) return s.pct_of_position;
  if ('shares' in s) return s.shares;
  if ('fixed_cash' in s) return s.fixed_cash;
  return 0;
}

function buildSize(mode: SizeMode, n: number): SizeSpec {
  switch (mode) {
    case 'all': return 'all';
    case 'pct_of_cash': return { pct_of_cash: n };
    case 'pct_of_position': return { pct_of_position: n };
    case 'shares': return { shares: Math.round(n) };
    case 'fixed_cash': return { fixed_cash: n };
  }
}

function defaultValueFor(mode: SizeMode): number {
  if (mode === 'pct_of_cash' || mode === 'pct_of_position') return 1;
  return 100;
}

export default function ActionEditor({ value, onChange }: Props) {
  const modes = value.action === 'sell' ? SELL_MODES : BUY_MODES;
  let mode = sizeMode(value.size);
  // Switching to sell while pct_of_cash is selected — which is buy-side semantics — falls back
  // to all, so the backend does not reject the order.
  if (!modes.some(m => m.value === mode)) {
    mode = 'all';
  }
  const num = sizeValue(value.size);
  const isPct = mode === 'pct_of_cash' || mode === 'pct_of_position';

  const onActionChange = (next: 'buy' | 'sell') => {
    if (next === value.action) return;
    const allowed = next === 'sell' ? SELL_MODES : BUY_MODES;
    const stillValid = allowed.some(m => m.value === sizeMode(value.size));
    onChange({
      action: next,
      size: stillValid ? value.size : 'all',
    });
  };

  return (
    <div className="inline" style={{ flexWrap: 'wrap', gap: 8 }}>
      <label style={{ margin: 0 }}>动作</label>
      <Select
        compact
        value={value.action}
        onChange={v => onActionChange(v as 'buy' | 'sell')}
        options={[
          { value: 'buy',  label: '买入' },
          { value: 'sell', label: '卖出' },
        ]}
        minWidth={90}
      />

      <label style={{ margin: 0 }}>数量</label>
      <Select
        compact
        value={mode}
        onChange={v => onChange({ ...value, size: buildSize(v as SizeMode, num || defaultValueFor(v as SizeMode)) })}
        options={modes}
        minWidth={200}
      />

      {mode !== 'all' && (
        <input
          type="number" step={isPct ? '0.01' : '1'}
          style={{ width: 100 }}
          value={num}
          onChange={e => onChange({ ...value, size: buildSize(mode, parseFloat(e.target.value) || 0) })}
        />
      )}
      {mode === 'pct_of_cash' && <span className="muted">（0~1，比如 0.5 = 用 50% 现金买入）</span>}
      {mode === 'pct_of_position' && <span className="muted">（0~1，比如 0.5 = 卖掉 50% 持仓）</span>}
    </div>
  );
}
