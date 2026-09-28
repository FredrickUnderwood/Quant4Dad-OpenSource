import type { AgentToolResult } from './types.ts';
import { defaultChartPalette } from '../charts/palette.ts';
import type { ChartPalette } from '../charts/palette.ts';
import type { KlineDisplay } from './kline-overview.ts';

export type CandleRow = [string, number, number, number, number, number];
export type MatchRow = [string, string, number, number, number];
export interface KlineAnalysis {
  kind: 'kline_analysis_v1'; file_id: string; code: string; period: '1d' | '1w' | '1mo';
  requested_start: string; requested_end: string; first_date: string; last_date: string; data_as_of: string;
  truncated: boolean; coverage_verified: false; price_basis: 'stored_ohlc'; adjustment_status: 'unverified'; volume_unit: 'unspecified';
  metric: 'close_return' | 'high_return'; comparison: 'gte' | 'gt' | 'lte' | 'lt'; threshold_pct: number;
  bar_count: number; eligible_count: number; excluded_count: number; matched_count: number; match_rate_pct: number | null;
  warnings: string[]; matches: { columns: string[]; rows: MatchRow[] };
  chart: { kind: 'candlestick_v1'; columns: string[]; rows: CandleRow[] };
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);
const finite = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v) && Math.abs(v) <= Number.MAX_SAFE_INTEGER;
const date = (v: unknown): v is string => typeof v === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(v) && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 10) === v;
const count = (v: unknown): v is number => finite(v) && Number.isInteger(v) && v >= 0 && v <= 500;
export const comparisonSymbol = { gte: '≥', gt: '>', lte: '≤', lt: '<' } as const;

// Only this versioned, bounded schema reaches our fixed renderer. Returned
// options, HTML, URLs and scripts are never interpreted as chart configuration.
export function klineAnalysisResult(result: AgentToolResult): KlineAnalysis | undefined {
  if (result.tool_name !== 'analyze_kline' || result.status !== 'succeeded' || result.result?.untrusted_data !== true) return;
  const v = result.result.data;
  if (!isObject(v) || v.kind !== 'kline_analysis_v1' || typeof v.file_id !== 'string' || !/^[a-f0-9]{64}$/.test(v.file_id) ||
    typeof v.code !== 'string' || !/^(sh|sz|bj)\.[0-9]{6}$/.test(v.code) || !['1d', '1w', '1mo'].includes(String(v.period)) ||
    v.price_basis !== 'stored_ohlc' || v.adjustment_status !== 'unverified' || v.volume_unit !== 'unspecified' || v.coverage_verified !== false || typeof v.truncated !== 'boolean' ||
    !['close_return', 'high_return'].includes(String(v.metric)) || !['gte', 'gt', 'lte', 'lt'].includes(String(v.comparison)) || !finite(v.threshold_pct) || v.threshold_pct < -100 || v.threshold_pct > 1000) return;
  if (![v.requested_start, v.requested_end, v.first_date, v.last_date, v.data_as_of].every(d => d === '' || date(d)) ||
    ![v.bar_count, v.eligible_count, v.excluded_count, v.matched_count].every(count) ||
    !Array.isArray(v.warnings) || v.warnings.length > 10 || !v.warnings.every(w => typeof w === 'string' && w.length <= 512) ||
    !isObject(v.chart) || v.chart.kind !== 'candlestick_v1' || JSON.stringify(v.chart.columns) !== '["date","open","high","low","close","volume"]' ||
    !isObject(v.matches) || JSON.stringify(v.matches.columns) !== '["date","previous_date","previous_close","price","return_pct"]' ||
    !Array.isArray(v.chart.rows) || v.chart.rows.length > 500 || !Array.isArray(v.matches.rows) || v.matches.rows.length > 500) return;
  if (!v.chart.rows.every((r, i, rows) => Array.isArray(r) && r.length === 6 && date(r[0]) && r.slice(1).every(finite) &&
    r.slice(1, 5).every(n => n > 0) && r[5] >= 0 && r[3] <= Math.min(r[1], r[4]) && r[2] >= Math.max(r[1], r[4]) && (i === 0 || rows[i - 1][0] < r[0]))) return;
  const candleByDate = new Map((v.chart.rows as CandleRow[]).map(row => [row[0], row]));
  if (!v.matches.rows.every((r, i, rows) => Array.isArray(r) && r.length === 5 && date(r[0]) && date(r[1]) && r[1] < r[0] &&
    r.slice(2).every(finite) && r[2] > 0 && r[3] > 0 && candleByDate.get(r[0])?.[v.metric === 'high_return' ? 2 : 4] === r[3] && (i === 0 || rows[i - 1][0] < r[0]))) return;
  const a = v as unknown as KlineAnalysis;
  if (a.bar_count !== a.chart.rows.length || a.eligible_count + a.excluded_count !== a.bar_count || a.matched_count !== a.matches.rows.length || a.matched_count > a.eligible_count ||
    (a.eligible_count === 0 ? a.match_rate_pct !== null : !finite(a.match_rate_pct) || Math.abs(a.match_rate_pct - a.matched_count / a.eligible_count * 100) > 1e-9) ||
    (a.bar_count > 0 ? a.first_date !== a.chart.rows[0][0] || a.last_date !== a.chart.rows[a.chart.rows.length - 1]?.[0] : a.first_date !== '' || a.last_date !== '')) return;
  return a;
}

