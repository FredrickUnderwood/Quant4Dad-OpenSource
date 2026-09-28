import type { CondExpr, IndicatorSpec, IndicatorMeta, Operand } from '../types';
import type { SelectOption } from './Select';
import { COND_KIND_OPTIONS, defaultCond, emptyCond } from './condCodec';
import Select from './Select';

interface Props {
  value: CondExpr;
  onChange: (next: CondExpr) => void;
  onRemove?: () => void;
  indicators: IndicatorSpec[];
  metas: IndicatorMeta[];
}

// Display labels for the OHLCV fields in the dropdown.
const OHLCV_LABELS: Record<string, string> = {
  close:  '收盘价',
  open:   '开盘价',
  high:   '最高价',
  low:    '最低价',
  volume: '成交量',
};

// The virtual position-related operands. They appear as a special group in the reference
// dropdown, and selecting one produces a non-ref Operand variant. Their values are prefixed with
// `__call:` or `__ratio:` so they cannot collide with a user's indicator alias.
const PORTFOLIO_OPTIONS: { value: string; label: string; operand: Operand }[] = [
  { value: '__call:pnl_pct',    label: '浮动盈亏率',   operand: { type: 'call',  op: 'pnl_pct' } },
  { value: '__call:days_held',  label: '持仓天数',     operand: { type: 'call',  op: 'days_held' } },
  { value: '__ratio:close',     label: '收盘价 ÷ 入场价', operand: { type: 'ratio', field: 'close' } },
  { value: '__ratio:open',      label: '开盘价 ÷ 入场价', operand: { type: 'ratio', field: 'open' } },
  { value: '__ratio:high',      label: '最高价 ÷ 入场价', operand: { type: 'ratio', field: 'high' } },
  { value: '__ratio:low',       label: '最低价 ÷ 入场价', operand: { type: 'ratio', field: 'low' } },
];

// Map the current operand back to the dropdown's value string.
function operandToSelectValue(o: Operand): string {
  switch (o.type) {
    case 'ref':   return o.name;
    case 'call':  return `__call:${o.op}`;
    case 'ratio': return `__ratio:${o.field}`;
    case 'const': return '';
  }
}

function selectValueToOperand(v: string): Operand {
  const hit = PORTFOLIO_OPTIONS.find(p => p.value === v);
  if (hit) return hit.operand;
  return { type: 'ref', name: v };
}

// Build every entry of the reference dropdown, group headers included.
function buildRefOptions(indicators: IndicatorSpec[], metas: IndicatorMeta[]): SelectOption[] {
  const out: SelectOption[] = [];
  out.push({ value: '__h:price', label: '价格', header: true });
  ['close', 'open', 'high', 'low', 'volume'].forEach(k => {
    out.push({ value: k, label: OHLCV_LABELS[k] });
  });
  out.push({ value: '__h:portfolio', label: '持仓', header: true });
  PORTFOLIO_OPTIONS.forEach(p => out.push({ value: p.value, label: p.label }));

  const metaByType: Record<string, string[]> = {};
  metas.forEach(m => { metaByType[m.name.toUpperCase()] = m.outputs || []; });
  const aliasRefs: { value: string; label: string }[] = [];
  indicators.forEach(ind => {
    if (!ind.alias) return;
    const outs = metaByType[(ind.type || '').toUpperCase()] || ['value'];
    if (outs.length <= 1) aliasRefs.push({ value: ind.alias, label: ind.alias });
    else outs.forEach(o => aliasRefs.push({ value: `${ind.alias}.${o}`, label: `${ind.alias}.${o}` }));
  });
  if (aliasRefs.length) {
    out.push({ value: '__h:indicator', label: '指标', header: true });
    aliasRefs.forEach(r => out.push(r));
  }
  return out;
}

