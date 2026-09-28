import { strategyFact, strategyRules, strategyTools } from './strategy-evidence.mjs'
import { researchFact, researchRules, researchTools } from './research-evidence.mjs'

// Only original, paired tool results contribute facts. Assistant
// text, user text and compaction summaries are never evidence sources.
const identifier = /^[A-Za-z][A-Za-z0-9_]{0,63}$/
const own = (value, key) => Object.hasOwn(value ?? {}, key)

export function indicatorFacts(data) {
  if (!Array.isArray(data?.items) || data.items.length > 128 || data.count !== data.items.length) return undefined
  const items = data.items
  if (items.some(item => typeof item?.name !== 'string' || !identifier.test(item.name) || !item.parameter_schema?.properties ||
    typeof item.parameter_schema.properties !== 'object' ||
    Array.isArray(item.parameter_schema.properties) || !Array.isArray(item.parameter_schema.required) ||
    item.parameter_schema.required.some(field => !own(item.parameter_schema.properties, field)))) return undefined
  if (new Set(items.map(item => item.name)).size !== items.length) return undefined
  const fields = [...new Set(items.flatMap(item => Object.keys(item.parameter_schema.properties)))].sort()
  if (fields.length > 64 || fields.some(field => !identifier.test(field))) return undefined
  const parameters = items.map(item => ({ indicator: item.name,
    fields: Object.fromEntries(fields.map(field => {
      const definition = item.parameter_schema.properties[field]
      const present = own(item.parameter_schema.properties, field)
      const hasDefault = present && own(definition, 'default')
      // Never promote arbitrary descriptions or string values into a trusted
      // prompt. Defaults are represented as JSON data inside the evidence block.
      const value = hasDefault ? definition.default : undefined
      const safeDefault = typeof value === 'boolean' || typeof value === 'number' && Number.isFinite(value) ||
        typeof value === 'string' && identifier.test(value)
      return [field, { present, required: present && item.parameter_schema.required.includes(field),
        has_default: hasDefault, ...(safeDefault ? { default: value } : {}) }]
    })) }))
  return { count: items.length, names: items.map(item => item.name), parameters,
    groups: Object.fromEntries(fields.map(field => {
      const names = parameters.filter(row => row.fields[field].present).map(row => row.indicator)
      return [field, { count: names.length, indicators: names }]
    })) }
}

export function sampleOrder(values) {
  // Only compare fully specified instants. Missing/ambiguous dates do not
  // establish any ordering. Equality is not a strictly increasing sequence.
  if (values.length < 2) return 'insufficient'
  const numbers = []
  for (const value of values) {
    const match = typeof value === 'string' && value.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/)
    if (!match) return 'unknown'
    const date = Date.parse(value.slice(0, 10) + 'T00:00:00Z')
    const seconds = Date.parse(match[1] + match[3])
    if (!Number.isFinite(date) || !Number.isFinite(seconds) || new Date(date).toISOString().slice(0, 10) !== value.slice(0, 10)) return 'unknown'
    numbers.push(BigInt(seconds) * 1000000n + BigInt((match[2] ?? '').padEnd(9, '0')))
  }
  const up = numbers.every((value, index) => !index || value >= numbers[index - 1])
  const down = numbers.every((value, index) => !index || value <= numbers[index - 1])
  return up && down ? 'equal' : up ? 'nondecreasing' : down ? 'nonincreasing' : 'mixed'
}

