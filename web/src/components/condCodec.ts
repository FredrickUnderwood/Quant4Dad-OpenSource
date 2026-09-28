import type { CondExpr, Operand } from '../types';

// ---------------------------------------------------------------------------
// Conversion between the visual CondExpr tree (used by the editor) and the
// JSON shape expected by internal/expr/parser.go.
//
// JSON form (single-key object per node):
//   { all: [child, ...] } / { any: [...] } / { not: child }
//   { gt: [lhs, rhs] } (and lt/gte/lte/eq)
//   { cross_up: [a, b] } / { cross_down: [a, b] }
//   { has_position: null }
// Operands inside cmp/cross are: a string (RefNode for indicator alias / OHLCV),
// a number (ConstNode), or a nested object for portfolio-state values:
//   { pnl_pct: null } / { days_held: null } / { ratio_from_entry: "close" }
// ---------------------------------------------------------------------------

export function condToJSON(c: CondExpr): any {
  switch (c.kind) {
    case 'all': return { all: c.children.map(condToJSON) };
    case 'any': return { any: c.children.map(condToJSON) };
    case 'not': return { not: condToJSON(c.child) };
    case 'cmp': return { [c.op]: [operandToJSON(c.lhs), operandToJSON(c.rhs)] };
    case 'cross': return { [c.dir]: [operandToJSON(c.a), operandToJSON(c.b)] };
    case 'call': return { [c.op]: null };
  }
}

function operandToJSON(o: Operand): any {
  switch (o.type) {
    case 'ref':   return o.name;
    case 'const': return o.value;
    case 'call':  return { [o.op]: null };
    case 'ratio': return { ratio_from_entry: o.field };
  }
}

const CMP_OPS = new Set(['gt', 'lt', 'gte', 'lte', 'eq']);
const CROSS_OPS = new Set(['cross_up', 'cross_down']);
const OHLCV = new Set(['open', 'high', 'low', 'close']);

export function jsonToCond(raw: any): CondExpr {
  if (!raw || typeof raw !== 'object') return defaultCond();
  const keys = Object.keys(raw);
  if (keys.length !== 1) return defaultCond();
  const op = keys[0]; const arg = raw[op];
  if (op === 'all' || op === 'any') {
    const arr = Array.isArray(arg) ? arg : [];
    return { kind: op, children: arr.map(jsonToCond) };
  }
  if (op === 'not') return { kind: 'not', child: jsonToCond(arg) };
  if (CMP_OPS.has(op)) {
    const [l, r] = Array.isArray(arg) ? arg : [];
    return { kind: 'cmp', op: op as any, lhs: parseOperand(l), rhs: parseOperand(r) };
  }
  if (CROSS_OPS.has(op)) {
    const [a, b] = Array.isArray(arg) ? arg : [];
    return { kind: 'cross', dir: op as any, a: parseOperand(a), b: parseOperand(b) };
  }
  if (op === 'has_position') return { kind: 'call', op: 'has_position' };
  // Backwards compatibility: pnl_pct, days_held and ratio_from_entry used to appear as
  // standalone leaves but can now only appear as operands. Historical data in that form is
  // upgraded to a cmp comparing against 0, which keeps the rule editable and loses no fields.
  if (op === 'pnl_pct' || op === 'days_held') {
    return { kind: 'cmp', op: 'gt', lhs: { type: 'call', op }, rhs: { type: 'const', value: 0 } };
  }
  if (op === 'ratio_from_entry') {
    const field = typeof arg === 'string' && OHLCV.has(arg) ? arg : 'close';
    return { kind: 'cmp', op: 'gt', lhs: { type: 'ratio', field: field as any }, rhs: { type: 'const', value: 1 } };
  }
  return defaultCond();
}

function parseOperand(v: any): Operand {
  if (typeof v === 'number') return { type: 'const', value: v };
  if (typeof v === 'string') return { type: 'ref', name: v };
  if (v && typeof v === 'object') {
    const keys = Object.keys(v);
    if (keys.length === 1) {
      const k = keys[0];
      if (k === 'pnl_pct' || k === 'days_held') return { type: 'call', op: k };
      if (k === 'ratio_from_entry') {
        const f = typeof v[k] === 'string' && OHLCV.has(v[k]) ? v[k] : 'close';
        return { type: 'ratio', field: f as any };
      }
    }
  }
  return { type: 'const', value: 0 };
}

export function defaultCond(): CondExpr {
  return { kind: 'cmp', op: 'gt', lhs: { type: 'ref', name: 'close' }, rhs: { type: 'ref', name: '' } };
}

// All node kinds shown in the kind dropdown, grouped logically.
export const COND_KIND_OPTIONS: { value: CondExpr['kind']; label: string }[] = [
  { value: 'all',   label: '全部满足 (AND)' },
  { value: 'any',   label: '任一满足 (OR)' },
  { value: 'not',   label: '取反 (NOT)' },
  { value: 'cmp',   label: '比较 (>, <, =)' },
  { value: 'cross', label: '交叉 (金叉/死叉)' },
  { value: 'call',  label: '持有仓位 (has_position)' },
];

// Build a fresh empty node when the user picks a kind from the dropdown.
export function emptyCond(kind: CondExpr['kind']): CondExpr {
  switch (kind) {
    case 'all': return { kind: 'all', children: [] };
    case 'any': return { kind: 'any', children: [] };
    case 'not': return { kind: 'not', child: defaultCond() };
    case 'cmp': return defaultCond();
    case 'cross': return { kind: 'cross', dir: 'cross_up', a: { type: 'ref', name: '' }, b: { type: 'ref', name: '' } };
    case 'call': return { kind: 'call', op: 'has_position' };
  }
}
