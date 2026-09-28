import test from 'node:test';
import assert from 'node:assert/strict';
import { pythonAnalysisResult, pythonChartOption, pythonTableCSV, pythonChartResults, pythonResultCards, pythonPriceBasis } from '../src/agent/python-analysis.ts';
import type { PythonAnalysis } from '../src/agent/python-analysis.ts';
import type { AgentToolResult } from '../src/agent/types.ts';
import { defaultChartPalette } from '../src/charts/palette.ts';

function fixture(): PythonAnalysis {
  return { kind: 'python_analysis_v1', title: '60周均线下穿', runtime: 'CPython 3.12.0 / WASI', source: "print('{}')", source_sha256: 'a'.repeat(64), input_sha256: 'b'.repeat(64),
    input_count: 5, sources: [{ file_id: 'c'.repeat(64), code: 'sh.510300', period: '1d', count: 5, requested_start: '2025-01-01', requested_end: '2025-01-10', truncated: false, data_as_of: '2025-01-10' }],
    start_date: '2025-01-01', end_date: '2025-01-10', status: 'succeeded', duration_ms: 150, stdout: '', stderr: '', warnings: ['复权未核验'],
    result: { metrics: { count: 1 }, table: { columns: ['date', 'close', 'note'], rows: [['2025-01-10', 95, '=HYPERLINK("https://example.com")']] } },
    chart: { code: 'sh.510300', period: '1w', rows: [['2025-01-03', 100, 102, 98, 101, 500], ['2025-01-10', 101, 103, 93, 95, 600]], lines: [{ name: 'MA60', points: [['2025-01-03', 100], ['2025-01-10', 99]] }], markers: ['2025-01-10'] } };
}
const result = (data: unknown): AgentToolResult => ({ run_id: 'run', tool_call_id: 'call', tool_name: 'execute_python', status: 'succeeded', result: { data, untrusted_data: true } });

test('qfq labels require real per-code anchors and keep separate ETF and raw charts', () => {
  const raw = fixture(), qfq = fixture();
  Object.assign(qfq, { price_basis: 'qfq', adjustment_status: 'verified', adjustments: [{ code: 'sh.510300', source: 'tushare.fund_adj', anchor_date: '2025-01-10', anchor_factor: 1.1 }] });
  assert.ok(pythonAnalysisResult(result(qfq)));
  assert.match(pythonPriceBasis(qfq), /前复权.*2025-01-10/);
  const option = pythonChartOption(qfq);
  assert.match(option.title.subtext, /前复权.*Tushare fund_adj/);
  assert.doesNotMatch(option.title.subtext, /未核验/);
  assert.equal(option.series[0].name, 'K 线（前复权）');
  const other = structuredClone(qfq);
  other.chart!.code = other.sources[0].code = other.adjustments![0].code = 'sh.512890';
  assert.deepEqual(pythonChartResults([raw, qfq, other].map(result)), [raw, qfq, other]);
  for (const mutate of [
    (a: PythonAnalysis) => { a.adjustment_status = 'unverified'; },
    (a: PythonAnalysis) => { a.adjustments = []; },
    (a: PythonAnalysis) => { a.adjustments![0].anchor_factor = 0; },
    (a: PythonAnalysis) => { a.adjustments![0].anchor_date = '2025-01-11'; },
    (a: PythonAnalysis) => { a.adjustments![0].code = 'sh.512890'; },
    (a: PythonAnalysis) => { a.price_basis = 'stored_ohlc'; },
  ]) { const a = structuredClone(qfq); mutate(a); assert.equal(pythonAnalysisResult(result(a)), undefined); }
  assert.match(pythonPriceBasis(raw), /复权未核验/);
});

test('Python chart uses real OHLC, one combined series and the active palette', () => {
  const a = pythonAnalysisResult(result(fixture()))!; assert.ok(a);
  for (const palette of [defaultChartPalette, { ...defaultChartPalette, surface: '#141414', ink: '#ffffff', accent: '#8bbfa2' }]) {
    const option = pythonChartOption(a, palette);
    assert.equal(option.backgroundColor, palette.surface);
    assert.deepEqual(option.series[0].data[1], [101, 95, 93, 103]);
    assert.deepEqual(option.series[3].data[0].value, ['2025-01-10', 103]);
    assert.deepEqual(option.series[2].data, [100, 99]);
    assert.ok(option.dataZoom.every(d => d.start === 0 && d.end === 100));
    assert.equal(option.tooltip.renderMode, 'richText');
    assert.doesNotMatch(option.title.subtext, /涨跌幅/);
  }
});

