import { createHash } from 'node:crypto'

const own = (value, key) => Object.hasOwn(value ?? {}, key)
const array = value => Array.isArray(value) ? value : []
const tag = value => typeof value === 'string' && /^[A-Za-z][A-Za-z0-9_.:/-]{0,127}$/.test(value) ? value : undefined
const number = value => Number.isSafeInteger(value) && value >= 0 ? value : undefined
const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex')
export const strategyTools = new Set(['validate_strategy', 'create_strategy', 'update_strategy', 'get_strategy'])

export function parseArguments(value) {
  try {
    const parsed = typeof value === 'string' ? JSON.parse(value) : value
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
  } catch { return {} }
}

function field(value, key) {
  return !own(value, key) ? { state: 'missing' } : value[key] === null ? { state: 'null' }
    : Array.isArray(value[key]) ? { state: value[key].length ? 'returned' : 'empty', count: value[key].length }
      : { state: 'returned' }
}

// Only closed vocabularies, numbers and hashes enter the system evidence block.
// Source code, descriptions and diagnostic messages remain untrusted tool data.
export function strategyFact(call, event, data, failed) {
  const args = parseArguments(call.arguments)
  const input = args.strategy
  const fact = { tool: call.name, call_seq: call.seq, source_seq: event.seq,
    outcome: failed ? 'tool_failed' : 'tool_succeeded' }
  if (input && typeof input === 'object') {
    fact.input = { submitted_definition_hash: hash(input), mode: input.body?.mode === undefined ? 'config' : ['config', 'script'].includes(input.body.mode) ? input.body.mode : 'unknown',
      description: field(input, 'description'),
      ...(own(input, 'description') ? { description_hash: hash(input.description) } : {}),
      ...(typeof input.body?.code === 'string' ? { code_hash: hash(input.body.code) } : {}) }
  }
  if (failed) return fact
  if (call.name === 'validate_strategy') {
    if (typeof data?.valid === 'boolean') fact.valid = data.valid
    fact.fields = Object.fromEntries(['errors', 'checks', 'warnings', 'script_report', 'validation_id', 'validation_expires_at'].map(key => [key, field(data, key)]))
    fact.error_paths = array(data?.errors).map(row => tag(row?.path)).filter(Boolean)
    fact.checks = array(data?.checks).map(tag).filter(Boolean)
    fact.receipt_issued = typeof data?.validation_id === 'string' && /^[0-9a-f]{64}$/.test(data.validation_id)
    const report = data?.script_report
    if (report && typeof report === 'object') {
      fact.report = { compile: tag(report.compile), branch_coverage_measured: typeof report.branch_coverage_measured === 'boolean' ? report.branch_coverage_measured : undefined,
        smoke: { status: tag(report.smoke?.status), attempted: number(report.smoke?.attempted), passed: number(report.smoke?.passed),
          signals: Object.fromEntries(['buy', 'sell', 'none'].map(key => [key, number(report.smoke?.signals?.[key])])) },
        behavior: { status: tag(report.behavior?.status), suites: array(report.behavior?.suites).map(tag).filter(Boolean),
          cases: array(report.behavior?.cases).map(row => ({ suite: tag(row?.suite), case: tag(row?.case), passed: typeof row?.passed === 'boolean' ? row.passed : undefined })) },
        diagnostics: array(report.diagnostics).map(row => ({ code: tag(row?.code), phase: tag(row?.phase), path: tag(row?.path),
          line: number(row?.line), column: number(row?.column), bar_index: number(row?.bar_index), position_state: tag(row?.position_state) })) }
    }
  } else {
    fact.persisted = { id: number(data?.id), version: number(data?.version) }
  }
  return fact
}