export function buildAnswerEvidence(events, boundary, isOriginal) {
  const calls = new Map()
  let indicators
  const news = []
  const byTool = new Map(), validations = [], strategies = [], research = []
  let attempted = 0, succeeded = 0, failed = 0
  const names = new Set()
  for (const event of events) {
    if (event.type === 'tool/call' && event.seq > boundary) {
      const name = event.data.name.replace(/^mcp__q4d__/, '')
      calls.set(`${event.data.turn}:${event.data.step}:${event.data.callId}`, { name, seq: event.seq, arguments: event.data.arguments, consumed: false })
      attempted++; names.add(name)
      if (identifier.test(name)) byTool.set(name, (byTool.get(name) ?? 0) + 1)
    }
    if (event.type !== 'tool/result' || event.seq <= boundary || !isOriginal(event)) continue
    const message = event.data.message?.content?.[0]
    const call = calls.get(`${event.data.turn}:${event.data.step}:${message?.toolCallId}`)
    if (!call || call.consumed || message?.type !== 'tool-result') continue
    call.consumed = true
    if (message.isError || event.data.error) {
      failed++
      if (strategyTools.has(call.name)) strategies.push(strategyFact(call, event, undefined, true))
      if (researchTools.has(call.name)) research.push(researchFact(call, event, undefined, true))
      continue
    }
    succeeded++
    if (!['list_indicators', 'list_news', 'validate_pipeline'].includes(call.name) && !strategyTools.has(call.name) && !researchTools.has(call.name)) continue
    let value
    try { value = JSON.parse(message.content.filter(block => block.type === 'text').map(block => block.text).join('')) } catch { continue }
    if (value?.untrusted_data !== true) continue
    if (strategyTools.has(call.name)) strategies.push(strategyFact(call, event, value.data, false))
    if (researchTools.has(call.name)) research.push(researchFact(call, event, value.data, false))
    if (['validate_strategy', 'validate_pipeline'].includes(call.name) && typeof value.data?.valid === 'boolean') {
      validations.push({ source_seq: event.seq, tool: call.name, valid: value.data.valid })
    }
    if (call.name === 'list_indicators') {
      // A newer malformed result invalidates the older snapshot.
      const facts = indicatorFacts(value.data)
      indicators = facts ? { source_seq: event.seq, ...facts } : undefined
    }
    if (call.name === 'list_news' && Array.isArray(value.data?.items)) {
      news.push({ source_seq: event.seq, count: value.data.items.length,
        published_at_sample_order: sampleOrder(value.data.items.map(item => item?.published_at)),
        scope: 'this_page_only_not_query_sort_contract' })
    }
  }
  return { scope: 'current_run_only', tools: { attempted, succeeded, failed, distinct: names.size },
    by_tool: Object.fromEntries([...byTool].sort(([a], [b]) => a.localeCompare(b))),
    ...(validations.length ? { validation_results: validations } : {}),
    ...(strategies.length ? { strategy_results: strategies } : {}),
    ...(research.length ? { research_results: research } : {}),
    ...(indicators ? { indicators } : {}), ...(news.length ? { news_pages: news } : {}) }
}

