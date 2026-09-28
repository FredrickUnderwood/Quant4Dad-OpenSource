const count = value => Number.isSafeInteger(value) && value >= 0 && value <= 500
const finite = value => typeof value === 'number' && Number.isFinite(value)
const date = value => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) &&
  Number.isFinite(Date.parse(value + 'T00:00:00Z')) && new Date(value + 'T00:00:00Z').toISOString().slice(0, 10) === value
const optionalDate = value => value === '' || date(value)
const matchColumns = ['date', 'previous_date', 'previous_close', 'price', 'return_pct']
const chartColumns = ['date', 'open', 'high', 'low', 'close', 'volume']
const same = (left, right) => JSON.stringify(left) === JSON.stringify(right)
const comparisons = { gte: (a, b) => a >= b, gt: (a, b) => a > b, lte: (a, b) => a <= b, lt: (a, b) => a < b }

export const researchTools = new Set(['analyze_kline', 'execute_python'])

// The Gateway result and Go result card retain every chart point. Only the
// model-facing content is compacted, after validation against the full schema.
// The model needs statistics and matches, not a second copy of all OHLC rows.
export function modelToolResult(name, value) {
  if (name === 'execute_python' && value?.untrusted_data === true && value.data?.kind === 'python_analysis_v1') {
    const { source, chart, ...data } = value.data
    const { chart: overlays, ...result } = data.result ?? {}
    return { ...value, data: { ...data, result, source_retained_in_result_card: typeof source === 'string',
      chart: chart && { code: chart.code, period: chart.period, point_count: chart.rows?.length, rows_omitted: true,
        line_names: chart.lines?.map(line => line.name), markers: chart.markers, marker_groups: chart.marker_groups } } }
  }
  if (name !== 'analyze_kline' || value?.untrusted_data !== true ||
      value.data?.kind !== 'kline_analysis_v1' || !Array.isArray(value.data.chart?.rows)) return value
  const { rows, ...chart } = value.data.chart
  return { ...value, data: { ...value.data, chart: { ...chart, point_count: rows.length, rows_omitted: true } } }
}

// Promote only a bounded, internally consistent statistical summary. Free-form
// warnings, titles, code and raw rows remain untrusted tool data. Neither prior
// assistant claims nor compaction summaries can establish these facts.
export function researchFact(call, event, data, failed) {
  const fact = { tool: call.name, call_seq: call.seq, source_seq: event.seq,
    outcome: failed ? 'tool_failed' : 'tool_succeeded' }
  if (failed) return fact
  if (call.name === 'execute_python') {
    const valid = data?.kind === 'python_analysis_v1' && /^[a-f0-9]{64}$/.test(data.source_sha256 ?? '') &&
      /^[a-f0-9]{64}$/.test(data.input_sha256 ?? '') && Number.isSafeInteger(data.input_count) && data.input_count > 0 && data.input_count <= 20000 &&
      ['succeeded', 'python_error', 'runtime_error', 'timed_out', 'output_limit', 'invalid_output'].includes(data.status)
    if (!valid) return { ...fact, execution_valid: false, formula_verified: false }
    const points = data.chart?.rows?.length ?? (data.chart?.rows_omitted === true ? data.chart.point_count : undefined)
    const codes = new Set(Array.isArray(data.sources) ? data.sources.map(s => s.code) : [])
    const adjusted = data.status === 'succeeded' && data.price_basis === 'qfq' && data.adjustment_status === 'verified' &&
      date(data.end_date) && codes.size > 0 && codes.size <= 40 && Array.isArray(data.adjustments) &&
      data.adjustments.length === codes.size && new Set(data.adjustments.map(a => a?.code)).size === codes.size &&
      data.adjustments.every(a => a && /^(sh|sz)\.[0-9]{6}$/.test(a.code) && codes.has(a.code) &&
        a.source === 'tushare.fund_adj' && date(a.anchor_date) && a.anchor_date <= data.end_date && finite(a.anchor_factor) && a.anchor_factor > 0)
    return { ...fact, execution_valid: true, status: data.status, source_sha256: data.source_sha256, input_sha256: data.input_sha256,
      input_count: data.input_count, formula_verified: false, coverage_verified: false, adjustment_status: adjusted ? 'verified' : 'unverified',
      ...(adjusted ? { price_basis: 'qfq', adjustments: data.adjustments.map(a => ({ code: a.code, source: a.source, anchor_date: a.anchor_date, anchor_factor: a.anchor_factor })) } : {}),
      chart_generated: data.status === 'succeeded' && Number.isSafeInteger(points) && points > 0 && points <= 600 }
  }
  fact.summary_valid = false
  if (data?.kind !== 'kline_analysis_v1' ||
      !['1d', '1w', '1mo'].includes(data.period) ||
      !['close_return', 'high_return'].includes(data.metric) || !['gte', 'gt', 'lte', 'lt'].includes(data.comparison) ||
      !finite(data.threshold_pct) || data.threshold_pct < -100 || data.threshold_pct > 1000 ||
      !['bar_count', 'eligible_count', 'excluded_count', 'matched_count'].every(key => count(data[key])) ||
      data.eligible_count + data.excluded_count !== data.bar_count || data.matched_count > data.eligible_count ||
      (data.eligible_count === 0 ? data.match_rate_pct !== null : !finite(data.match_rate_pct) ||
        Math.abs(data.match_rate_pct - data.matched_count / data.eligible_count * 100) > 1e-8) ||
      !same(data.matches?.columns, matchColumns) || !Array.isArray(data.matches?.rows) || data.matches.rows.length !== data.matched_count ||
      !['requested_start', 'requested_end', 'first_date', 'last_date', 'data_as_of'].every(key => optionalDate(data[key])) ||
      data.requested_start && data.requested_end && data.requested_start > data.requested_end ||
      data.bar_count > 0 && (!date(data.first_date) || !date(data.last_date) || data.first_date > data.last_date) ||
      data.bar_count > 0 && (data.requested_start && data.first_date < data.requested_start || data.requested_end && data.last_date > data.requested_end) ||
      data.bar_count === 0 && (data.first_date !== '' || data.last_date !== '') ||
      typeof data.truncated !== 'boolean' || data.coverage_verified !== false ||
      data.price_basis !== 'stored_ohlc' || data.adjustment_status !== 'unverified') return fact
  let previous
  for (const row of data.matches.rows) {
    if (!Array.isArray(row) || row.length !== 5 || !date(row[0]) || !date(row[1]) || row[1] >= row[0] ||
        previous && row[0] <= previous || row[0] < data.first_date || row[0] > data.last_date ||
        !row.slice(2).every(finite) || row[2] <= 0 || row[3] <= 0 ||
        !comparisons[data.comparison](row[4], data.threshold_pct)) return fact
    previous = row[0]
  }
  const chart = data.chart
  const chartCount = Array.isArray(chart?.rows) ? chart.rows.length : chart?.rows_omitted === true ? chart.point_count : undefined
  return { ...fact, summary_valid: true,
    ...(typeof data.code === 'string' && /^[A-Za-z0-9_.-]{1,32}$/.test(data.code) ? { code: data.code } : {}),
    ...(typeof data.file_id === 'string' && /^[0-9a-f]{64}$/.test(data.file_id) ? { file_id: data.file_id } : {}),
    period: data.period, metric: data.metric, comparison: data.comparison, threshold_pct: data.threshold_pct,
    requested_start: data.requested_start, requested_end: data.requested_end,
    first_date: data.first_date, last_date: data.last_date, data_as_of: data.data_as_of,
    bar_count: data.bar_count, eligible_count: data.eligible_count, excluded_count: data.excluded_count,
    matched_count: data.matched_count, match_rate_pct: data.match_rate_pct,
    truncated: data.truncated, coverage_verified: false, price_basis: 'stored_ohlc', adjustment_status: 'unverified',
    chart_generated: chart?.kind === 'candlestick_v1' && same(chart.columns, chartColumns) &&
      count(chartCount) && chartCount === data.bar_count && chartCount > 0 }
}

