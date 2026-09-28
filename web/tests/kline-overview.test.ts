import assert from 'node:assert/strict';
import test from 'node:test';
import { klineOverviews } from '../src/agent/kline-overview.ts';
import { klineAnalysisResult, klineChartOption, klineMatchesCSV } from '../src/agent/kline-analysis.ts';
import type { KlineAnalysis } from '../src/agent/kline-analysis.ts';
import { defaultChartPalette } from '../src/charts/palette.ts';
import { klineAnalysisFixture, klineAnalysisWindowsFixture } from './kline-analysis-fixture.mjs';

test('seven validated windows become one complete chart and one CSV, even when results arrive out of order', () => {
  const source = klineAnalysisWindowsFixture() as KlineAnalysis[];
  source.forEach(data => assert.ok(klineAnalysisResult({ run_id: 'run', tool_call_id: 'call', tool_name: 'analyze_kline', status: 'succeeded', result: { untrusted_data: true, data } })));
  const [overview] = klineOverviews([...source].reverse());
  assert.equal(klineOverviews(source).length, 1);
  assert.equal(overview.segments.length, 7);
  assert.equal(overview.analysis.bar_count, 1400);
  assert.equal(overview.analysis.eligible_count, 1400);
  assert.equal(overview.analysis.matched_count, 70);
  assert.equal(overview.analysis.match_rate_pct, 5);
  assert.deepEqual(overview.analysis.chart.rows, source.flatMap(a => a.chart.rows));
  assert.equal(klineChartOption(overview.analysis).series[0].data.length, 1400);
  assert.equal(klineChartOption(overview.analysis).series[0].markPoint?.data.length, 70);
  assert.equal(klineMatchesCSV(overview.analysis).split('\r\n').length, 71);
  assert.equal(klineOverviews([...source, structuredClone(source[0])]).length, 1);
  assert.equal(klineOverviews([...source, structuredClone(source[0])])[0].analysis.bar_count, 1400);
});

test('overview adds valid denominators instead of averaging rates and retains exclusions and provenance', () => {
  const first = klineAnalysisFixture({ count: 20 }) as KlineAnalysis;
  first.eligible_count = 19; first.excluded_count = 1; first.match_rate_pct = 100 / 19;
  first.warnings = ['缺少前收', '复权未核验'];
  const second = klineAnalysisFixture({ count: 60, start: '2025-01-21' }) as KlineAnalysis;
  const { analysis, segments } = klineOverviews([first, second])[0];
  assert.equal(analysis.matched_count, 4);
  assert.equal(analysis.eligible_count, 79);
  assert.equal(analysis.excluded_count, 1);
  assert.equal(analysis.match_rate_pct, 4 / 79 * 100);
  assert.ok(analysis.warnings.includes('缺少前收'));
  assert.equal(analysis.coverage_verified, false);
  assert.equal(analysis.adjustment_status, 'unverified');
  assert.deepEqual(segments, [first, second]);
});

test('different symbols, metrics, thresholds, truncated, overlapping and disjoint request ranges are not merged', () => {
  const [first, second] = klineAnalysisWindowsFixture() as KlineAnalysis[];
  for (const change of [
    (a: KlineAnalysis) => { a.code = 'sz.000001'; },
    (a: KlineAnalysis) => { a.metric = 'high_return'; },
    (a: KlineAnalysis) => { a.threshold_pct = 5; },
    (a: KlineAnalysis) => { a.comparison = 'gt'; },
    (a: KlineAnalysis) => { a.truncated = true; },
    (a: KlineAnalysis) => { a.first_date = first.last_date; },
  ]) { const a = structuredClone(second); change(a); assert.equal(klineOverviews([first, a]).length, 2); }
  const far = klineAnalysisFixture({ count: 20, start: '2026-01-01' }) as KlineAnalysis;
  assert.equal(klineOverviews([first, far]).length, 2);
  const overlap = structuredClone(first); overlap.matched_count = 0; overlap.matches.rows = []; overlap.match_rate_pct = 0;
  assert.equal(klineOverviews([first, overlap]).length, 2);
});

test('the full-range chart uses the active backtest palette for candles, axes, text, tooltip and export background', () => {
  const dark = { ...defaultChartPalette, surface: '#141c19', ink: '#e9eadf', muted: '#9a9f94', hair: '#353e36', rule: '#465047', up: '#78b48f', down: '#e89182', accent: '#bca86d' };
  const a = klineAnalysisFixture() as KlineAnalysis;
  for (const palette of [defaultChartPalette, dark]) {
    const option = klineChartOption(a, palette);
    assert.equal(option.backgroundColor, palette.surface);
    assert.equal(option.title.textStyle.color, palette.ink);
    assert.equal(option.tooltip.backgroundColor, palette.surface);
    assert.equal(option.series[0].itemStyle?.color, palette.up);
    assert.equal(option.series[0].itemStyle?.color0, palette.down);
    assert.equal(option.yAxis[0].splitLine.lineStyle.color, palette.hair);
    assert.ok(option.dataZoom.every(zoom => zoom.start === 0 && zoom.end === 100));
  }
});
