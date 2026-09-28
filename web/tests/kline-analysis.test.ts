import test from 'node:test';
import assert from 'node:assert/strict';
import { klineAnalysisResult, klineChartOption, klineMatchesCSV } from '../src/agent/kline-analysis.ts';
import type { KlineAnalysis } from '../src/agent/kline-analysis.ts';
import type { AgentToolResult } from '../src/agent/types.ts';

function fixture(): KlineAnalysis {
  return { kind: 'kline_analysis_v1', file_id: 'a'.repeat(64), code: 'sh.600000', period: '1d', requested_start: '2025-01-02', requested_end: '2025-01-03', first_date: '2025-01-02', last_date: '2025-01-03', data_as_of: '2025-01-03', truncated: false, coverage_verified: false, price_basis: 'stored_ohlc', adjustment_status: 'unverified', volume_unit: 'unspecified', metric: 'close_return', comparison: 'gte', threshold_pct: 4, bar_count: 2, eligible_count: 2, excluded_count: 0, matched_count: 1, match_rate_pct: 50, warnings: ['复权未经核验'], matches: { columns: ['date', 'previous_date', 'previous_close', 'price', 'return_pct'], rows: [['2025-01-02', '2024-12-31', 100, 104, 4]] }, chart: { kind: 'candlestick_v1', columns: ['date', 'open', 'high', 'low', 'close', 'volume'], rows: [['2025-01-02', 101, 105, 100, 104, 200], ['2025-01-03', 104, 106, 102, 103, 300]] } };
}
function result(data: unknown = fixture()): AgentToolResult {
  return { run_id: 'run', tool_call_id: 'call', tool_name: 'analyze_kline', status: 'succeeded', result: { untrusted_data: true, data } };
}

test('only owned successful analysis result renders bounded true OHLC with matching event markers', () => {
  const a = klineAnalysisResult(result())!;
  assert.ok(a);
  const option = klineChartOption(a);
  assert.deepEqual(option.series[0].data[0], [101, 104, 100, 105]); // ECharts order: open, close, low, high.
  assert.deepEqual(option.series[0].markPoint?.data[0].coord, ['2025-01-02', 104]);
  assert.equal(option.tooltip.renderMode, 'richText');
  assert.match(option.tooltip.formatter([{ seriesType: 'candlestick', dataIndex: 0 }]), /开盘：101\n最高：105\n最低：100\n收盘：104/);
  assert.equal(option.tooltip.formatter([{ dataIndex: '<script>' }]), '');
  assert.match(option.title.text, /sh\.600000/);
  assert.match(option.title.subtext, /2025-01-02 — 2025-01-03/);
  assert.match(option.title.subtext, /复权未核验/);
  assert.deepEqual(option.dataZoom[0].xAxisIndex, [0, 1]);
  assert.equal(option.dataZoom[0].zoomOnMouseWheel, false);
  assert.equal(option.dataZoom[0].moveOnMouseWheel, false);
  assert.equal(option.dataZoom[0].preventDefaultMouseMove, false);
  assert.ok(option.dataZoom.every(zoom => zoom.start === 0 && zoom.end === 100));
  assert.match(klineMatchesCSV(a), /2025-01-02,2024-12-31,100,104,4/);
  assert.equal(klineAnalysisResult({ ...result(), status: 'failed' }), undefined);
  assert.equal(klineAnalysisResult({ ...result(), tool_name: 'get_news' }), undefined);
});

test('chart rejects malformed, oversized, inconsistent or executable data instead of passing options through', () => {
  for (const edit of [
    (a: KlineAnalysis) => { a.chart.rows[0][2] = Infinity; },
    (a: KlineAnalysis) => { a.chart.rows[0][2] = 1; },
    (a: KlineAnalysis) => { a.chart.rows[0][0] = '2025-13-40'; },
    (a: KlineAnalysis) => { a.chart.rows[0][0] = '<script>alert(1)</script>'; },
    (a: KlineAnalysis) => { a.chart.rows.push(...Array(501).fill(a.chart.rows[0])); },
    (a: KlineAnalysis) => { a.chart.rows.reverse(); },
    (a: KlineAnalysis) => { a.matches.rows[0][3] = 999; },
    (a: KlineAnalysis) => { a.matches.rows[0][0] = '2025-02-01'; },
    (a: KlineAnalysis) => { a.matched_count = 0; },
    (a: KlineAnalysis) => { a.match_rate_pct = 40; },
    (a: KlineAnalysis) => { a.eligible_count = 3; },
    (a: KlineAnalysis) => { a.first_date = ''; },
  ]) { const a = fixture(); edit(a); assert.equal(klineAnalysisResult(result(a)), undefined); }
  const a = fixture();
  const projected = klineChartOption(klineAnalysisResult(result({ ...a, option: { tooltip: { formatter: '<img src=x onerror=alert(1)>' } } }))!);
  assert.equal(typeof projected.tooltip.formatter, 'function');
  assert.doesNotMatch(projected.tooltip.formatter([{ seriesType: 'candlestick', dataIndex: 0 }]), /<img|onerror/);
});

test('empty analysis has no fabricated rate or price chart', () => {
  const a = fixture(); a.bar_count = a.eligible_count = a.matched_count = 0; a.first_date = a.last_date = ''; a.match_rate_pct = null; a.chart.rows = []; a.matches.rows = [];
  assert.ok(klineAnalysisResult(result(a)));
  a.match_rate_pct = 0; assert.equal(klineAnalysisResult(result(a)), undefined);
});

test('ETF decline results keep signed thresholds, comparison symbols and matching chart/CSV rows', () => {
  for (const comparison of ['lte', 'lt'] as const) {
    const a = fixture();
    a.code = 'sh.510300'; a.comparison = comparison; a.threshold_pct = -4;
    a.chart.rows[0] = ['2025-01-02', 100, 100, 95, 95, 200];
    a.matches.rows[0] = ['2025-01-02', '2024-12-31', 100, 95, -5];
    const validated = klineAnalysisResult(result(a));
    assert.ok(validated);
    const chart = klineChartOption(validated);
    assert.ok(chart.title.subtext.includes(comparison === 'lte' ? '≤ -4%' : '< -4%'));
    assert.deepEqual(chart.series[0].markPoint?.data[0].coord, ['2025-01-02', 95]);
    assert.ok(klineMatchesCSV(validated).includes(`-5,close_return,${comparison},-4,sh.510300`));
  }
});