export const researchRules = '行情统计事实约束：research_results 来自本轮 analyze_kline/execute_python 原始工具结果。' +
  'execute_python 的 execution_valid 仅验证执行记录结构；status=succeeded 才表示 Python 执行及 JSON 输出成功，其他状态没有有效计算结果。formula_verified=false 表示脚本公式未经独立验证，不能把实际执行等同于算法必然正确；仅引用对应脚本真实 result，不引用错误输出或编造计数。源码与输入哈希、完整图表保留在结果卡，模型内容省略源码/图点不表示未执行。' +
  'summary_valid=true 表示结构和计数一致，不能推导交易日完整或已复权；false 表示无法核实统计摘要，不要引用其中的精确计数。' +
  'matched_count 是命中次数，eligible_count 是可计算收益的样本数，excluded_count 是无法计算的行，bar_count 包含有效与排除样本。' +
  'match_rate_pct 是样本占比，不是未来发生概率；零有效样本返回 null，不是0%。' +
  '只按实际 period、metric、comparison、threshold_pct、日期范围报告；周/月线不能当日线样本。close_return 使用本地相邻收盘，high_return 使用当前最高价相对前一本地收盘。' +
  'coverage_verified=false、adjustment_status=unverified 必须保持未知；truncated=false 或 adj_factor 全1不证明数据完整或没有除权。' +
  'price_basis=qfq且adjustment_status=verified证明服务端已按有来源的日线因子前复权，报告adjustments中的实际基准日；这不证明脚本公式、交易日完整性或账户总回报。复权来源未核验时，不得把close*factor/latest_factor恒等变换标注为前复权/后复权或回答复权后谁更好；明确该口径无法完成，不默认用原始价格排名替代。价格差或相同回撤日期不能证明差异来自费用、跟踪成本或分红；一页资讯不能证明全库没有分红信息。' +
  'chart_generated=true 才表示有非空 K 线图；图表原始行情已保留在工具结果卡中，模型内容省略 chart.rows 不表示图未生成。' +
  '结果充分后直接交付结论、明细和图表说明，不等待用户再说继续。'