export const strategyRules = '策略事实约束：shares 的单位是股，比例参数取0到1。script 允许 lang=starlark；indicators/rules 可省略、null 或 []，不能含配置逻辑。' +
  'behavior_tests 只适用于 script；config 必须省略或 []。ma3_cross_up_entry 只检查入场：前一 close <= 前一 MA3 且当前 close > 当前 MA3；' +
  '当前 MA3 包含当前 close，前后两个 MA3 共需四根行情；不检查固定数量或退出。flat_buy_100_exit_after_one_day 才要求空仓买100股、入场当日等待、至少一自然日后全部卖出（含亏损）。' +
  'validation_id 在相同用户、会话、checker_revision、完整归一化定义不变且30分钟有效期内可复用；description 也绑定。审批独立，不得说审批拒绝就使校验失效。' +
  'missing、empty、null、not_requested、not_run、failed 含义不同；工具成功不等于 valid=true；smoke 不证明全部分支覆盖，也不是行情回测。' +
  '类型支持与本次实际测试输入不同：例如只提交 shares=100/200/300 不能声称本次已测试浮点；引用字段时核对每次调用来源，不能遗漏校验参数。'

export function renderStrategyFacts(evidence) {
  const rows = evidence.strategy_results ?? []
  const lines = ['以下为本次工具记录能确认的结果：']
  for (const row of rows) {
    const ref = `${row.tool}（记录 ${row.source_seq}）`
    if (row.outcome === 'tool_failed') { lines.push(`- ${ref}：调用失败；不能据此认定已保存。`); continue }
    if (row.tool !== 'validate_strategy') {
      lines.push(`- ${ref}：成功；ID=${row.persisted?.id ?? '未返回'}，版本=${row.persisted?.version ?? '未返回'}。`)
      continue
    }
    lines.push(`- ${ref}：valid=${row.valid ?? '未返回'}；errors/checks/warnings 条数分别为 ${['errors', 'checks', 'warnings'].map(key => row.fields?.[key]?.count ?? '未返回数组').join('/')}。`)
    if (row.error_paths?.length) lines.push(`  错误位置：${row.error_paths.join('、')}。`)
    if (row.report) {
      const r = row.report
      lines.push(`  编译=${r.compile ?? '未返回'}；smoke=${r.smoke.status ?? '未返回'}（尝试 ${r.smoke.attempted ?? '未返回'}，通过 ${r.smoke.passed ?? '未返回'}）；行为测试=${r.behavior.status ?? '未返回'}。`)
      lines.push(`  信号 buy/sell/none=${['buy', 'sell', 'none'].map(key => r.smoke.signals[key] ?? '未返回').join('/')}；branch_coverage_measured=${r.branch_coverage_measured ?? '未返回'}。`)
      if (r.diagnostics.length) lines.push('  诊断：' + r.diagnostics.map(d => `${d.code ?? 'unknown'}@${d.path ?? 'unknown'}`).join('、') + '。')
      for (const c of r.behavior.cases) lines.push(`  ${c.suite}/${c.case}：${c.passed === undefined ? '未返回' : c.passed ? '通过' : '失败'}。`)
    }
    if (row.receipt_issued) lines.push('  已签发校验凭据；同用户、同会话、检查器版本和完整定义不变且未过期时可复用，保存仍需独立审批。')
  }
  if (!rows.length) lines.push('当前没有可核实的策略工具结果，不能确认校验、保存或回测完成。')
  return lines.join('\n')
}

// Raw paired sources are supplied only as user data to the isolated reviewer.
// No prior answers or compacted history can authorize a claim.
export function strategyReviewSources(events, boundary, isOriginal) {
  const calls = new Map(), sources = []
  for (const event of events) {
    if (event.seq <= boundary) continue
    if (event.type === 'tool/call') calls.set(`${event.data.turn}:${event.data.step}:${event.data.callId}`, event)
    if (event.type !== 'tool/result' || !isOriginal(event)) continue
    const result = event.data.message?.content?.[0]
    const key = `${event.data.turn}:${event.data.step}:${result?.toolCallId}`
    const call = calls.get(key)
    if (!call || result?.type !== 'tool-result') continue
    calls.delete(key)
    sources.push({ call_seq: call.seq, source_seq: event.seq, tool: call.data.name,
      arguments: parseArguments(call.data.arguments), isError: !!(result.isError || event.data.error), content: result.content })
  }
  return sources
}
