// Synthetic OHLC only, used by the isolated UI fixture and browser checks.
export function klineAnalysisFixture({ count = 60, start = '2025-01-01' } = {}) {
  const rows = [], matches = []
  const first = Date.parse(start)
  let previousClose = 100, previousDate = new Date(first - 86400000).toISOString().slice(0, 10)
  for (let i = 0; i < count; i++) {
    const date = new Date(first + i * 86400000).toISOString().slice(0, 10)
    const open = previousClose, close = Number((open * (i % 20 === 10 ? 1.06 : i % 2 ? 1.01 : .99)).toFixed(2))
    const high = Number((Math.max(open, close) * 1.01).toFixed(2)), low = Number((Math.min(open, close) * .99).toFixed(2))
    const pct = (close / previousClose - 1) * 100
    rows.push([date, open, high, low, close, 1000 + i * 37])
    if (pct >= 4) matches.push([date, previousDate, previousClose, close, pct])
    previousClose = close; previousDate = date
  }
  return { kind: 'kline_analysis_v1', file_id: 'a'.repeat(64), code: 'sh.600000', period: '1d', requested_start: rows[0][0], requested_end: rows.at(-1)[0], first_date: rows[0][0], last_date: rows.at(-1)[0], data_as_of: rows.at(-1)[0], truncated: false, coverage_verified: false, price_basis: 'stored_ohlc', adjustment_status: 'unverified', volume_unit: 'unspecified', metric: 'close_return', comparison: 'gte', threshold_pct: 4, bar_count: rows.length, eligible_count: rows.length, excluded_count: 0, matched_count: matches.length, match_rate_pct: matches.length / rows.length * 100, warnings: ['使用库中价格，未进行复权处理；adj_factor 为 1 不能证明没有除权除息。', '收益相对上一条可用 K 线收盘价；未核验交易日缺口，不能据此断言区间完整。'], matches: { columns: ['date', 'previous_date', 'previous_close', 'price', 'return_pct'], rows: matches }, chart: { kind: 'candlestick_v1', columns: ['date', 'open', 'high', 'low', 'close', 'volume'], rows } }
}

// Seven bounded artifacts exercise a long overview without expanding the
// trusted per-tool schema's 500-bar limit or reading any production data.
export function klineAnalysisWindowsFixture() {
  return Array.from({ length: 7 }, (_, i) => klineAnalysisFixture({ count: 200,
    start: new Date(Date.UTC(2020, 0, 1) + i * 200 * 86400000).toISOString().slice(0, 10) }))
}
