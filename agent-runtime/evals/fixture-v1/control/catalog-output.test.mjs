import assert from 'node:assert/strict'
import { execFile, spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { join, resolve } from 'node:path'
import { promisify } from 'node:util'
import test from 'node:test'
import { TrustedGateway } from '../../../src/mcp/trusted-gateway.mjs'

const root = resolve(import.meta.dirname, '../../../..')
let exported
function catalog() {
  return exported ??= promisify(execFile)('go', ['test', '-tags=agentcatalogintegration', './internal/application',
    '-run=^TestAgentCatalogContractFixture$', '-v', '-count=1'], { cwd: root, timeout: 100000, maxBuffer: 1024 * 1024 })
    .then(({ stdout }) => {
      const value = stdout.split('\n').find(line => line.startsWith('Q4D_CATALOG\t'))?.slice(12)
      assert.ok(value, 'actual Go catalog fixture missing')
      return value
    })
}
async function probe(mode = 'register') {
  const input = await catalog()
  const source = resolve(process.env.Q4D_DSH_SOURCE_DIR)
  const loader = createRequire(join(source, 'package.json')).resolve('tsx/esm')
  const child = spawn(process.execPath, ['--import', loader, join(root, 'agent-runtime/fixtures/catalog-output-host.ts'), mode], {
    env: { PATH: process.env.PATH, TSX_TSCONFIG_PATH: join(source, 'tsconfig.json'), DSH_TELEMETRY_DISABLED: '1' },
    timeout: 30000,
    killSignal: 'SIGKILL',
    stdio: ['pipe', 'pipe', 'pipe'],
  })
  let output = '', error = ''
  child.stdout.on('data', bytes => { output += bytes })
  child.stderr.on('data', bytes => { error += bytes })
  const finished = new Promise((resolve, reject) => { child.on('error', reject); child.on('exit', code => resolve(code)) })
  child.stdin.end(input)
  assert.equal(await finished, 0, error)
  return JSON.parse(output)
}

// Real Go schemas are essential: simplified fixtures missed behavior.size.
test('TEST-CATALOG-OUTPUT-01 actual strategy schemas register in pinned DSH', { timeout: 120000 }, async () => {
  const expected = JSON.parse(await catalog()).tools.map(tool => 'mcp__q4d__' + tool.name).sort()
  assert.equal(expected.length, 19)
  assert.ok(expected.includes('mcp__q4d__analyze_kline'))
  assert.ok(expected.includes('mcp__q4d__execute_python'))
  assert.deepEqual((await probe()).checked.sort(), expected)
})

test('TEST-CATALOG-RESEARCH-05 real Go chart schema validates full payload before compact model rendering', { timeout: 120000 }, async () => {
  const { research } = await probe('research')
  assert.deepEqual(research.map(row => row.isError), [false, true, true])
  const valid = research[0]
  assert.equal(valid.full_points, 2)
  assert.equal(valid.original_points, 2)
  assert.equal(valid.content.data.chart.rows, undefined)
  assert.equal(valid.content.data.chart.point_count, 2)
  assert.equal(valid.content.data.chart.rows_omitted, true)
  assert.equal(valid.content.data.matches.rows.length, 1)
  assert.equal(valid.content.data.matched_count, 1)
  assert.deepEqual(valid.rendered, valid.content)
  assert.ok(research.slice(1).every(row => row.event.data.code === 'agent_tool_failed'))
})

test('TEST-CATALOG-OUTPUT-02 validation failures retain actionable codes; unknown errors stay private', { timeout: 120000 }, async () => {
  const { failures } = await probe('failures')
  for (const item of failures.slice(0, 3)) {
    assert.equal(item.result.isError, true)
    assert.equal(item.event.data.code, 'agent_' + item.input)
    const text = JSON.parse(item.result.content[0].text)
    assert.equal(text.error.code, 'agent_' + item.input)
    assert.equal(typeof text.error.meaning, 'string')
  }
  assert.equal(failures[3].event.data.code, 'agent_tool_failed')
  assert.equal(failures[3].result.content[0].text.includes('private_unknown_backend_error'), false)
})


test('TEST-CATALOG-OUTPUT-03 full Ajv checks still reject invalid sizes and bounds after DSH projection', { timeout: 120000 }, async () => {
  const { outputs } = await probe('outputs')
  assert.deepEqual(outputs.map(row => row.isError), [false, true, true])
  assert.equal(outputs[0].event.type, 'tool.completed')
  assert.ok(outputs.slice(1).every(row => row.event.data.code === 'agent_tool_failed'))
})

test('TEST-CATALOG-INPUT-04 real schemas bind mode to suites and focus diagnostics without relaxing admission', { timeout: 120000 }, async () => {
  const tools = JSON.parse(await catalog()).tools
  const gateway = new TrustedGateway({ url: 'http://127.0.0.1/internal/mcp', runtimeToken: 'fixture-token', catalog: tools })
  gateway.beginRun('dsh', { sessionId: 'session', runId: 'run', capability: 'fixture-capability', expiresAt: Date.now() + 60000,
    allowedTools: ['validate_strategy', 'create_strategy', 'update_strategy'] })
  const config = { name: 'config', universe: ['sh.600809'], period: '1d', body: { indicators: [], rules: [], execution: { fill_at: 'next_open' } } }
  for (const mode of [undefined, 'config']) {
    config.body.mode = mode
    if (mode === undefined) delete config.body.mode
    gateway.prepare('dsh', 'validate_strategy', { strategy: config })
    gateway.prepare('dsh', 'validate_strategy', { strategy: config, behavior_tests: [] })
    assert.throws(() => gateway.prepare('dsh', 'validate_strategy', { strategy: config, behavior_tests: ['flat_buy_100_exit_after_one_day'] }),
      error => error.validation.length === 1 && error.validation[0].path === '/behavior_tests')
  }
  const script = { ...config, body: { mode: 'script', lang: 'starlark', code: 'def on_bar(ctx):\n    return None', execution: { fill_at: 'next_open' } } }
  for (const name of ['validate_strategy', 'create_strategy', 'update_strategy']) {
    const base = { strategy: script, ...(name === 'validate_strategy' ? {} : { validation_id: 'a'.repeat(64) }),
      ...(name === 'update_strategy' ? { id: 1, expected_version: 1 } : {}) }
    for (const empty of [undefined, null, []]) {
      for (const key of ['indicators', 'rules']) {
        if (empty === undefined) delete script.body[key]
        else script.body[key] = empty
      }
      gateway.prepare('dsh', name, base)
    }
    script.body.indicators = [{}]
    assert.throws(() => gateway.prepare('dsh', name, base), error => error.validation.length === 1 &&
      error.validation[0].path === '/strategy/body/indicators' && error.validation[0].message.includes('0 items'))
    script.body.indicators = null
    script.body.lang = 'python'
    assert.throws(() => gateway.prepare('dsh', name, base), error => error.validation[0].path === '/strategy/body/lang')
    script.body.lang = 'starlark'
  }
})
