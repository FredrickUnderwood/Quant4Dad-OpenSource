import type { KlineAnalysis } from './kline-analysis.ts';

export type KlineDisplay = Omit<KlineAnalysis, 'kind' | 'file_id'>;
export interface KlineOverview { analysis: KlineDisplay; segments: KlineAnalysis[] }
const maxOverviewBars = 20_000;
const conditionKey = (a: KlineDisplay) => JSON.stringify([a.code, a.period, a.metric, a.comparison, a.threshold_pct, a.price_basis, a.adjustment_status, a.volume_unit]);
const start = (a: KlineDisplay) => a.requested_start || a.first_date;
const end = (a: KlineDisplay) => a.requested_end || a.last_date;

/** Combine validated tool artifacts, never model prose. Only adjacent request
 * windows with disjoint bars can be added: overlapping or inconsistent runs
 * must not silently double-count samples or overwrite OHLC. */
export function klineOverviews(analyses: KlineAnalysis[]): KlineOverview[] {
  const groups: KlineOverview[] = [];
  const seen = new Set<string>();
  const sorted = [...analyses].sort((a, b) => start(a).localeCompare(start(b)));
  for (const a of sorted) {
    const { kind: _kind, file_id: _file, ...display } = a;
    const identity = JSON.stringify(display);
    if (seen.has(identity)) continue;
    seen.add(identity);
    const group = groups.find(g => {
      const prior = g.analysis;
      return conditionKey(prior) === conditionKey(a) && !prior.truncated && !a.truncated &&
        prior.bar_count > 0 && a.bar_count > 0 && prior.last_date < a.first_date &&
        prior.bar_count + a.bar_count <= maxOverviewBars &&
        Date.parse(start(a)) <= Date.parse(end(prior)) + 86_400_000;
    });
    if (!group) { groups.push({ analysis: display, segments: [a] }); continue; }
    const prior = group.analysis;
    const matched = prior.matched_count + a.matched_count, eligible = prior.eligible_count + a.eligible_count;
    group.segments.push(a);
    group.analysis = {
      ...prior, requested_end: end(a) > end(prior) ? end(a) : end(prior), last_date: a.last_date,
      data_as_of: a.data_as_of > prior.data_as_of ? a.data_as_of : prior.data_as_of,
      bar_count: prior.bar_count + a.bar_count, eligible_count: eligible,
      excluded_count: prior.excluded_count + a.excluded_count, matched_count: matched,
      match_rate_pct: eligible ? matched / eligible * 100 : null,
      warnings: [...new Set([...prior.warnings, ...a.warnings])],
      chart: { ...prior.chart, rows: [...prior.chart.rows, ...a.chart.rows] },
      matches: { ...prior.matches, rows: [...prior.matches.rows, ...a.matches.rows] },
    };
  }
  return groups;
}