// The evaluator accepts typed claims, never natural-language guesses. A pass
// only proves these claims; free prose still requires independent review.
export function checkEvidenceClaims(evidence, claims) {
  const errors = []
  const compare = (path, actual, expected) => {
    if (JSON.stringify(actual) !== JSON.stringify(expected)) errors.push({ path, expected, actual })
  }
  if (!claims || typeof claims !== 'object' || Array.isArray(claims)) return [{ path: '$', code: 'claims_object_required' }]
  const keys = new Set(['indicator_count', 'source_count', 'tool_calls', 'tool_kinds', 'indicator_fields', 'news_orders', 'validation_results', 'research_counts'])
  for (const key of Object.keys(claims)) if (!keys.has(key)) errors.push({ path: key, code: 'unknown_claim' })
  for (const key of ['indicator_count', 'source_count']) if (own(claims, key)) {
    if (!evidence.indicators) errors.push({ path: key, code: 'current_indicator_evidence_missing' })
    else compare(key, claims[key], key === 'indicator_count' ? evidence.indicators.count : evidence.indicators.groups.source?.count ?? 0)
  }
  if (own(claims, 'tool_calls')) compare('tool_calls', claims.tool_calls, evidence.tools.attempted)
  if (own(claims, 'tool_kinds')) compare('tool_kinds', claims.tool_kinds, evidence.tools.distinct)
  if (own(claims, 'research_counts')) {
    if (!Array.isArray(claims.research_counts)) errors.push({ path: 'research_counts', code: 'array_required' })
    else compare('research_counts', claims.research_counts, (evidence.research_results ?? []).map(row => row.summary_valid ? {
      matched_count: row.matched_count, eligible_count: row.eligible_count, excluded_count: row.excluded_count, bar_count: row.bar_count,
    } : null))
  }
  if (own(claims, 'validation_results')) {
    if (!Array.isArray(claims.validation_results)) errors.push({ path: 'validation_results', code: 'array_required' })
    else {
      for (const [i, row] of claims.validation_results.entries()) {
        if (!row || typeof row !== 'object' || Array.isArray(row) || Object.keys(row).some(key => !['tool', 'valid'].includes(key))) {
          errors.push({ path: `validation_results.${i}`, code: 'validation_claim_invalid' })
        }
      }
      compare('validation_results', claims.validation_results.map(row => ({ tool: row?.tool, valid: row?.valid })),
        (evidence.validation_results ?? []).map(row => ({ tool: row.tool, valid: row.valid })))
    }
  }
  if (own(claims, 'indicator_fields')) {
    if (!Array.isArray(claims.indicator_fields)) errors.push({ path: 'indicator_fields', code: 'array_required' })
    else {
      const seen = new Set()
      for (const [index, row] of claims.indicator_fields.entries()) {
        const path = `indicator_fields.${index}`
        if (!row || typeof row !== 'object') { errors.push({ path, code: 'object_required' }); continue }
        const identity = `${row.indicator}:${row.field}`
        if (seen.has(identity)) errors.push({ path, code: 'duplicate_claim' })
        seen.add(identity)
        const indicator = evidence.indicators?.parameters.find(item => item.indicator === row.indicator)
        if (!indicator || typeof row.field !== 'string' || !identifier.test(row.field)) { errors.push({ path, code: 'indicator_evidence_missing' }); continue }
        const expected = own(indicator.fields, row.field) ? indicator.fields[row.field] : { present: false, required: false, has_default: false }
        for (const key of Object.keys(row)) if (!['indicator', 'field', 'present', 'required', 'has_default', 'default'].includes(key)) errors.push({ path: `${path}.${key}`, code: 'unknown_claim' })
        for (const key of ['present', 'required', 'has_default']) compare(`${path}.${key}`, row[key], expected[key])
        if (expected.has_default && !own(expected, 'default')) errors.push({ path, code: 'default_not_projected' })
        else if (expected.has_default || own(row, 'default')) compare(`${path}.default`, row.default, expected.default)
      }
    }
  }
  if (own(claims, 'news_orders')) {
    if (!Array.isArray(claims.news_orders)) errors.push({ path: 'news_orders', code: 'array_required' })
    else compare('news_orders', claims.news_orders, (evidence.news_pages ?? []).map(page => page.published_at_sample_order))
  }
  return errors
}

export function evidencePrompt(evidence) {
  if (!evidence.tools.attempted) return ''
  if (Buffer.byteLength(JSON.stringify(evidence)) > 32768) evidence = { scope: evidence.scope, tools: evidence.tools,
    by_tool: Object.fromEntries(Object.entries(evidence.by_tool ?? {}).slice(0, 128)),
    validation_results: evidence.validation_results?.slice(-64), research_results: evidence.research_results?.slice(-8), details_omitted: true }
  if (Buffer.byteLength(JSON.stringify(evidence)) > 32768) evidence = { scope: evidence.scope, tools: evidence.tools, details_omitted: true }
  return '以下是程序从本次任务原始工具记录计算的事实快照，独立于历史回答与压缩摘要。' +
    '字段不存在、字段可选、存在默认值是不同状态；groups 的 count 是支持该字段的指标数。' +
    'tools.attempted 是本次工具调用次数，distinct 是工具种类数；by_tool 为逐工具次数，不能用步骤数或种类数代替调用数。' +
    'details_omitted=true 表示明细省略，不可用局部明细重算总数或宣称已检查全部结果。' +
    '用户未要求调用统计时不主动附加调用总数。validation_results 中 valid=false 是业务校验失败，即使 tools.succeeded 增加也不代表校验通过；用户要求遇错停止时立即停止，不自行修正或继续调用工具。' +
    '新闻时间顺序只描述已返回样本，不证明查询排序规则。' + (evidence.strategy_results?.length ? strategyRules : '') +
    (evidence.research_results?.length ? researchRules : '') +
    '本次工具事实与旧回答冲突时，以本次事实为准并纠正旧说法；没有当前证据的历史状态需重新查询。' +
    '只回答用户所需内容，不附加未经核对的参数、数字或排序结论。\n<q4d_evidence_data>\n' +
    JSON.stringify(evidence) + '\n</q4d_evidence_data>'
}