export function klineChartOption(a: KlineDisplay, p: ChartPalette = defaultChartPalette) {
  const up = p.up, down = p.down, marker = p.accent;
  const dates = a.chart.rows.map(r => r[0]);
  const matches = new Map(a.matches.rows.map(r => [r[0], r[4]]));
  const tooltip = (params: unknown): string => {
    const items = Array.isArray(params) ? params : [params];
    const point = items.find(p => isObject(p) && p.seriesType === 'candlestick') ?? items[0];
    if (!isObject(point) || typeof point.dataIndex !== 'number' || !Number.isInteger(point.dataIndex)) return '';
    const row = a.chart.rows[point.dataIndex];
    if (!row) return '';
    const matched = matches.get(row[0]);
    return [`${a.code} · ${row[0]}`, `开盘：${row[1]}`, `最高：${row[2]}`, `最低：${row[3]}`, `收盘：${row[4]}`, `成交量（原始单位）：${row[5]}`, ...(matched === undefined ? [] : [`命中涨跌幅：${matched.toFixed(2)}%`])].join('\n');
  };
  return {
    animation: false,
    backgroundColor: p.surface,
    textStyle: { color: p.ink, fontFamily: 'Manrope, sans-serif' },
    title: { text: `${a.code} · ${a.period === '1d' ? '日 K' : a.period === '1w' ? '周 K' : '月 K'} · ${a.matched_count} 次命中`, left: 12, top: 8, textStyle: { fontSize: 13, color: p.ink },
      subtext: `${a.first_date} — ${a.last_date}\n${a.metric === 'high_return' ? '最高价' : '收盘价'}涨跌幅 ${comparisonSymbol[a.comparison]} ${a.threshold_pct}%（相邻可用收盘价）\n库中价格，复权未核验`, subtextStyle: { fontSize: 10, color: p.muted, lineHeight: 15 } },
    aria: { enabled: true, description: `${a.code} K 线图，${a.first_date} 至 ${a.last_date}，${a.matched_count} 次阈值命中。` },
    tooltip: { trigger: 'axis', renderMode: 'richText', hideDelay: 0, transitionDuration: 0, axisPointer: { type: 'shadow' }, backgroundColor: p.surface, borderColor: p.hair, textStyle: { color: p.ink, fontFamily: 'JetBrains Mono, monospace', fontSize: 11 }, formatter: tooltip },
    axisPointer: { link: [{ xAxisIndex: 'all' }] },
    grid: [{ left: 60, right: 18, top: 95, height: '43%' }, { left: 60, right: 18, top: '73%', height: '12%' }],
    xAxis: [0, 1].map(gridIndex => ({ type: 'category', data: dates, gridIndex, axisLine: { lineStyle: { color: p.rule } }, axisTick: { lineStyle: { color: p.rule } }, axisLabel: { show: gridIndex === 1, color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: 10 } })),
    yAxis: [0, 1].map(gridIndex => ({ scale: true, splitNumber: gridIndex ? 2 : 4, gridIndex, axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: p.muted, fontFamily: 'JetBrains Mono, monospace', fontSize: gridIndex ? 9 : 10 }, splitLine: { show: gridIndex === 0, lineStyle: { color: p.hair, type: 'dashed' } } })),
    dataZoom: [{ type: 'inside', xAxisIndex: [0, 1], start: 0, end: 100, zoomOnMouseWheel: false, moveOnMouseWheel: false, preventDefaultMouseMove: false }, { type: 'slider', xAxisIndex: [0, 1], start: 0, end: 100, bottom: 0, height: 20, borderColor: p.hair, textStyle: { color: p.muted }, fillerColor: 'rgba(128,128,128,.12)', dataBackground: { lineStyle: { color: p.muted }, areaStyle: { color: p.hair } }, selectedDataBackground: { lineStyle: { color: p.accent }, areaStyle: { color: p.hair } }, handleStyle: { color: p.surface, borderColor: p.rule } }],
    series: [{ name: 'K 线（库中价格）', type: 'candlestick', data: a.chart.rows.map(r => [r[1], r[4], r[3], r[2]]),
      itemStyle: { color: up, color0: down, borderColor: up, borderColor0: down },
      markPoint: { symbol: 'triangle', symbolSize: [10, 12], symbolRotate: 180, symbolOffset: [0, -12], label: { show: false }, itemStyle: { color: marker, borderColor: p.surface, borderWidth: 1 }, data: a.matches.rows.map(r => ({ name: `${r[0]} · ${r[4].toFixed(2)}%`, coord: [r[0], r[3]], value: Number(r[4].toFixed(2)) })) } },
    { name: '成交量（原始单位）', type: 'bar', barWidth: '60%', xAxisIndex: 1, yAxisIndex: 1, data: a.chart.rows.map(r => ({ value: r[5], itemStyle: { color: r[4] >= r[1] ? up : down, opacity: .55 } })) }],
  };
}

export function klineMatchesCSV(a: KlineDisplay): string {
  // Every cell is a validated date, finite number or fixed enum, never free text.
  return ['date,previous_date,previous_close,price,return_pct,metric,comparison,threshold_pct,code,period,price_basis,adjustment_status',
    ...a.matches.rows.map(r => [...r, a.metric, a.comparison, a.threshold_pct, a.code, a.period, a.price_basis, a.adjustment_status].join(','))].join('\r\n');
}