function OperandEditor({ value, onChange, refOptions }: {
  value: Operand;
  onChange: (next: Operand) => void;
  refOptions: SelectOption[];
}) {
  // At the top level an operand is only ever a reference or a constant; the position metrics
  // appear as a special group inside the reference dropdown.
  const topType: 'ref' | 'const' = value.type === 'const' ? 'const' : 'ref';
  const curValue = operandToSelectValue(value);
  // When editing historical data, curValue may be absent from options — a retired alias — so it
  // is filled back in as a fallback.
  const knownValues = new Set(refOptions.filter(o => !o.header).map(o => o.value));
  const refOpts = knownValues.has(curValue) || !curValue
    ? refOptions
    : [{ value: curValue, label: curValue }, ...refOptions];

  return (
    <span className="inline">
      <Select
        compact
        value={topType}
        onChange={v => onChange(
          v === 'const'
            ? { type: 'const', value: 0 }
            : { type: 'ref', name: 'close' }
        )}
        options={[
          { value: 'ref',   label: '引用' },
          { value: 'const', label: '常数' },
        ]}
        minWidth={80}
      />
      {topType === 'ref' ? (
        <Select
          compact
          value={curValue}
          onChange={v => onChange(selectValueToOperand(v))}
          options={refOpts}
          placeholder="（请选）"
          minWidth={140}
        />
      ) : (
        <input
          type="number" step="any" style={{ width: 90 }}
          value={value.type === 'const' ? value.value : 0}
          onChange={e => onChange({ type: 'const', value: parseFloat(e.target.value) || 0 })}
        />
      )}
    </span>
  );
}

export default function ConditionEditor({ value, onChange, onRemove, indicators, metas }: Props) {
  const refOptions = buildRefOptions(indicators, metas);
  const kindClass = value.kind === 'all' ? 'group-all'
                  : value.kind === 'any' ? 'group-any'
                  : value.kind === 'not' ? 'group-not'
                  : 'leaf';

  const changeKind = (kind: CondExpr['kind']) => onChange(emptyCond(kind));

  return (
    <div className={`cond-node ${kindClass}`}>
      <div className="cond-header">
        <Select
          compact
          value={value.kind}
          onChange={v => changeKind(v as CondExpr['kind'])}
          options={COND_KIND_OPTIONS.map(o => ({ value: o.value, label: o.label }))}
          minWidth={160}
        />

        {/* leaf-specific inline editors --------------------------------- */}
        {value.kind === 'cmp' && (
          <>
            <OperandEditor value={value.lhs} refOptions={refOptions} onChange={lhs => onChange({ ...value, lhs })} />
            <Select
              compact
              value={value.op}
              onChange={v => onChange({ ...value, op: v as any })}
              options={[
                { value: 'gt',  label: '>' },
                { value: 'gte', label: '>=' },
                { value: 'lt',  label: '<' },
                { value: 'lte', label: '<=' },
                { value: 'eq',  label: '==' },
              ]}
              minWidth={70}
            />
            <OperandEditor value={value.rhs} refOptions={refOptions} onChange={rhs => onChange({ ...value, rhs })} />
          </>
        )}

        {value.kind === 'cross' && (
          <>
            <OperandEditor value={value.a} refOptions={refOptions} onChange={a => onChange({ ...value, a })} />
            <Select
              compact
              value={value.dir}
              onChange={v => onChange({ ...value, dir: v as any })}
              options={[
                { value: 'cross_up',   label: '金叉 ↗' },
                { value: 'cross_down', label: '死叉 ↘' },
              ]}
              minWidth={100}
            />
            <OperandEditor value={value.b} refOptions={refOptions} onChange={b => onChange({ ...value, b })} />
          </>
        )}

        {value.kind === 'call' && (
          <span className="cond-op-label muted">当前已持有该标的的仓位</span>
        )}

        {(value.kind === 'all' || value.kind === 'any') && (
          <span className="cond-op-label">{value.kind === 'all' ? '需要全部成立' : '满足任一即可'}</span>
        )}

        <span style={{ flex: 1 }} />
        {onRemove && (
          <button className="ghost small" title="删除该条件" onClick={onRemove}>✕</button>
        )}
      </div>

      {/* recursive children ------------------------------------------------ */}
      {(value.kind === 'all' || value.kind === 'any') && (
        <div className="cond-children">
          {value.children.map((child, i) => (
            <ConditionEditor
              key={i} value={child}
              onChange={next => {
                const arr = value.children.slice();
                arr[i] = next;
                onChange({ ...value, children: arr });
              }}
              onRemove={() => onChange({ ...value, children: value.children.filter((_, j) => j !== i) })}
              indicators={indicators} metas={metas}
            />
          ))}
          <button
            className="secondary small"
            onClick={() => onChange({ ...value, children: [...value.children, defaultCond()] })}
          >+ 添加子条件</button>
        </div>
      )}

      {value.kind === 'not' && (
        <div className="cond-children">
          <ConditionEditor
            value={value.child}
            onChange={child => onChange({ ...value, child })}
            indicators={indicators} metas={metas}
          />
        </div>
      )}
    </div>
  );
}
