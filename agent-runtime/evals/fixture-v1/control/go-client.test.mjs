import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, readdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createInterface } from 'node:readline'
import test, { before, after } from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import { fixture, request, runtimeRoot, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'
import { sessionRequest } from '../../../fixtures/session-config.mjs'

let buildDirectory, binary
const schemas = new Map()
before(async () => {
  buildDirectory = await mkdtemp(join(tmpdir(), 'q4d-go-bridge-'))
  binary = join(buildDirectory, 'bridge.test')
  await run('go', ['test', '-mod=readonly', '-c', '-tags=bridgeintegration', '-o', binary, './internal/agentbridge'], {
    cwd: resolve(runtimeRoot, '..'), env: process.env,
  })
  const ajv = new Ajv2020({ strict: true, allErrors: true })
  addFormats(ajv)
  for (const file of await readdir(join(runtimeRoot, 'contracts/bridge-v1'))) {
    if (!file.endsWith('.schema.json')) continue
    const schema = JSON.parse(await readFile(join(runtimeRoot, 'contracts/bridge-v1', file), 'utf8'))
    ajv.addSchema(schema, file)
  }
  for (const file of ['session-created', 'prompt-response', 'run', 'event', 'transcript-page', 'session-closed']) schemas.set(file, ajv.getSchema(`${file}.schema.json`))
}, { timeout: 120_000 })
after(async () => { if (buildDirectory) await rm(buildDirectory, { recursive: true, force: true }) })

function run(command, args, options, gateway) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { ...options, stdio: ['pipe', 'pipe', 'pipe'] })
    const timer = setTimeout(() => { child.kill('SIGKILL') }, gateway ? 30_000 : 120_000)
    let output = '', receiptError
    child.stderr.on('data', chunk => { output += chunk })
    const lines = createInterface({ input: child.stdout })
    lines.on('line', line => {
      if (!line.startsWith('Q4D_APPROVE\t')) { output += line + '\n'; return }
      try { child.stdin.write(gateway.approve(JSON.parse(line.slice(12))) + '\n') }
      catch (error) { receiptError = error; child.kill('SIGKILL') }
    })
    child.on('error', error => { clearTimeout(timer); lines.close(); reject(error) })
    child.on('exit', (code, signal) => {
      clearTimeout(timer); lines.close()
      if (receiptError) return reject(receiptError)
      if (code !== 0) return reject(new Error(`Go fixture failed (${code ?? signal}): ${output}`))
      resolve()
    })
    child.stdin.on('error', () => {}) // A killed fixture may close the signer pipe.
  })
}

async function setup(t, text) {
  const gateway = await gatewayFixture(t)
  const environment = await fixture(t)
  const stateFile = join(buildDirectory, `${t.name}.json`)
  const launch = () => environment.launch({ env: {
    Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken,
    Q4D_T00_CAPABILITY_PUBLIC_KEY: gateway.publicKey,
  } })
  const issue = (text, runId) => ({ ...request(text, runId), run_capability: gateway.issueRun(runId, { sessionId: 'session-1' }).capability })
  const input = { token: bridgeToken, session: sessionRequest(), prompt: issue(text, 'run-1'), next_prompt: issue('query', 'run-2'), state_file: stateFile }
  return { gateway, environment, launch, async execute(host, phase) {
    await run(binary, ['-test.run=^TestCordisBridge$', '-test.v', '-test.timeout=25s'], {
      cwd: resolve(runtimeRoot, '..'), env: { PATH: process.env.PATH, Q4D_BRIDGE_TEST_INPUT: JSON.stringify({ ...input, url: host.url, phase }) },
    }, gateway)
    const state = JSON.parse(await readFile(stateFile, 'utf8'))
    for (const [schema, values] of [
      ['session-created', [state.created]], ['prompt-response', [state.accepted]], ['run', [state.run]],
      ['event', state.events], ['transcript-page', state.pages], ['session-closed', [state.closed]],
    ]) for (const value of values) {
      const validate = schemas.get(schema)
      assert.ok(validate(value), `Go ${schema} roundtrip violates shared schema: ${JSON.stringify(validate.errors)}`)
    }
    const files = await readdir(environment.directory, { recursive: true, withFileTypes: true })
    const artifacts = [await readFile(stateFile, 'utf8')]
    for (const file of files.filter(file => file.isFile())) artifacts.push(await readFile(join(file.parentPath, file.name), 'utf8'))
    for (const secret of [bridgeToken, gateway.runtimeToken, input.prompt.run_capability, input.next_prompt.run_capability]) {
      assert.ok(artifacts.every(content => !content.includes(secret)), 'credential persisted in fixture output')
    }
    return state
  } }
}

test('TEST-GO-BRIDGE-01 Go creates, admits, resumes SSE, paginates and retries cold after Cordis restart', { timeout: 40_000 }, async t => {
  const { launch, execute, gateway } = await setup(t, 'query')
  const first = await launch()
  const before = await execute(first, 'first')
  assert.equal(gateway.executions.length, 1)
  await first.close()
  const second = await launch()
  const after = await execute(second, 'restart')
  assert.deepEqual(after.created, before.created)
  assert.equal(gateway.executions.length, 2, 'old admission and cold replay must not execute again')
  assert.ok(after.pages.flatMap(page => page.items).some(item => item.run_id === 'run-1'))
  assert.ok(after.pages.flatMap(page => page.items).some(item => item.run_id === 'run-2'))
})

for (const phase of ['cancel', 'reject', 'allow']) {
  test(`TEST-GO-BRIDGE-02 Go ${phase} controls a live Cordis Run over HTTP`, { timeout: 30_000 }, async t => {
    const { launch, execute, gateway } = await setup(t, phase === 'cancel' ? 'gateway-wait' : 'gateway-approval')
    const host = await launch()
    const state = await execute(host, phase)
    assert.equal(state.run.state, phase === 'cancel' ? 'cancelled' : 'completed')
    assert.equal(gateway.executions.length, phase === 'allow' ? 1 : 0)
    assert.equal(state.events.some(event => event.type === 'tool.started'), phase !== 'reject')
  })
}
