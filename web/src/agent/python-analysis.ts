import type { AgentToolResult } from './types.ts';
import { klineChartOption } from './kline-analysis.ts';
import type { CandleRow } from './kline-analysis.ts';
import { defaultChartPalette } from '../charts/palette.ts';
import type { ChartPalette } from '../charts/palette.ts';

type Cell = string | number | boolean | null;
export interface PythonChart {
  code: string; period: '1d' | '1w' | '1mo'; rows: CandleRow[];
  lines: { name: string; points: [string, number | null][] }[]; markers: string[];
  marker_groups?: { name: string; line_name?: string; dates: string[] }[];
}
export interface PythonAnalysis {
  kind: 'python_analysis_v1'; title: string; runtime: string; source: string; source_sha256: string; input_sha256: string;
  input_count: number; sources: { file_id: string; code: string; period: string; count: number; requested_start: string; requested_end: string; truncated: boolean; data_as_of: string }[];
  start_date: string; end_date: string; status: 'succeeded' | 'python_error' | 'runtime_error' | 'timed_out' | 'output_limit' | 'invalid_output';
  duration_ms: number; stdout: string; stderr: string; warnings: string[]; chart: PythonChart | null;
  result: Record<string, unknown>;
  price_basis?: 'stored_ohlc' | 'qfq'; adjustment_status?: 'unverified' | 'verified';
  adjustments?: { code: string; source: 'tushare.fund_adj'; anchor_date: string; anchor_factor: number }[];
}
const object = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v);
const finite = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= Number.MAX_SAFE_INTEGER;
const date = (v: unknown): v is string => typeof v === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(v) && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 10) === v;
const hash = (v: unknown) => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v);
const text = (v: unknown, max: number): v is string => typeof v === 'string' && v.length <= max;
const cell = (v: unknown): v is Cell => v === null || typeof v === 'boolean' || finite(v) || text(v, 2000);

export function pythonAnalysisResult(result: AgentToolResult): PythonAnalysis | undefined {
  const a = result.result?.data;
  if (result.tool_name !== 'execute_python' || result.status !== 'succeeded' || result.result?.untrusted_data !== true || !object(a) || a.kind !== 'python_analysis_v1' ||
    !text(a.title, 360) || !text(a.runtime, 100) || !text(a.source, 16384) || !hash(a.source_sha256) || !hash(a.input_sha256) ||
    !finite(a.input_count) || !Number.isInteger(a.input_count) || a.input_count < 1 || a.input_count > 20000 ||
    !finite(a.duration_ms) || a.duration_ms < 0 || !text(a.stdout, 65536) || !text(a.stderr, 4096) ||
    !['succeeded', 'python_error', 'runtime_error', 'timed_out', 'output_limit', 'invalid_output'].includes(String(a.status)) ||
    !object(a.result) || !Array.isArray(a.warnings) || a.warnings.length > 10 || !a.warnings.every(w => text(w, 512)) ||
    !Array.isArray(a.sources) || a.sources.length < 1 || a.sources.length > 40 || !a.sources.every(s => object(s) && hash(s.file_id) && text(s.code, 32) && text(s.period, 8) && finite(s.count) && s.count >= 0 && s.count <= 500 && typeof s.truncated === 'boolean') ||
    ![a.start_date, a.end_date].every(d => d === '' || date(d))) return;
  // Missing metadata is an older, unverified artifact. New adjusted artifacts
  // must carry a complete per-instrument anchor from the server.
  if (a.price_basis !== undefined || a.adjustment_status !== undefined || a.adjustments !== undefined) {
    if (!['stored_ohlc', 'qfq'].includes(String(a.price_basis)) || !['unverified', 'verified'].includes(String(a.adjustment_status)) ||
      !Array.isArray(a.adjustments) || a.adjustments.length > 40) return;
    if (a.price_basis === 'qfq') {
      const codes = new Set(a.sources.map(s => (s as Record<string, unknown>).code));
      if (a.adjustment_status !== 'verified' || !date(a.end_date) || a.adjustments.length !== codes.size ||
        new Set(a.adjustments.map(v => object(v) ? v.code : undefined)).size !== codes.size ||
        !a.adjustments.every(v => object(v) && codes.has(v.code) && v.source === 'tushare.fund_adj' && date(v.anchor_date) &&
          v.anchor_date <= String(a.end_date) && finite(v.anchor_factor) && v.anchor_factor > 0)) return;
    } else if (a.adjustments.length !== 0) return;
  }
  if (a.chart !== null) {
    const c = a.chart;
    if (a.status !== 'succeeded' || !object(c) || typeof c.code !== 'string' || !/^(sh|sz|bj)\.[0-9]{6}$/.test(c.code) || !['1d', '1w', '1mo'].includes(String(c.period)) ||
      !Array.isArray(c.rows) || c.rows.length < 1 || c.rows.length > 600 || !c.rows.every((r, i, rows) => Array.isArray(r) && r.length === 6 && date(r[0]) && r.slice(1).every(finite) &&
        r.slice(1, 5).every(n => n > 0) && r[5] >= 0 && r[2] >= Math.max(r[1], r[4]) && r[3] <= Math.min(r[1], r[4]) && (i === 0 || rows[i - 1][0] < r[0])) ||
      !Array.isArray(c.lines) || c.lines.length > 4 || !Array.isArray(c.markers) || c.markers.length > 600) return;
    const dates = new Set(c.rows.map(r => r[0]));
    if (new Set(c.markers).size !== c.markers.length || !c.markers.every(d => date(d) && dates.has(d)) || !c.lines.every(l => object(l) && text(l.name, 120) &&
      l.name.trim().length > 0 && Array.isArray(l.points) && l.points.length <= 600 && new Set(l.points.map(p => Array.isArray(p) ? p[0] : undefined)).size === l.points.length &&
      l.points.every(p => Array.isArray(p) && p.length === 2 && date(p[0]) && dates.has(p[0]) && (p[1] === null || finite(p[1]))) )) return;
    const names = new Set(c.lines.map(l => l.name));
    if (names.size !== c.lines.length) return;
    if (c.marker_groups !== undefined) {
      if (!Array.isArray(c.marker_groups) || c.marker_groups.length > 4 || (c.marker_groups.length > 0 && c.markers.length > 0)) return;
      for (const g of c.marker_groups) {
        if (!object(g) || !text(g.name, 120) || !g.name.trim() || names.has(g.name) ||
          (g.line_name !== undefined && (!text(g.line_name, 120) || !c.lines.some(l => l.name === g.line_name))) ||
          !Array.isArray(g.dates) || g.dates.length > 600 || new Set(g.dates).size !== g.dates.length || !g.dates.every(d => date(d) && dates.has(d))) return;
        names.add(g.name);
      }
    }
  }
  return a as unknown as PythonAnalysis;
}

