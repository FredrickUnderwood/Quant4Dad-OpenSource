import { readFileSync } from 'node:fs'
import { installResearchTools } from '../src/runtime/research-tools.ts'
import { assertSupportedJsonSchema } from '@deepseek-ai/dsh-tools'

// Pure adapter probe: no model, network, session files or product data.
const catalog = JSON.parse(readFileSync(0, 'utf8'))
const mode = process.argv[2] ?? 'register'
if (mode === 'failures') catalog.tools = catalog.tools.filter((t: any) => t.name === 'create_strategy')
if (mode === 'outputs') catalog.tools = catalog.tools.filter((t: any) => t.name === 'validate_strategy')
if (mode === 'research') catalog.tools = catalog.tools.filter((t: any) => t.name === 'analyze_kline')
const checked: string[] = [], observed: any[] = []
const definitions = new Map<string, any>()
const hooks = new Map<string, (...args: any[]) => any>()
let guard: any, failureCode = '', payload: any
const ctx: any = { on(event: string, handler: any) { hooks.set(event, handler) }, tools: {
  guard(handler: any) { guard = handler }, register(tool: any) {
    assertSupportedJsonSchema(tool.output.schema)
    checked.push(tool.name)
    definitions.set(tool.name, tool)
  },
} }
const signal = new AbortController().signal
installResearchTools(ctx, catalog, { beforeTool: async () => 1,
  execution: () => ({ lease: { tool() {}, check() {}, signal }, gateway: {
    prepare: (_session: string, name: string, args: any) => ({ name, arguments: args, tool_call_id: 'fixture-call' }),
    invoke: async () => ['outputs', 'research'].includes(mode) ? { isError: false, structuredContent: payload } : { isError: true, content: [{ type: 'text', text: failureCode }] },
  } as any }), observe: (type, _call, data) => observed.push({ type, data }) })(ctx)
const failures: any[] = []
if (mode === 'failures') for (const code of ['strategy_validation_required', 'strategy_validation_mismatch', 'strategy_validation_expired', 'private_unknown_backend_error']) {
  failureCode = code
  const exec: any = { name: 'mcp__q4d__create_strategy', arguments: {}, agent: { session: { id: 'fixture' } }, token: {}, signal }
  await hooks.get('tools/pre-execute')!(exec, () => undefined)
  if (guard(exec) !== undefined) throw new Error('fixture_guard_rejected')
  const result = await hooks.get('tools/execute')!(exec)
  failures.push({ input: code, result, event: observed.at(-1) })
  hooks.get('tools/result')!(exec)
}
const outputs: any[] = []
if (mode === 'outputs') for (const variant of ['valid', 'zero_size', 'too_many_calls']) {
  payload = { untrusted_data: true, data: { valid: true, errors: [], checks: [], warnings: [], checker_revision: 'fixture',
    script_report: { checker_revision: 'fixture', compile: 'passed', branch_coverage_measured: false, diagnostics: [],
      smoke: { status: 'passed', bars: 64, position_states: ['flat', 'held_flat', 'held_profit', 'held_loss'], attempted: variant === 'too_many_calls' ? 257 : 256, passed: 256, signals: { buy: 64, sell: 0, none: 192 } },
      behavior: { status: 'passed', suites: ['fixture'], cases: [{ suite: 'fixture', case: 'size', passed: true,
        expected: { action: 'buy', size: { fixed_cash: variant === 'zero_size' ? 0 : 100 } } }] },
    } } }
  const exec: any = { name: 'mcp__q4d__validate_strategy', arguments: {}, agent: { session: { id: 'fixture' } }, token: {}, signal }
  await hooks.get('tools/pre-execute')!(exec, () => undefined)
  if (guard(exec) !== undefined) throw new Error('fixture_guard_rejected')
  const result = await hooks.get('tools/execute')!(exec)
  outputs.push({ variant, isError: result.isError, event: observed.at(-1) })
  hooks.get('tools/result')!(exec)
}
const research: any[] = []
if (mode === 'research') for (const variant of ['valid', 'short_row', 'too_many_bars']) {
  payload = { untrusted_data: true, data: { kind: 'kline_analysis_v1', file_id: 'a'.repeat(64), code: 'sh.688289', period: '1d',
    requested_start: '2025-01-01', requested_end: '2025-01-03', first_date: '2025-01-02', last_date: '2025-01-03', data_as_of: '2025-01-03',
    truncated: false, coverage_verified: false, price_basis: 'stored_ohlc', adjustment_status: 'unverified', volume_unit: 'unspecified',
    metric: 'close_return', comparison: 'gte', threshold_pct: 4, bar_count: 2, eligible_count: 1, excluded_count: 1, matched_count: 1, match_rate_pct: 100,
    warnings: [], matches: { columns: ['date', 'previous_date', 'previous_close', 'price', 'return_pct'], rows: [['2025-01-03', '2025-01-02', 100, 104, 4]] },
    chart: { kind: 'candlestick_v1', columns: ['date', 'open', 'high', 'low', 'close', 'volume'],
      rows: [['2025-01-02', 99, 101, 98, 100, 500], ['2025-01-03', 100, 105, 99, 104, 800]] } } }
  if (variant === 'short_row') payload.data.chart.rows[0].pop()
  if (variant === 'too_many_bars') payload.data.chart.rows = Array.from({ length: 501 }, () => payload.data.chart.rows[0])
  const exec: any = { name: 'mcp__q4d__analyze_kline', arguments: {}, agent: { session: { id: 'fixture' } }, token: {}, signal }
  await hooks.get('tools/pre-execute')!(exec, () => undefined)
  if (guard(exec) !== undefined) throw new Error('fixture_guard_rejected')
  const result = await hooks.get('tools/execute')!(exec)
  const content = result.isError ? undefined : JSON.parse(result.content[0].text)
  const rendered = result.isError ? undefined : JSON.parse(definitions.get(exec.name).output.render({}, result.value)[0].text)
  research.push({ variant, isError: result.isError, event: observed.at(-1), full_points: result.value?.data?.chart?.rows?.length,
    content, rendered, original_points: payload.data.chart.rows.length })
  hooks.get('tools/result')!(exec)
}
process.stdout.write(JSON.stringify({ checked, failures, outputs, research }) + '\n')