test('four event groups share line colors, stay distinct on one candle, and retain zero-hit legends', () => {
  const a = fixture(), c = a.chart!;
  c.lines = [5, 10, 20, 60].map(n => ({ name: `MA${n}`, points: [['2025-01-03', 100], ['2025-01-10', 99]] }));
  c.markers = [];
  c.marker_groups = c.lines.map(l => ({ name: `下穿 ${l.name}`, line_name: l.name, dates: ['2025-01-10'] }));
  assert.ok(pythonAnalysisResult(result(a)));
  const option = pythonChartOption(a);
  assert.equal(option.series.filter(s => s.type === 'candlestick').length, 1);
  const lines = option.series.slice(2, 6), events = option.series.slice(6);
  assert.equal(new Set(events.map(s => s.itemStyle.color)).size, 4);
  assert.deepEqual(events.map(s => s.itemStyle.color), lines.map(s => s.itemStyle.color));
  assert.equal(new Set(events.map(s => s.data[0].symbolOffset[1])).size, 4);
  assert.deepEqual(events.map(s => s.data[0].value), Array(4).fill(['2025-01-10', 103]));
  assert.equal(option.legend.data.length, 8);
  const tooltip = option.tooltip.formatter([{ seriesType: 'candlestick', dataIndex: 1 }]);
  for (const n of [5, 10, 20, 60]) assert.ok(tooltip.includes(`下穿 MA${n}`));
  c.marker_groups[0].dates = [];
  assert.equal(pythonChartOption(a).legend.formatter('下穿 MA5'), '下穿 MA5 (0)');
  for (const mutate of [
    (v: PythonAnalysis) => { v.chart!.marker_groups![1].name = v.chart!.marker_groups![0].name; },
    (v: PythonAnalysis) => { v.chart!.marker_groups![0].dates = ['2025-01-11']; },
    (v: PythonAnalysis) => { v.chart!.marker_groups![0].dates = ['2025-01-10', '2025-01-10']; },
    (v: PythonAnalysis) => { v.chart!.marker_groups![0].line_name = 'not a line'; },
    (v: PythonAnalysis) => { v.chart!.markers = ['2025-01-10']; },
  ]) { const v = structuredClone(a); mutate(v); assert.equal(pythonAnalysisResult(result(v)), undefined); }
});

test('retry charts replace the same range and audit-only calls cannot hide or duplicate them', () => {
  const first = fixture(), retry = fixture(), audit = fixture(), failed = fixture();
  retry.source_sha256 = 'd'.repeat(64);
  audit.chart = null;
  failed.chart = null; failed.status = 'output_limit';
  assert.deepEqual(pythonChartResults([first, retry, audit, failed].map(result)), [retry]);
});

test('comparison tables remain visible alongside charts, while corrected copies replace earlier results', () => {
  const chart = fixture(), comparison = fixture(), corrected = fixture(), failed = fixture();
  comparison.chart = null; comparison.title = '两只 ETF 比较'; comparison.source_sha256 = 'd'.repeat(64);
  comparison.sources.push({ ...comparison.sources[0], code: 'sh.512890', file_id: 'e'.repeat(64) });
  Object.assign(corrected, structuredClone(comparison));
  corrected.source_sha256 = 'f'.repeat(64); corrected.result.metrics = { count: 2 };
  failed.chart = null; failed.status = 'python_error';
  const cards = pythonResultCards([comparison, chart, corrected, failed].map(result));
  assert.equal(cards.length, 2);
  assert.ok(cards.includes(chart)); assert.ok(cards.includes(corrected));
  assert.ok(!cards.includes(comparison)); assert.ok(!cards.includes(failed));
});

test('Python CSV preserves numeric negatives and neutralizes script-provided formulas', () => {
  const a = fixture();
  a.result.table = { columns: ['text', 'number'], rows: [['=1+2', -3], ['  @SUM(A1)', 4], ['hello,"world"', 5], ['\t=1', 6]] };
  const csv = pythonTableCSV(a);
  assert.ok(csv.includes('"\'=1+2","-3"'));
  assert.ok(csv.includes('"\'  @SUM(A1)"'));
  assert.ok(csv.includes('"hello,""world"""'));
});

test('invalid or fabricated chart markers cannot reach the renderer', () => {
  for (const mutate of [
    (a: PythonAnalysis) => { a.chart!.markers = ['2027-01-01']; },
    (a: PythonAnalysis) => { a.chart!.rows[0][2] = Infinity; },
    (a: PythonAnalysis) => { a.chart!.rows[0][0] = '<script>'; },
    (a: PythonAnalysis) => { a.chart!.lines[0].points[0][1] = NaN; },
    (a: PythonAnalysis) => { a.status = 'python_error'; },
    (a: PythonAnalysis) => { a.input_sha256 = 'forged'; },
  ]) { const a = fixture(); mutate(a); assert.equal(pythonAnalysisResult(result(a)), undefined); }
  assert.equal(pythonAnalysisResult({ ...result(fixture()), tool_name: 'other' }), undefined);
  const failed = fixture(); failed.status = 'python_error'; failed.chart = null; failed.stderr = 'NameError';
  assert.ok(pythonAnalysisResult(result(failed)));
  assert.equal(pythonTableCSV(failed), '');
});