export function pythonTable(a: PythonAnalysis): { columns: string[]; rows: Cell[][] } | undefined {
  if (a.status !== 'succeeded') return;
  const t = a.result.table;
  if (!object(t) || !Array.isArray(t.columns) || t.columns.length < 1 || t.columns.length > 30 || !t.columns.every(c => text(c, 120)) ||
    !Array.isArray(t.rows) || t.rows.length > 2000 || !t.rows.every(r => Array.isArray(r) && r.length === (t.columns as unknown[]).length && r.every(cell))) return;
  return t as { columns: string[]; rows: Cell[][] };
}
export function pythonTableCSV(a: PythonAnalysis): string {
  const t = pythonTable(a); if (!t) return '';
  // Quoting alone does not neutralize spreadsheet formulas in script output.
  const encode = (v: Cell) => { const s = v === null ? '' : String(v); return '"' + (typeof v === 'string' && /^[\s]*[=+\-@]|^[\t\r]/.test(s) ? "'" + s : s).replace(/"/g, '""') + '"'; };
  return [t.columns, ...t.rows].map(row => row.map(encode).join(',')).join('\r\n');
}

export function pythonPriceBasis(a: PythonAnalysis, code?: string): string {
  if (a.price_basis === 'qfq' && a.adjustment_status === 'verified' && a.adjustments?.length) {
    const anchors = a.adjustments.filter(v => !code || v.code === code);
    const dates = [...new Set(anchors.map(v => v.anchor_date))];
    return `前复权 · 基准 ${dates.join(' / ')} · Tushare fund_adj`;
  }
  return a.adjustment_status === 'verified' ? '未复权原始价格 · 因子来源已核验' : '库中价格，复权未核验';
}

export function pythonChartOption(a: PythonAnalysis, p: ChartPalette = defaultChartPalette) {
  const c = a.chart!;
  const first = c.rows[0][0], last = c.rows[c.rows.length - 1][0];
  const groups = c.marker_groups?.length ? c.marker_groups : c.markers.length ? [{ name: '脚本标记', dates: c.markers, line_name: undefined }] : [];
  const eventCount = groups.reduce((n, g) => n + g.dates.length, 0);
  const hitDays = new Set(groups.flatMap(g => g.dates)).size;
  const colors = [p.researchBlue, p.researchOrange, p.researchPurple, p.researchTeal];
  const colorFor = (g: typeof groups[number], i: number) => colors[Math.max(0, g.line_name ? c.lines.findIndex(l => l.name === g.line_name) : i)];
  const base = klineChartOption({ code: c.code, period: c.period, requested_start: a.start_date, requested_end: a.end_date,
    first_date: first, last_date: last, data_as_of: last, truncated: false, coverage_verified: false, price_basis: 'stored_ohlc', adjustment_status: 'unverified', volume_unit: 'unspecified',
    metric: 'close_return', comparison: 'gte', threshold_pct: 0, bar_count: c.rows.length, eligible_count: c.rows.length, excluded_count: 0, matched_count: eventCount, match_rate_pct: null,
    warnings: [], matches: { columns: [], rows: [] }, chart: { kind: 'candlestick_v1', columns: [], rows: c.rows } }, p);
  const byDate = new Map(c.rows.map(r => [r[0], r]));
  const lineValues = c.lines.map(l => new Map(l.points));
  const occupied = new Map<string, number>();
  const eventSeries = groups.map((group, i) => ({ name: group.name, type: 'scatter', symbol: 'triangle', symbolRotate: 180, symbolSize: [9, 10], z: 10,
    itemStyle: { color: colorFor(group, i), borderColor: p.surface, borderWidth: 1, opacity: 1 },
    data: group.dates.map(date => { const slot = occupied.get(date) ?? 0; occupied.set(date, slot + 1);
      return { value: [date, byDate.get(date)![2]], symbolOffset: [0, -10 - slot * 11] }; }),
  }));
  const description = groups.map(g => `${g.name} ${g.dates.length}次`).join('；');
  const tooltip = (params: unknown) => {
    const items = Array.isArray(params) ? params : [params];
    const point = items.find(v => object(v) && v.seriesType === 'candlestick');
    if (!object(point) || typeof point.dataIndex !== 'number') return '';
    const row = c.rows[point.dataIndex]; if (!row) return '';
    return [base.tooltip.formatter(params), ...c.lines.map((l, i) => `${l.name}：${lineValues[i].get(row[0]) ?? '—'}`),
      ...groups.filter(g => g.dates.includes(row[0])).map(g => `▼ ${g.name}`)].join('\n');
  };
  return { ...base,
    title: { ...base.title, text: `${c.code} · ${(a.title || 'Python 行情研究').slice(0, 55)}`, subtext: `${first} — ${last} · ${c.period === '1w' ? '周五聚合周线' : c.period === '1mo' ? '月末聚合月线' : '日线'} · ${c.rows.length} 根\n${eventCount} 个事件 / ${hitDays} 个日期 · ${pythonPriceBasis(a, c.code)}` },
    aria: { enabled: true, description: `${c.code}，${first}至${last}，${description || '无事件标记'}。` },
    tooltip: { ...base.tooltip, formatter: tooltip },
    grid: [{ ...base.grid[0], top: 115, height: '43%' }, base.grid[1]],
    yAxis: [{ ...base.yAxis[0], boundaryGap: [0, '22%'] }, base.yAxis[1]],
    series: [{ ...base.series[0], name: a.price_basis === 'qfq' ? 'K 线（前复权）' : base.series[0].name, markPoint: undefined }, base.series[1],
    ...c.lines.map((line, i) => ({ name: line.name, type: 'line', symbol: 'none', connectNulls: false,
      data: c.rows.map(r => lineValues[i].get(r[0]) ?? null), lineStyle: { width: 1.5, color: colors[i] }, itemStyle: { color: colors[i] } })), ...eventSeries],
    legend: { type: 'scroll', data: [...c.lines.map(l => l.name), ...groups.map(g => ({ name: g.name, icon: 'path://M0,0 L10,0 L5,8 Z' }))], left: 60, right: 18, top: 76,
      textStyle: { color: p.ink, fontSize: 11 }, pageTextStyle: { color: p.ink }, pageIconColor: p.ink,
      formatter: (name: string) => { const g = groups.find(g => g.name === name); return g ? `${name} (${g.dates.length})` : name; } },
  };
}

// Retries for the same chart range replace earlier successful charts. A later
// audit without a chart never hides the chart; every execution remains in details.
export function pythonChartResults(values: AgentToolResult[]): PythonAnalysis[] {
  const charts = new Map<string, PythonAnalysis>();
  for (const value of values) {
    const a = pythonAnalysisResult(value);
    if (a?.status === 'succeeded' && a.chart) charts.set(JSON.stringify([a.chart.code, a.chart.period, a.start_date, a.end_date, a.price_basis ?? 'stored_ohlc', a.adjustments ?? []]), a);
  }
  return [...charts.values()];
}

// A comparison table may use several instruments while the accompanying chart
// uses just one. Preserve both deliverables, even after an audit-only call.
export function pythonResultCards(values: AgentToolResult[]): PythonAnalysis[] {
  const tables = new Map<string, PythonAnalysis>();
  const analyses = values.flatMap(value => { const a = pythonAnalysisResult(value); return a ? [a] : []; });
  for (const a of analyses) {
    if (a.status !== 'succeeded' || a.chart) continue;
    const sources = [...new Set(a.sources.map(s => `${s.code}/${s.period}`))].sort();
    tables.set(JSON.stringify([a.title, sources, a.start_date, a.end_date, a.price_basis ?? 'stored_ohlc', a.adjustments ?? []]), a);
  }
  const selected = new Set([...pythonChartResults(values), ...tables.values()]);
  const cards = analyses.filter(a => selected.has(a));
  return cards.length ? cards : analyses.slice(-1);
}
