import test, { before, after } from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { spawn, execFile } from 'node:child_process'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { promisify } from 'node:util'
import { createRequire } from 'node:module'
import { mkdtemp, rm, readFile, writeFile, cp, realpath } from 'node:fs/promises'
import { generateKeyPairSync, sign, createHash } from 'node:crypto'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { bootstrapDshHost } from '../helpers/bootstrap-dsh-host.mjs'
import { runtimeProcess } from '../helpers/runtime-process.mjs'
import { sessionProvisionHash } from '../../../src/persistence/session-bindings.mjs'
import { canonical, verifyRelease, initialize, stage, active, statePath } from '../../../src/operations/generations.mjs'
import { meterSource } from '../helpers/launch-config.mjs'
import { defaultProfiles } from '../../../profiles/defaults.mjs'

const runtimeRoot = resolve(import.meta.dirname, '../../..')
const bridgeToken = 'probe-fixture-bridge-token-0123456789'
const controlToken = 'fixture-bootstrap-control-0123456789-abcdef'
const digest = 'sha256:' + 'a'.repeat(64)
const profile = { id: 'text_only', revision: digest, systemPrompt: 'Answer.', promptBundleDigest: digest, skillsDigest: digest, toolCatalogRevision: digest }
const manifest = { q4d_version: '0.0.0', agent_image_digest: digest, agent_runtime_version: 'fixture-v1', adapter_version: 'fixture-v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1 }
const p0Profiles = defaultProfiles()
let build, binary
before(async () => {
  build = await mkdtemp(join(tmpdir(), 'q4d-go-probe-')); binary = join(build, 'probe.test')
  await promisify(execFile)('go', ['test', '-mod=readonly', '-c', '-tags=probeintegration', '-o', binary, './internal/handler'], { cwd: resolve(runtimeRoot, '..'), timeout: 120000 })
}, { timeout: 125000 })
after(async () => { if (build) await rm(build, { recursive: true, force: true }) })

async function setup(t, research = false, entrypoint = false, p0 = false, candidateMeter = false) {
  const selectedProfile = research ? p0Profiles.find(p => p.id === 'research') : profile
  let mode = 'ready', held, runtime, p0Tool, outputFailures = 0
  const requests = [], arrival = Promise.withResolvers()
  const model = createServer((req, res) => {
    let bytes = ''
    req.on('data', value => { bytes += value })
    req.on('end', () => {
      const body = JSON.parse(bytes)
      requests.push({ body, path: req.url, authorization: req.headers.authorization, apiKey: req.headers['x-api-key'] })
      const respond = () => {
        if (mode === 'unavailable') { res.writeHead(401, { 'content-type': 'application/json' }); res.end('{"error":{"message":"private-provider-error-marker"}}'); return }
        const currentMessages = p0 || research ? body.messages.slice(body.messages.findLastIndex(m => m.role === 'user') + 1) : body.messages
        const second = currentMessages.some(message => message.role === 'tool' || Array.isArray(message.content) && message.content.some(block => block.type === 'tool_result'))
        const textRun = !body.tools?.length || mode === 'resume-final'
        const args = mode === 'wrong-args' ? '{"sentinel":"Q4D_ECHO_V1_7F2A","extra":true}' : '{"sentinel":"Q4D_ECHO_V1_7F2A"}'
        const business = mode !== 'resume-final' && body.tools?.some(tool => tool.function?.name === 'mcp__q4d__query_kline')
        const summarizing = body.messages.some(m => typeof m.content === 'string' && m.content.startsWith('You are now acting as a compaction engine'))
        const truncated = mode === 'recover-output' && business && second && outputFailures++ === 0
        let delta = mode === 'compaction-tool' && summarizing ? { role: 'assistant', tool_calls: [{ index: 0, id: 'summary-forged-tool', type: 'function', function: { name: 'mcp__q4d__create_strategy', arguments: '{}' } }] } : mode === 'p0' && p0Tool ? (second ? { role: 'assistant', content: '本次工具调用已结束。' } : { role: 'assistant', tool_calls: [{ index: 0, id: 'model-reused-call', type: 'function', function: { name: 'mcp__q4d__' + p0Tool.name, arguments: JSON.stringify(p0Tool.arguments) } }] }) : business ? (second && mode !== 'repeat-tools' ? { role: 'assistant', content: '行情查询完成：最新收盘价 109。' }
          : { role: 'assistant', tool_calls: [{ index: 0, id: 'model-reused-call', type: 'function', function: {
            name: mode === 'business-write' ? 'mcp__q4d__create_strategy' : 'mcp__q4d__query_kline',
            arguments: mode === 'business-forged' ? '{"code":"sh.600519","actor_id":"admin"}' : '{"code":"sh.600519","limit":2}' } }] })
          : textRun ? { role: 'assistant', content: '已完成文本分析 🐉' } : second || mode === 'no-tool' ? { role: 'assistant', content: mode === 'wrong-final' || !second ? 'private-probe-final-marker' : 'Q4D_PROBE_OK' }
          : { role: 'assistant', tool_calls: [{ index: 0, id: 'probe-call', type: 'function', function: { name: mode === 'wrong-tool' ? 'shell' : 'q4d_agent_echo_probe', arguments: args } }] }
        if (second && mode === 'probe-second-bytes') delta = { role: 'assistant', content: 'private-probe-text-marker'.repeat(4000) }
        if (second && mode === 'probe-second-tool') delta = { role: 'assistant', tool_calls: [{ index: 0, id: 'private-probe-call-marker',
          type: 'function', function: { name: 'private-probe-tool-marker', arguments: '{"private-probe-args-marker":true}' } }] }
        if (second && mode === 'probe-reasoning-wrong-final') delta = { role: 'assistant', content: 'private-wrong-echo-marker' }
        if (second && mode === 'probe-reasoning-only') delta = { role: 'assistant' }
        if (truncated) delta = { role: 'assistant', tool_calls: [{ index: 0, id: 'truncated-call', type: 'function', function: { name: 'mcp__q4d__query_kline', arguments: '{\"symbol\":' } }] }
        res.writeHead(200, { 'content-type': 'text/event-stream' })
        if (req.url === '/v1/messages') {
          const events = [
            { type: 'message_start', message: { id: 'msg_probe', type: 'message', role: 'assistant', model: 'fixture-model',
              content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 30, output_tokens: 0 } } },
            { type: 'content_block_start', index: 0, content_block: second ? { type: 'text', text: '' }
              : { type: 'tool_use', id: 'probe-call', name: 'q4d_agent_echo_probe', input: {} } },
            { type: 'content_block_delta', index: 0, delta: second ? { type: 'text_delta', text: 'Q4D_PROBE_OK' }
              : { type: 'input_json_delta', partial_json: args } },
            { type: 'content_block_stop', index: 0 },
            { type: 'message_delta', delta: { stop_reason: second ? 'end_turn' : 'tool_use', stop_sequence: null }, usage: { output_tokens: 20 } },
            { type: 'message_stop' },
          ]
          for (const event of events) res.write(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
          res.end(); return
        }
        if (mode.startsWith('probe-reasoning')) res.write('data: ' + JSON.stringify({ choices: [{
          delta: { role: 'assistant', reasoning_content: 'private-probe-reasoning-marker' }, index: 0, finish_reason: null,
        }] }) + '\n\n')
        for (const value of [{ choices: [{ delta, index: 0, finish_reason: null }] },
          { choices: [{ delta: {}, index: 0, finish_reason: second && mode === 'probe-second-length' || truncated ? 'length' : second && mode === 'probe-second-tool' ? 'stop' : delta.tool_calls ? 'tool_calls' : 'stop' }], usage: { prompt_tokens: 30, completion_tokens: second && mode === 'probe-second-budget' ? 129 : truncated ? (body.max_tokens ?? body.max_completion_tokens) : 20 } }]) {
          res.write('data: ' + JSON.stringify(value) + '\n\n')
        }
        res.end('data: [DONE]\n\n')
      }
      if (mode === 'hold' && !held) { held = respond; arrival.resolve() } else respond()
    })
  })
  await new Promise(resolve => model.listen(0, '127.0.0.1', resolve))
  const child = spawn(binary, ['-test.run=^TestAgentProbeControlHost$', '-test.timeout=80s'], { cwd: resolve(runtimeRoot, '..'),
    env: { PATH: process.env.PATH, Q4D_PROBE_MODEL_URL: `http://127.0.0.1:${model.address().port}/v1`, ...(research ? { Q4D_RESEARCH_FIXTURE: '1' } : {}), ...(p0 ? { Q4D_P0_FIXTURE: '1' } : {}), ...(candidateMeter ? { Q4D_CANDIDATE_METER_FIXTURE: '1' } : {}) }, stdio: ['pipe', 'pipe', 'pipe'] })
  const ready = Promise.withResolvers(), controls = new Map(), exited = once(child, 'exit')
  let controlID = 0
  let logs = ''
  const lines = createInterface({ input: child.stdout })
  lines.on('line', line => {
    if (!line.startsWith('Q4D_PROBE\t')) { logs += line + '\n'; return }
    const data = JSON.parse(line.slice(10))
    if (data.event === 'ready') ready.resolve(data.url); else { controls.get(data.id)?.resolve(data); controls.delete(data.id) }
  })
  child.stderr.on('data', bytes => { logs += bytes })
  child.on('error', ready.reject)
  child.on('exit', () => { ready.reject(new Error(logs)); for (const p of controls.values()) p.reject(new Error(logs)); controls.clear() })
  t.after(async () => {
    await runtime?.close(); child.stdin.end()
    const timer = setTimeout(() => child.kill('SIGKILL'), 5000)
    const [code] = await exited.finally(() => clearTimeout(timer)); lines.close()
    model.closeAllConnections(); await new Promise(resolve => model.close(resolve))
    assert.equal(code, 0, logs)
  })
  const url = await ready.promise
  const control = async command => { const id = ++controlID, done = Promise.withResolvers(); controls.set(id, done); child.stdin.write(JSON.stringify({ id, ...command }) + '\n'); return done.promise }
  const selectedMeterSource = candidateMeter ? await readFile(join(runtimeRoot, 'src/meter/byte-budget.mjs'), 'utf8') : undefined
  const launch = root => entrypoint ? runtimeProcess(t, { root, bootstrapURL: url + '/internal/v1/agent/bootstrap',
    controlToken, bridgeToken, selectedProfile, ...(p0 ? { selectedProfiles: p0Profiles } : {}), selectedManifest: manifest, selectedMeterSource })
    : bootstrapDshHost(t, { root, url: url + '/internal/v1/agent/bootstrap', token: controlToken, fixture: 'session-runtime-host.ts', env: {
      Q4D_SESSION_BRIDGE_TOKEN: bridgeToken, Q4D_SESSION_CONFIG: JSON.stringify({ ...(p0 ? { profiles: p0Profiles } : { profile: selectedProfile }), manifest, realProbe: true, realPolicy: true }),
    } })
  runtime = await launch()
  await control({ url: `http://127.0.0.1:${runtime.port}` })
  const api = async (path, body, method = body === undefined ? 'GET' : 'POST', expected = 200, headers = {}) => {
    const response = await fetch(url + '/api/v1/' + path, { method, headers: { cookie: 'q4d_auth=fixture-login-token', 'content-type': 'application/json', ...headers },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(22000) })
    const result = await response.json()
    assert.ok((Array.isArray(expected) ? expected : [expected]).includes(response.status), JSON.stringify({ status: response.status, result }))
    return result
  }
  return { api, requests, url, get runtime() { return runtime }, control, arrival, selectTool(name, args) { mode = 'p0'; p0Tool = { name, arguments: args } },
    async restartRuntime() {
      const root = runtime.directory; await runtime.close()
      runtime = await launch(root)
      await control({ url: `http://127.0.0.1:${runtime.port}` })
    }, modelOrigin: `http://127.0.0.1:${model.address().port}`, get mode() { return mode }, set mode(value) { mode = value }, release: () => held(),
    probe: (force = false, expected = 200) => api('agent/models/fixture/probe', { force }, 'POST', expected),
    async bridge(path, body, status = 200, token = bridgeToken) {
      const response = await fetch(`http://127.0.0.1:${runtime.port}/q4d/v1/${path}`, { method: 'POST', headers: { authorization: 'Bearer ' + token, 'content-type': 'application/json' }, body: JSON.stringify(body) })
      const result = await response.json(); assert.equal(response.status, status, JSON.stringify(result)); return result
    }, logs: () => logs + runtime.logs() }
}


async function probeDiagnostics(s, count) {
  let rows = []
  for (let attempt = 0; attempt < 100; attempt++) {
    rows = s.logs().split('\n').filter(line => line.startsWith('{')).flatMap(line => {
      try { const row = JSON.parse(line); return row.event === 'agent_model_probe_completed' ? [row] : [] } catch { return [] }
    })
    if (rows.length >= count) return rows
    await new Promise(resolve => setTimeout(resolve, 10))
  }
  assert.fail(`expected ${count} probe diagnostics, got ${rows.length}`)
}

test('TEST-MODEL-PROBE-01 Go bootstrap -> Runtime pi-ai -> Go persisted readiness, cache, force and Session gate', { timeout: 30000 }, async t => {
  const s = await setup(t), catalog = await s.api('agent/models')
  const session = { q4d_session_id: 'probe-session', provider: 'fixture', model: 'fixture-model', profile: profile.id,
    profile_revision: digest, model_config_revision: catalog.revision }
  session.provision_request_hash = sessionProvisionHash(session)
  await s.bridge('sessions', session, 503)
  const result = await s.probe()
  assert.equal(result.status, 'ready'); assert.equal(result.model_turns, 2); assert.equal(result.tool_calls, 1)
  assert.equal(result.expires_at_ms - result.checked_at_ms, 86400000)
  assert.equal(s.requests.length, 2)
  for (const req of s.requests) {
    assert.equal(req.authorization, 'Bearer probe-fixture-model-secret')
    assert.equal(req.body.tools.length, 1); assert.equal(req.body.tools[0].function.name, 'q4d_agent_echo_probe')
    assert.equal(req.body.max_tokens ?? req.body.max_completion_tokens, 128)
  }
  assert.ok(s.requests[1].body.messages.some(m => m.role === 'tool' && m.content.includes('Q4D_ECHO_V1_7F2A')))
  assert.equal((await s.api('agent/models')).models[0].status, 'ready')
  await s.bridge('sessions', session)
  const firstLog = (await probeDiagnostics(s, 1))[0]
  assert.equal(firstLog.status, 'ready'); assert.equal(firstLog.check, 'passed')
  assert.equal(firstLog.stage, 'second_turn'); assert.equal(firstLog.checked_at_ms, result.checked_at_ms)
  assert.deepEqual(firstLog.turns.map(row => row.finish), ['tool-calls', 'stop'])
  assert.deepEqual(firstLog.turns.map(row => row.tool_blocks), [1, 0])
  assert.equal(firstLog.turns[1].text_bytes, Buffer.byteLength('Q4D_PROBE_OK'))
  assert.ok(firstLog.turns.every(row => row.chunks > 0 && row.bytes > 0 && row.output_tokens === 20))
  assert.deepEqual(await s.probe(), result); assert.equal(s.requests.length, 2)
  assert.equal((await probeDiagnostics(s, 1)).length, 1)
  await s.probe(true); assert.equal(s.requests.length, 4)
  assert.equal((await probeDiagnostics(s, 2)).length, 2)
  for (const secret of [controlToken, bridgeToken, 'probe-fixture-model-secret', 'private-provider-error-marker']) assert.ok(!s.logs().includes(secret))
})
test('TEST-MODEL-PROBE-02 deterministic Tool/schema/final failures are incompatible and cannot become ready', { timeout: 30000 }, async t => {
  const s = await setup(t)
  const checks = { 'no-tool': 'first_finish', 'wrong-tool': 'first_tool_name', 'wrong-args': 'first_tool_arguments',
    'wrong-final': 'second_final_text', 'probe-second-length': 'second_finish', 'probe-second-tool': 'second_block_types',
    'probe-second-budget': 'output_budget', 'probe-second-bytes': 'stream_bytes' }
  let executions = 0
  for (const [mode, check] of Object.entries(checks)) {
    s.mode = mode
    const result = await s.probe(true)
    assert.equal(result.status, 'incompatible', mode); assert.equal(result.reason, 'probe_contract_mismatch')
    assert.equal((await s.api('agent/models')).models[0].status, 'incompatible')
    const row = (await probeDiagnostics(s, ++executions)).at(-1)
    assert.equal(row.check, check, mode)
    assert.equal(row.status, 'incompatible')
    assert.equal(row.stage, mode === 'wrong-final' || mode.startsWith('probe-second') ? 'second_turn' : 'first_turn')
    assert.deepEqual(Object.keys(row).sort(), ['event', 'probe_version', 'model_config_revision', 'checked_at_ms', 'status',
      'stage', 'check', 'elapsed_ms', 'model_turns', 'tool_calls', 'turns'].sort())
    for (const turn of row.turns) {
      assert.deepEqual(Object.keys(turn).sort(), ['turn', 'chunks', 'bytes', 'finish', 'input_tokens', 'output_tokens', 'text_bytes', 'tool_blocks', 'reasoning_blocks', 'other_blocks'].sort())
      assert.ok(['not_observed', 'stop', 'tool-calls', 'max-tokens', 'aborted', 'error', 'other'].includes(turn.finish))
    }
  }
  for (const marker of ['private-probe-final-marker', 'private-probe-text-marker', 'private-probe-tool-marker',
    'private-probe-args-marker', 'private-probe-call-marker', 'probe-fixture-model-secret']) assert.ok(!s.logs().includes(marker), marker)
})
test('TEST-MODEL-PROBE-REASONING canonical DSH reasoning is accepted while exact visible echo remains required', { timeout: 30000 }, async t => {
  const s = await setup(t)
  s.mode = 'probe-reasoning'
  const result = await s.probe()
  assert.equal(result.status, 'ready')
  assert.equal(result.model_turns, 2)
  assert.equal(result.tool_calls, 1)
  const accepted = (await probeDiagnostics(s, 1))[0]
  assert.equal(accepted.check, 'passed')
  for (const turn of accepted.turns) {
    assert.equal(turn.reasoning_blocks, 1)
    assert.equal(turn.other_blocks, 0)
  }
  for (const [index, mode] of ['probe-reasoning-wrong-final', 'probe-reasoning-only'].entries()) {
    s.mode = mode
    assert.equal((await s.probe(true)).status, 'incompatible', mode)
    const row = (await probeDiagnostics(s, index + 2)).at(-1)
    assert.equal(row.check, 'second_final_text', mode)
    assert.equal(row.turns[1].reasoning_blocks, 1)
  }
  assert.ok(!s.logs().includes('private-probe-reasoning-marker'))
  assert.ok(!s.logs().includes('private-wrong-echo-marker'))
})

test('TEST-MODEL-PROBE-DIAGNOSTICS logger faults cannot change readiness and unknown finish metadata is redacted', { timeout: 30000 }, async () => {
  const source = resolve(process.env.Q4D_DSH_SOURCE_DIR)
  const loader = createRequire(join(source, 'package.json')).resolve('tsx/esm')
  const code = `
    import assert from 'node:assert/strict'
    import { ModelProbe } from ${JSON.stringify(join(runtimeRoot, 'src/models/probe.ts'))}
    const revision = 'a'.repeat(32)
    const snapshot = { revision: 'fixture', model_config_revision: revision,
      providers: [{ id: 'fixture', default_model: 'fixture-model', agent: { context_window: 8192, max_output_tokens: 512 } }] }
    const sync = { readConfiguration: () => snapshot, subscribeInvalidation: () => () => {},
      status: () => ({ configuration_applied: true, model_config_revision: revision }) }
    const request = { provider: 'fixture', model: 'fixture-model', model_config_revision: revision, probe_version: 'echo-v1', force: false }
    let streams = 0, logs = 0, unknown = false
    const applier = { async *stream(options) {
      streams++
      const second = options.messages.length > 1
      yield { type: 'block-end', index: 0, block: second ? { type: 'text', text: 'Q4D_PROBE_OK' } :
        { type: 'tool-call', id: 'private-call-marker', name: 'q4d_agent_echo_probe', arguments: JSON.stringify({ sentinel: 'Q4D_ECHO_V1_7F2A' }) } }
      yield { type: 'finish', reason: { kind: second ? unknown ? 'private-finish-marker' : 'stop' : 'tool-calls', private: 'private-detail-marker' } }
    } }
    const throwing = new ModelProbe(sync, applier, () => 100, () => { logs++; throw new Error('private-logger-marker') })
    const result = await throwing.probe(request)
    assert.equal(result.status, 'ready'); assert.ok(throwing.ready('fixture', 'fixture-model', revision))
    assert.deepEqual(await throwing.probe(request), result); assert.equal(streams, 2); assert.equal(logs, 1)
    throwing.close()
    const rows = []; unknown = true
    const redacted = new ModelProbe(sync, applier, () => 100, row => rows.push(row))
    const failed = await redacted.probe(request)
    assert.equal(failed.status, 'incompatible'); assert.equal(rows[0].check, 'second_finish')
    assert.equal(rows[0].turns[1].finish, 'other')
    assert.ok(!JSON.stringify(rows).includes('private-'))
    redacted.close()
  `
  const result = await promisify(execFile)(process.execPath, ['--import', loader, '--input-type=module', '-e', code],
    { env: { ...process.env, TSX_TSCONFIG_PATH: join(source, 'tsconfig.json'), DSH_TELEMETRY_DISABLED: '1' }, timeout: 20000 })
  assert.equal(result.stdout, ''); assert.equal(result.stderr, '')
})
test('TEST-MODEL-PROBE-03 provider errors have a five-minute unavailable cache without raw diagnostics', { timeout: 30000 }, async t => {
  const s = await setup(t); s.mode = 'unavailable'
  const result = await s.probe()
  assert.equal(result.status, 'unavailable'); assert.equal(result.expires_at_ms - result.checked_at_ms, 300000)
  assert.ok(!JSON.stringify(result).includes('private-provider-error-marker'))
  const row = (await probeDiagnostics(s, 1))[0]
  assert.equal(row.status, 'unavailable'); assert.equal(row.stage, 'first_turn'); assert.equal(row.check, 'stream')
  assert.ok(!s.logs().includes('private-provider-error-marker'))
  await s.probe(); assert.equal(s.requests.length, 1)
  await s.probe(true); assert.equal(s.requests.length, 2)
})
test('TEST-MODEL-PROBE-04 revoke during probing rejects late completion and prevents another model dispatch', { timeout: 30000 }, async t => {
  const s = await setup(t); s.mode = 'hold'
  const pending = s.probe(true, [412, 503]); await s.arrival.promise
  assert.equal((await s.api('agent/models')).models[0].status, 'probing')
  await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  s.mode = 'ready'; s.release(); await pending
  assert.equal((await s.api('agent/models')).models[0].reason, 'credential_revoked')
  const count = s.requests.length
  await s.probe(false, 422); assert.equal(s.requests.length, count)
})
test('TEST-MODEL-PROBE-05 concurrent force requests coalesce provider work and only newest Go attempt publishes', { timeout: 30000 }, async t => {
  const s = await setup(t); s.mode = 'hold'
  const one = s.probe(true, [200, 412]); await s.arrival.promise
  const two = s.probe(true, [200, 412])
  // Wait for the second HTTP call's pending state to reach the Runtime before release.
  await new Promise(resolve => setTimeout(resolve, 100))
  s.mode = 'ready'; s.release()
  const results = await Promise.all([one, two])
  assert.equal(results.filter(result => result.status === 'ready').length, 1)
  assert.equal(s.requests.length, 2); assert.equal((await s.api('agent/models')).models[0].status, 'ready')
})
test('TEST-MODEL-PROBE-06 authenticated fixed contract rejects arbitrary inputs and control-token reuse', { timeout: 30000 }, async t => {
  const s = await setup(t), catalog = await s.api('agent/models')
  await s.api('agent/models/fixture/probe', { force: true, prompt: 'user data' }, 'POST', 400)
  await s.api('agent/models/fixture/probe?force=true', {}, 'POST', 400)
  await s.api('agent/models/missing/probe', {}, 'POST', 404)
  const request = { provider: 'fixture', model: 'fixture-model', model_config_revision: catalog.revision, probe_version: 'echo-v1', force: false }
  await s.bridge('models/probe', request, 401, controlToken)
  await s.bridge('models/probe', { ...request, endpoint: 'http://forged' }, 400)
  await s.bridge('models/probe', { ...request, model_config_revision: 'a'.repeat(32) }, 409)
  assert.equal(s.requests.length, 0)
  await s.api('settings/llm-providers/fixture', { agent: { context_window: 128, max_output_tokens: 64 } }, 'PATCH')
  assert.equal((await s.probe()).status, 'incompatible')
  assert.equal(s.requests.length, 0) // Fixed prompt and Tool framing must fit too.
})

test('TEST-MODEL-PROBE-07 Anthropic Messages completes the same echo contract with explicit credentials', { timeout: 30000 }, async t => {
  const s = await setup(t)
  await s.api('settings/llm-providers/fixture', { type: 'anthropic', base_url: s.modelOrigin }, 'PATCH')
  const result = await s.probe()
  assert.equal(result.status, 'ready'); assert.equal(s.requests.length, 2)
  for (const request of s.requests) {
    assert.equal(request.path, '/v1/messages'); assert.equal(request.apiKey, 'probe-fixture-model-secret')
    assert.equal(request.body.max_tokens, 128)
    assert.equal(request.body.tools.length, 1); assert.equal(request.body.tools[0].name, 'q4d_agent_echo_probe')
  }
  assert.equal((await s.api('agent/models')).models[0].status, 'ready')
})

test('TEST-MODEL-PROBE-08 a silent provider is aborted within the fixed 15-second budget', { timeout: 30000 }, async t => {
  const s = await setup(t); s.mode = 'hold'
  const started = performance.now(), pending = s.probe()
  await s.arrival.promise
  const result = await pending
  assert.equal(result.status, 'unavailable'); assert.equal(result.model_turns, 1); assert.equal(result.tool_calls, 0)
  assert.ok(performance.now() - started < 18000)
  assert.equal((await s.api('agent/models')).models[0].status, 'unavailable')
  assert.equal(s.requests.length, 1)
})


const sessionInput = { provider: 'fixture', model: 'fixture-model', profile: 'text_only', title: '研究会话' }
const createProductSession = (s, key, body = sessionInput, expected = 201) => s.api('agent/sessions', body, 'POST', expected, { 'idempotency-key': key })
const messageInput = { client_request_id: 'message-1', content: [{ type: 'text', text: '请分析 🐉 <private-run-text>\n保持原文' }] }
const sendMessage = (s, session, body = messageInput, expected = 202) => s.api(`agent/sessions/${session}/messages`, body, 'POST', expected)
test('TEST-GENERATION-01 actual DSH data survives signed candidate cold verification, activation and rollback without model calls', { timeout: 60000 }, async t => {
  const s = await setup(t, false, true, true); await s.probe()
  const session = await createProductSession(s, 'generation-session', { ...sessionInput, profile: 'research' })
  s.selectTool('list_instruments', { limit: 2 })
  const sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  await s.bridge('maintenance/drain', {}); await s.runtime.close()
  const count = s.requests.length, root = await realpath(await mkdtemp(join(tmpdir(), 'q4d-generation-process-')))
  t.after(() => rm(root, { recursive: true, force: true }))
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  const r = { schema_version: 1, version: manifest.agent_runtime_version, channel: 'verified', image: 'registry.example/q4d@' + digest,
    q4d_version: manifest.q4d_version, adapter_version: manifest.adapter_version, dsh_version: manifest.dsh_version,
    bridge_protocol: 1, session_format: 0, event_journal_format: 1, session_binding_format: 1,
    meter_sha256: 'sha256:' + createHash('sha256').update(meterSource).digest('hex') }
  const publicPEM = publicKey.export({ type: 'spki', format: 'pem' }), document = { release: r, signature: sign(null, Buffer.from(canonical(r)), privateKey).toString('base64url') }
  const verified = verifyRelease(document, publicPEM)
  await writeFile(join(root, 'signed.json'), JSON.stringify(document), { mode: 0o600 }); await writeFile(join(root, 'key.pem'), publicPEM, { mode: 0o600 })
  await initialize(root, 'g1', verified)
  await cp(join(s.runtime.directory, 'state'), statePath(root, 'g1'), { recursive: true })
  const projection = join(root, 'product-sessions.json')
  await writeFile(projection, JSON.stringify([{ id: session.session_id, dsh_session_id: session.session_id, status: 'active' }]), { mode: 0o600 })
  const inspect = () => promisify(execFile)('/bin/bash', [join(runtimeRoot, 'scripts/inspect-sessions.sh'), root, 'g1', projection], { timeout: 20000 })
  assert.equal(JSON.parse((await inspect()).stdout).status, 'consistent')
  await writeFile(projection, '[]')
  await assert.rejects(inspect(), e => e.code === 2 && JSON.parse(e.stdout).issues[0].code === 'runtime_binding_without_product')
  await stage(root, 'g2', verified)
  const candidate = await runtimeProcess(t, { root: join(root, 'generations/g2'), bootstrapURL: s.url + '/internal/v1/agent/bootstrap',
    controlToken, bridgeToken, selectedProfiles: p0Profiles, selectedManifest: manifest })
  const cli = (...args) => promisify(execFile)(process.execPath, [join(runtimeRoot, 'scripts/q4dctl.mjs'), ...args], { timeout: 20000 })
  const proof = JSON.parse((await cli('verify-candidate', root, 'g2', 'http://127.0.0.1:' + candidate.port, join(candidate.directory, 'bridge.token'))).stdout)
  assert.equal(proof.cold_sessions, 1); assert.equal(proof.terminal_runs, 1)
  await candidate.close(); await cli('seal-candidate', root, 'g2')
  await cli('activate', root, 'g2', join(root, 'signed.json'), join(root, 'key.pem'))
  assert.equal((await active(root)).generation, 'g2')
  await cli('rollback', root); assert.equal((await active(root)).generation, 'g1')
  assert.equal(s.requests.length, count)
})
test('TEST-CANDIDATE-METER-01 oversized current input stops after bounded history compaction without another Tool call', { timeout: 30000 }, async t => {
  const s = await setup(t, true, true, false, true); await s.probe()
  const session = await researchSession(s, 'candidate-meter')
  const sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const count = s.requests.length, queries = (await s.control({ research_status: true })).queries
  const large = await sendMessage(s, session.session_id, { client_request_id: 'oversized-meter-input', content: [{ type: 'text', text: '界'.repeat(25000) }] })
  assert.equal((await terminalRun(s, large.run_id)).state, 'failed')
  assert.equal(s.requests.length, count + 1)
  assert.ok(s.requests[count].body.messages.some(m => typeof m.content === 'string' && m.content.startsWith('You are now acting as a compaction engine')))
  assert.ok(!s.requests[count].body.tools?.length)
  const events = eventFrames(await (await browserEvents(s, large.run_id)).text())
  assert.equal(events.filter(e => e.type === 'usage.updated').reduce((sum, e) => sum + e.data.usage.output_tokens, 0), 20)
  assert.equal((await s.control({ research_status: true })).queries, queries)
})
test('TEST-MAINTENANCE-01 drain rejects new work while admitted work and cold reads complete', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'maintenance-session')
  s.mode = 'hold'
  const pending = sendMessage(s, session.session_id)
  await s.arrival.promise
  const draining = await s.bridge('maintenance/drain', {})
  assert.equal(draining.status, 'draining'); assert.equal(draining.active_runs, 1)
  await s.bridge('maintenance/drain', { cancel: true }, 400)
  await s.bridge('models/probe', { provider: 'fixture', model: 'fixture-model', model_config_revision: (await s.api('agent/models')).revision, probe_version: 'echo-v1', force: true }, 503)
  s.mode = 'ready'; s.release()
  const sent = await pending
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  assert.ok((await s.api('agent/sessions/' + session.session_id)).transcript.items.length >= 2)
  const count = s.requests.length
  await sendMessage(s, session.session_id, { ...messageInput, client_request_id: 'while-draining' }, 503)
  assert.equal(s.requests.length, count)
  assert.equal((await s.bridge('maintenance/resume', {})).status, 'active')
  const retried = await sendMessage(s, session.session_id, { ...messageInput, client_request_id: 'while-draining' })
  assert.equal((await terminalRun(s, retried.run_id)).state, 'completed')
  await s.restartRuntime()
  assert.equal((await s.api('agent/status')).status, 'model_check_required')
  await s.probe(true)
  assert.equal((await s.api('agent/status')).status, 'ready')
})
async function terminalRun(s, id, timeout = 5000) {
  const deadline = performance.now() + timeout
  while (performance.now() < deadline) {
    const run = await s.api('agent/runs/' + id)
    if (run.terminal) return run
    await new Promise(resolve => setTimeout(resolve, 30))
  }
  throw new Error('Run did not reach a durable terminal state')
}
test('TEST-RUN-BFF-01 Go text admission, signed original authority, durable query, history and idempotency', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'run-session')
  const sent = await sendMessage(s, session.session_id)
  assert.equal(sent.durable, true); assert.equal(sent.status, 'accepted')
  const run = await terminalRun(s, sent.run_id)
  assert.equal(run.state, 'completed'); assert.equal(run.durable, true)
  assert.match(run.execution_envelope_digest, /^sha256:[0-9a-f]{64}$/)
  const transcript = (await s.api('agent/sessions/' + session.session_id)).transcript
  assert.equal(transcript.items.filter(item => item.role === 'user').length, 1)
  assert.equal(transcript.items.find(item => item.role === 'user').content[0].text, messageInput.content[0].text)
  assert.ok(transcript.items.some(item => item.role === 'assistant' && item.content.some(block => block.text === '已完成文本分析 🐉')))
  const again = await sendMessage(s, session.session_id); assert.equal(again.run_id, sent.run_id)
  await sendMessage(s, session.session_id, { ...messageInput, content: [{ type: 'text', text: 'changed' }] }, 409)
  await sendMessage(s, session.session_id, { ...messageInput, actor_id: 'admin' }, 400)
  await sendMessage(s, session.session_id, { ...messageInput, run_capability: 'forged' }, 400)
  await s.api('agent/runs/' + sent.run_id, undefined, 'GET', 401, { cookie: '' })
  const controlURL = `${s.url}/internal/v1/agent/runs/${sent.run_id}/authorization`
  assert.equal((await fetch(controlURL, { headers: { cookie: 'q4d_auth=fixture-login-token' } })).status, 401)
  const control = await fetch(controlURL, { headers: { authorization: `Bearer ${controlToken}` } })
  assert.equal(control.status, 200)
  const claims = await control.json()
  assert.equal(claims.envelope.run_id, sent.run_id); assert.deepEqual(claims.allowed_tools, [])
  assert.equal(claims.envelope.budgets.max_tool_calls, 0)
  await s.api('agent/sessions/' + session.session_id, { archived: true }, 'PATCH')
  assert.equal((await sendMessage(s, session.session_id)).run_id, sent.run_id)
  await sendMessage(s, session.session_id, { ...messageInput, client_request_id: 'new-message' }, 409)
  assert.equal(s.requests.length, 3)
  for (const secret of ['private-run-text', controlToken, bridgeToken, 'probe-fixture-model-secret', 'run_capability']) assert.ok(!s.logs().includes(secret), secret)
})
test('TEST-RUN-BFF-02 lost prompt acknowledgement recovers through Runtime restart and model revocation', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'lost-run-ack')
  await s.control({ lose_run_reply: true })
  const pending = await sendMessage(s, session.session_id, messageInput, 503)
  assert.ok(pending.run_id)
  await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  await s.restartRuntime()
  const repaired = await sendMessage(s, session.session_id)
  assert.equal(repaired.run_id, pending.run_id); assert.equal(repaired.durable, true)
  const run = await terminalRun(s, pending.run_id)
  assert.ok(['completed', 'failed', 'interrupted'].includes(run.state))
  assert.equal(s.requests.length, 3)
  assert.equal((await s.api('agent/sessions/' + session.session_id)).transcript.items.filter(item => item.role === 'user').length, 1)
})
test('TEST-RUN-BFF-03 cancel persists revocation and stops a silent model through the product API', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'cancel-run'); s.mode = 'hold'
  const pending = sendMessage(s, session.session_id); await s.arrival.promise
  const sent = await pending
  await s.api('agent/runs/' + sent.run_id + '/cancel', {})
  const run = await terminalRun(s, sent.run_id)
  assert.equal(run.state, 'cancelled')
  assert.equal((await s.api('agent/runs/' + sent.run_id + '/cancel', {})).state, 'cancelled')
  assert.equal(s.requests.length, 3)
  const policy = await fetch(`${s.url}/internal/v1/agent/runs/${sent.run_id}/authorization`, { headers: { authorization: `Bearer ${controlToken}` } })
  assert.equal(policy.status, 403)
})
test('TEST-RUN-BFF-04 current Go model policy revokes a silent Run without waiting for Bootstrap polling', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'revoke-run'); s.mode = 'hold'
  const pending = sendMessage(s, session.session_id); await s.arrival.promise
  const sent = await pending
  const started = performance.now()
  await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  const run = await terminalRun(s, sent.run_id, 3000)
  assert.equal(run.state, 'failed'); assert.ok(performance.now() - started < 3000)
  assert.equal(s.requests.length, 3)
})
test('TEST-RUN-BFF-05 concurrent retries share one durable prompt and a busy Session preserves the next request identity', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'concurrent-run'); s.mode = 'hold'
  const first = sendMessage(s, session.session_id); await s.arrival.promise
  const sent = await first
  const duplicates = await Promise.all(Array.from({ length: 6 }, () => sendMessage(s, session.session_id)))
  for (const duplicate of duplicates) assert.equal(duplicate.run_id, sent.run_id)
  assert.equal(s.requests.length, 3)
  const nextInput = { ...messageInput, client_request_id: 'message-2' }
  const busy = await sendMessage(s, session.session_id, nextInput, 409)
  assert.equal(busy.message, 'agent_run_in_progress'); assert.ok(busy.run_id)
  await s.api('agent/runs/' + sent.run_id + '/cancel', {})
  await terminalRun(s, sent.run_id)
  s.mode = 'ready'
  const next = await sendMessage(s, session.session_id, nextInput)
  assert.equal(next.run_id, busy.run_id)
  assert.equal((await terminalRun(s, next.run_id)).state, 'completed')
  assert.equal(s.requests.length, 4)
  assert.equal((await s.api('agent/sessions/' + session.session_id)).transcript.items.filter(item => item.role === 'user').length, 2)
})
test('TEST-SESSION-BFF-01 authenticated Go creation, durable Runtime binding, DB metadata and transcript', { timeout: 30000 }, async t => {
  const s = await setup(t)
  await createProductSession(s, 'before-ready', sessionInput, 422)
  await s.probe()
  const first = await createProductSession(s, 'create-1')
  assert.match(first.session_id, /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/); assert.equal(first.status, 'active')
  const again = await createProductSession(s, 'create-1'); assert.equal(again.session_id, first.session_id)
  await createProductSession(s, 'create-1', { ...sessionInput, title: 'different' }, 409)
  const path = 'agent/sessions/' + first.session_id
  const edited = await s.api(path, { title: '新标题', archived: true }, 'PATCH')
  assert.equal(edited.title, '新标题'); assert.equal(edited.status, 'archived')
  const detail = await s.api(path)
  assert.equal(detail.session.session_id, first.session_id); assert.equal(detail.transcript.session_id, first.session_id)
  assert.deepEqual(detail.transcript.items, [])
  assert.equal((await createProductSession(s, 'create-1')).title, '新标题')
  await s.api(path, { archived: false }, 'PATCH')
  const second = await createProductSession(s, 'create-2')
  const page = await s.api('agent/sessions?limit=1')
  const next = await s.api('agent/sessions?limit=1&cursor=' + page.next_cursor)
  assert.deepEqual(new Set([page.items[0].session_id, next.items[0].session_id]), new Set([first.session_id, second.session_id]))
  assert.equal(next.next_cursor, null); assert.equal(s.requests.length, 2)
})
test('TEST-SESSION-BFF-02 lost Bridge acknowledgement reconciles after Runtime restart and model revocation', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  await s.control({ lose_session_reply: true })
  const pending = await createProductSession(s, 'lost-ack', sessionInput, 202)
  assert.equal(pending.status, 'provisioning_failed')
  assert.equal((await s.api('agent/sessions/' + pending.session_id)).transcript, null)
  await s.api('agent/sessions/' + pending.session_id, { archived: true }, 'PATCH', 409)
  await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  await s.restartRuntime()
  const repaired = await s.api('agent/sessions/' + pending.session_id + '/reconcile', {})
  assert.equal(repaired.status, 'active'); assert.equal(repaired.session_id, pending.session_id)
  assert.equal((await createProductSession(s, 'lost-ack')).session_id, pending.session_id)
  assert.equal((await s.api('agent/sessions')).items.length, 1)
  assert.deepEqual((await s.api('agent/sessions/' + pending.session_id)).transcript.items, [])
  await createProductSession(s, 'another-new', sessionInput, 422)
  assert.equal(s.requests.length, 2)
})
test('TEST-SESSION-BFF-03 strict input, cookie authentication and no caller-supplied actor or Runtime state', { timeout: 30000 }, async t => {
  const s = await setup(t)
  await s.api('agent/sessions', undefined, 'GET', 401, { cookie: '' })
  await s.api('agent/sessions', sessionInput, 'POST', 400) // Missing idempotency key.
  await createProductSession(s, 'bad actor', sessionInput, 400)
  await createProductSession(s, 'extra', { ...sessionInput, actor_id: 'admin' }, 400)
  await createProductSession(s, 'extra', { ...sessionInput, status: 'active' }, 400)
  await createProductSession(s, 'extra', { ...sessionInput, profile: 'not-configured' }, 422)
  for (const suffix of ['?limit=0', '?limit=01', '?limit=1&limit=2', '?cursor=bad', '?actor_id=admin']) await s.api('agent/sessions' + suffix, undefined, 'GET', 400)
  await s.api('agent/sessions/00000000000000000000000000/reconcile', {}, 'POST', 404)
  assert.equal(s.requests.length, 0)
})

const eventFrames = text => text.split('\n\n').filter(frame => frame.startsWith('id: ')).map(frame => {
  const lines = frame.split('\n'), event = JSON.parse(lines.find(line => line.startsWith('data: ')).slice(6))
  assert.equal(lines[0], 'id: ' + event.id); assert.equal(lines[1], 'event: ' + event.type)
  return event
})
const browserEvents = (s, id, cursor = '0', signal = AbortSignal.timeout(5000), cookie = 'q4d_auth=fixture-login-token') => fetch(`${s.url}/api/v1/agent/runs/${id}/events`, {
  headers: { cookie, accept: 'text/event-stream', 'Last-Event-ID': cursor }, signal,
})
test('TEST-BROWSER-SSE-01 authenticated replay is contiguous, terminal replay is empty and invalid cursors stay JSON', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  assert.deepEqual(await s.api('agent/options'), { profiles: ['text_only'], text_only: true })
  const session = await createProductSession(s, 'browser-replay'), sent = await sendMessage(s, session.session_id)
  assert.equal(sent.events_url, `/api/v1/agent/runs/${sent.run_id}/events`)
  const run = await terminalRun(s, sent.run_id), response = await browserEvents(s, sent.run_id)
  assert.equal(response.status, 200); assert.match(response.headers.get('content-type'), /^text\/event-stream/)
  assert.equal(response.headers.get('x-accel-buffering'), 'no'); assert.equal(response.headers.get('cache-control'), 'no-store')
  const events = eventFrames(await response.text())
  assert.equal(events[0].type, 'run.started'); assert.equal(events.at(-1).type, 'run.completed')
  for (const [index, event] of events.entries()) {
    assert.equal(event.id, String(index + 1)); assert.equal(event.session_id, session.session_id); assert.equal(event.run_id, sent.run_id)
  }
  assert.ok(events.some(event => event.type === 'message.completed' && event.data.text.includes('🐉')))
  const replay = await browserEvents(s, sent.run_id, '1')
  assert.deepEqual(eventFrames(await replay.text()), events.slice(1))
  const empty = await browserEvents(s, sent.run_id, run.last_event_id)
  assert.equal(empty.status, 200); assert.deepEqual(eventFrames(await empty.text()), [])
  for (const cursor of ['01', '-1', '1e3', '9007199254740993']) {
    const invalid = await browserEvents(s, sent.run_id, cursor)
    assert.equal(invalid.status, 400); assert.match(invalid.headers.get('content-type'), /^application\/json/)
    assert.equal((await invalid.json()).message, 'agent_invalid_event_cursor')
  }
  assert.equal((await browserEvents(s, sent.run_id, '0', undefined, '')).status, 401)
  await s.control({ expire_events: true })
  const expired = await browserEvents(s, sent.run_id)
  assert.equal(expired.status, 410); assert.match(expired.headers.get('content-type'), /^application\/json/)
  assert.equal((await expired.json()).message, 'agent_event_cursor_expired')
  assert.equal(s.requests.length, 3)
})
test('TEST-BROWSER-SSE-02 disconnect only closes the subscription; explicit cancel is replayed durably', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'browser-disconnect'); s.mode = 'hold'
  const sending = sendMessage(s, session.session_id); await s.arrival.promise
  const sent = await sending, abort = new AbortController(), started = performance.now()
  const response = await browserEvents(s, sent.run_id, '0', abort.signal)
  assert.equal(response.status, 200); assert.ok(performance.now() - started < 2000)
  const reader = response.body.getReader(); let text = ''
  while (!text.includes('event: run.started')) { const chunk = await reader.read(); assert.equal(chunk.done, false); text += new TextDecoder().decode(chunk.value) }
  abort.abort(); await reader.cancel().catch(() => {}); reader.releaseLock()
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'running')
  await s.api('agent/runs/' + sent.run_id + '/cancel', {})
  assert.equal((await terminalRun(s, sent.run_id)).state, 'cancelled')
  const events = eventFrames(await (await browserEvents(s, sent.run_id, '1')).text())
  assert.equal(events.at(-1).type, 'run.cancelled')
  assert.equal(s.requests.length, 3)
  assert.equal((await s.api('agent/sessions/' + session.session_id)).transcript.items.filter(item => item.role === 'user').length, 1)
})

const researchSession = (s, key) => createProductSession(s, key, { ...sessionInput, profile: 'research' })

test('TEST-COMPACTION-01 budgeted public compaction preserves full history and Tools after cold reload', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe()
  const session = await researchSession(s, 'compaction')
  const text = 'synthetic context '.repeat(1600)
  const first = await sendMessage(s, session.session_id, { client_request_id: 'long-message', content: [{ type: 'text', text }] })
  assert.equal((await terminalRun(s, first.run_id, 10000)).state, 'completed')
  const sent = await sendMessage(s, session.session_id, { client_request_id: 'compact-history', content: [{ type: 'text', text: 'recent context '.repeat(400) }] })
  assert.equal((await terminalRun(s, sent.run_id, 10000)).state, 'completed')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.ok(events.some(e => e.type === 'context.compacted'), JSON.stringify(events))
  const summaries = s.requests.filter(r => r.body.messages.some(m => typeof m.content === 'string' && m.content.startsWith('You are now acting as a compaction engine')))
  assert.equal(summaries.length, 1); assert.ok(!summaries[0].body.tools?.length)
  assert.ok((summaries[0].body.max_tokens ?? summaries[0].body.max_completion_tokens) <= 512)
  const history = await s.api('agent/sessions/' + session.session_id)
  assert.ok(history.transcript.items.some(m => m.role === 'user' && m.content.some(c => c.text === text)))
  await s.restartRuntime()
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
  await s.probe(true)
  const next = await sendMessage(s, session.session_id, { client_request_id: 'after-compaction', content: [{ type: 'text', text: '继续查询最新行情。' }] })
  assert.equal((await terminalRun(s, next.run_id)).state, 'completed')
  assert.ok(eventFrames(await (await browserEvents(s, next.run_id)).text()).some(e => e.type === 'tool.completed'))
})

test('TEST-COMPACTION-02 a summary Tool call is rejected without dispatch and ends the Run', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe()
  const session = await researchSession(s, 'compaction-reject')
  const first = await sendMessage(s, session.session_id, { client_request_id: 'long-message', content: [{ type: 'text', text: 'synthetic context '.repeat(1600) }] })
  assert.equal((await terminalRun(s, first.run_id, 10000)).state, 'completed')
  s.mode = 'compaction-tool'
  const sent = await sendMessage(s, session.session_id, { client_request_id: 'compact-history', content: [{ type: 'text', text: 'recent context '.repeat(400) }] })
  const terminal = await terminalRun(s, sent.run_id, 10000)
  assert.equal(terminal.state, 'failed')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.ok(!events.some(e => e.type === 'context.compacted'))
  const ledger = await s.control({ research_status: true })
  assert.equal(ledger.audits.length, 2); assert.ok(ledger.audits.every(row => row.ToolName === 'query_kline'))
  assert.equal(ledger.queries, 2); assert.equal(s.requests.length, 6)
})

async function approvalEvent(s, run) {
  const response = await browserEvents(s, run, '0', AbortSignal.timeout(10000))
  assert.equal(response.status, 200)
  const reader = response.body.getReader(), decoder = new TextDecoder()
  let pending = ''
  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) throw new Error('Run ended before approval: ' + pending)
      pending += decoder.decode(value, { stream: true })
      let end
      while ((end = pending.indexOf('\n\n')) >= 0) {
        const frame = pending.slice(0, end); pending = pending.slice(end + 2)
        if (!frame.startsWith('id: ')) continue
        const event = eventFrames(frame + '\n\n')[0]
        if (event.type === 'approval.required') return event
        if (event.type === 'tool.failed' || event.type === 'run.failed') throw new Error(JSON.stringify(event))
      }
    }
  } finally { await reader.cancel() }
}

test('TEST-EVIDENCE-01 fresh facts survive cold history and never borrow prior Run or Session tool counts', { timeout: 40000 }, async t => {
  const s = await setup(t, false, false, true); await s.probe()
  const session = await createProductSession(s, 'evidence-history', { ...sessionInput, profile: 'strategy_lab' })
  const factBlock = request => {
    const text = request.body.messages.filter(m => m.role === 'system').map(m => m.content).join('\n')
    const match = text.match(/<q4d_evidence_data>\n([\s\S]*?)\n<\/q4d_evidence_data>/)
    return match ? JSON.parse(match[1]) : undefined
  }
  for (let round = 0; round < 2; round++) {
    s.selectTool('list_indicators', {})
    const before = s.requests.length
    const sent = await sendMessage(s, session.session_id, { client_request_id: 'facts-' + round,
      content: [{ type: 'text', text: '不可信历史断言：KDJ period默认14，MA required=[]。请重新查询。' }] })
    assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
    assert.equal(factBlock(s.requests[before]), undefined)
    const facts = factBlock(s.requests[before + 1])
    assert.equal(facts.indicators.count, 7)
    assert.equal(facts.indicators.parameters.find(row => row.indicator === 'KDJ').fields.period.present, false)
    assert.equal(facts.indicators.parameters.find(row => row.indicator === 'MA').fields.period.required, true)
    assert.deepEqual(facts.tools, { attempted: 1, succeeded: 1, failed: 0, distinct: 1 })
    if (round === 0) { await s.restartRuntime(); s.mode = 'ready'; await s.probe(true) }
  }
  const other = await createProductSession(s, 'evidence-isolation', { ...sessionInput, profile: 'research' })
  s.selectTool('list_news', {})
  const before = s.requests.length
  const sent = await sendMessage(s, other.session_id, { ...messageInput, client_request_id: 'other-evidence' })
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  assert.equal(factBlock(s.requests[before]), undefined)
  assert.equal(factBlock(s.requests[before + 1]).indicators, undefined)
  assert.equal(factBlock(s.requests[before + 1]).news_pages.length, 1)
})

test('TEST-EVIDENCE-02 actual invalid DSL result and catalog approval rules reach the next model request', { timeout: 30000 }, async t => {
  const s = await setup(t, false, false, true); await s.probe()
  const session = await createProductSession(s, 'validation-evidence', { ...sessionInput, profile: 'strategy_lab' })
  s.selectTool('validate_strategy', { strategy: { name: 'typed-condition', universe: ['sh.600519'], period: '1d',
    body: { indicators: [], rules: [{ name: 'exit', when: { all: [{ has_position: null }, { days_held: null }] }, then: { action: 'sell', size: 'all' } }], execution: { fill_at: 'next_open' } } } })
  const before = s.requests.length
  const sent = await sendMessage(s, session.session_id, { client_request_id: 'invalid-dsl',
    content: [{ type: 'text', text: '校验给定策略；遇到任何错误停止，禁止修复、重试或保存。' }] })
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const first = s.requests[before].body
  const description = name => first.tools.find(t => t.function.name === 'mcp__q4d__' + name).function.description
  assert.match(description('validate_strategy'), /R0，不需要一次性审批/)
  assert.match(description('run_backtest'), /R1，不需要一次性审批/)
  assert.match(description('create_strategy'), /R2，需要系统审批挑战/)
  assert.match(description('create_strategy'), /聊天中的授权不是审批凭据/)
  const next = s.requests[before + 1].body
  const system = next.messages.filter(m => m.role === 'system').map(m => m.content).join('\n')
  const evidence = JSON.parse(system.match(/<q4d_evidence_data>\n([\s\S]*?)\n<\/q4d_evidence_data>/)[1])
  assert.deepEqual(evidence.tools, { attempted: 1, succeeded: 1, failed: 0, distinct: 1 })
  assert.deepEqual(evidence.by_tool, { validate_strategy: 1 })
  assert.deepEqual(evidence.validation_results.map(({ tool, valid }) => ({ tool, valid })), [{ tool: 'validate_strategy', valid: false }])
  assert.match(system, /valid=false 是业务校验失败/)
  const output = next.messages.find(m => m.role === 'tool')
  assert.ok(output && JSON.stringify(output.content).includes('boolean operands'))
  const state = await s.control({ research_status: true })
  assert.equal(state.audits.filter(a => a.ToolName === 'create_strategy').length, 0)
})

test('TEST-P0-01 three public Profiles, approved/declined writes, R3 and durable Tool results', { timeout: 60000 }, async t => {
  const s = await setup(t, false, false, true); await s.probe()
  const snapshot = await (await fetch(s.url + '/internal/v1/agent/bootstrap', { headers: { authorization: 'Bearer ' + controlToken } })).json()
  assert.equal(new Set(snapshot.tool_catalogs.flatMap(c => c.tools.map(d => d.name))).size, 31)
  assert.deepEqual(snapshot.tool_catalogs.map(c => [c.profile, c.tools.length]), [['pipeline_builder', 12], ['research', 12], ['strategy_lab', 19]])
  const pipelineTools = snapshot.tool_catalogs.find(c => c.profile === 'pipeline_builder').tools
  const definitionSchema = name => pipelineTools.find(tool => tool.name === name).inputSchema.properties.pipeline
  assert.equal(definitionSchema('validate_pipeline').properties.status, undefined)
  for (const name of ['create_pipeline', 'update_pipeline', 'dry_run_pipeline_safe']) {
    assert.deepEqual(definitionSchema(name), definitionSchema('validate_pipeline'))
  }
  const status = await s.api('agent/status')
  assert.equal(status.status, 'ready'); assert.ok(status.profiles.every(p => p.tools.every(tool => tool.available)))
  const exercised = new Set()
  let sequence = 0
  const invoke = async (profileID, name, args, decision, expectedRisk, failure, existingSession) => {
    t.diagnostic('P0 Tool: ' + profileID + '/' + name)
    const session = existingSession ?? await createProductSession(s, 'p0-session-' + ++sequence, { ...sessionInput, profile: profileID })
    s.selectTool(name, args)
    const before = s.requests.length
    const sent = await sendMessage(s, session.session_id, { ...messageInput, client_request_id: 'p0-message-' + ++sequence })
    let approval
    if (decision) {
      const event = await approvalEvent(s, sent.run_id)
      assert.equal(event.data.risk, expectedRisk)
      approval = await s.api('agent/approvals/' + event.data.approval_id)
      assert.equal(approval.status, 'pending'); assert.equal(approval.run_id, sent.run_id)
      const pending = await s.api(`agent/runs/${sent.run_id}/tools/${event.data.tool_call_id}`)
      assert.equal(pending.status, 'pending_approval'); assert.equal(pending.result, undefined)
      const result = await s.api('agent/approvals/' + approval.id + '/decision', { decision })
      assert.equal(result.delivered, true)
      assert.ok(!JSON.stringify(result).includes('receipt'))
    }
    const frames = eventFrames(await (await browserEvents(s, sent.run_id)).text().catch(async error => {
      const ledger = await s.control({ research_status: true })
      throw new Error(JSON.stringify({ name, run: await s.api('agent/runs/' + sent.run_id), audits: ledger.audits.map(a => ({ name: a.ToolName, status: a.Status, code: a.ErrorCode })), requests: s.requests.length - before }), { cause: error })
    }))
    const terminal = await terminalRun(s, sent.run_id)
    assert.equal(terminal.state, 'completed', JSON.stringify(frames))
    const proposed = frames.find(e => e.type === 'tool.proposed')
    assert.equal(proposed.data.arguments_omitted, undefined, JSON.stringify({ name, args, frames }))
    const result = await s.api(`agent/runs/${sent.run_id}/tools/${proposed.data.tool_call_id}`)
    assert.equal(result.tool_name, name)
    const model = s.requests[before].body
    const declared = snapshot.tool_catalogs.find(c => c.profile === profileID).tools.map(d => 'mcp__q4d__' + d.name).sort()
    assert.deepEqual(model.tools.map(d => d.function.name).sort(), declared)
    for (const tool of snapshot.tool_catalogs.find(c => c.profile === profileID).tools.filter(d => ['list_news', 'get_news'].includes(d.name))) {
      const description = model.tools.find(d => d.function.name === 'mcp__q4d__' + tool.name).function.description
      assert.ok(description.startsWith(tool.description))
      assert.match(description, /R0，不需要一次性审批/)
    }

    if (profileID === 'strategy_lab') {
      const paramsDescription = schema => schema.properties.strategy.properties.body.oneOf.find(s => s.properties.mode.enum.includes('config')).properties.indicators.items.properties.params.description
      for (const toolName of ['validate_strategy', 'create_strategy', 'update_strategy']) {
        const definition = snapshot.tool_catalogs.find(c => c.profile === profileID).tools.find(d => d.name === toolName)
        const sentTool = model.tools.find(d => d.function.name === 'mcp__q4d__' + toolName)
        assert.equal(paramsDescription(sentTool.function.parameters), paramsDescription(definition.inputSchema))
        assert.match(paramsDescription(sentTool.function.parameters), /for the selected indicator type/)
        assert.doesNotMatch(paramsDescription(sentTool.function.parameters), /source defaults to close/)
      }
    }
    const system = JSON.stringify(model.messages.filter(m => m.role === 'system'))
    assert.ok(system.includes(p0Profiles.find(p => p.id === profileID).systemPrompt))
    assert.ok(system.includes('数组长度、count/total') && system.includes('具体根因或其他入口可用'))
    assert.equal(system.includes('逐个指标先检查各自 parameter_schema.properties'), profileID === 'strategy_lab')
    assert.equal(system.includes('required=[] 只表示没有必填字段'), profileID === 'strategy_lab')
    assert.equal(system.includes('用户明确的停止条件优先于修正'), profileID === 'pipeline_builder')
    assert.equal(system.includes('单位与换算仅以工具明示为依据'), profileID !== 'pipeline_builder')
    assert.ok(system.includes(({ research: '研究流程', strategy_lab: '策略构建流程', pipeline_builder: '流水线构建流程' })[profileID]))
    for (const other of ['research', 'strategy_lab', 'pipeline_builder'].filter(p => p !== profileID)) assert.ok(!system.includes('Profile: ' + other))
    if (decision === 'reject') {
      assert.equal(frames.filter(e => e.type === 'tool.started').length, 0)
      assert.equal(frames.find(e => e.type === 'tool.failed').data.code, 'agent_approval_denied')
      assert.equal(result.status, 'failed'); assert.equal(result.error_code, 'agent_approval_denied')
    } else if (failure) {
      assert.equal(result.status, 'failed')
      assert.equal(frames.find(e => e.type === 'tool.failed').data.code, failure)
      if (failure === 'agent_tool_not_found') {
        assert.equal(result.error_code, 'tool_not_found')
        const feedback = s.requests[before + 1].body.messages.findLast(m => m.role === 'tool')
        const error = JSON.parse(feedback.content).error
        assert.equal(error.code, failure)
        assert.equal(error.category, 'resource_not_found')
        assert.equal(error.input_schema_accepted, true)
        assert.match(error.meaning, /resource or record was not found/)
        assert.match(error.meaning, /tool exists and handled the request/)
      }
    } else { assert.equal(result.status, 'succeeded', JSON.stringify([frames, result])); exercised.add(name) }
    for (const secret of ['receipt_nonce', 'approval_receipt', 'ResultRef', bridgeToken, 'probe-fixture-model-secret']) assert.ok(!JSON.stringify([frames, result]).includes(secret))
    return { result, sent, approval, session }
  }
  await invoke('research', 'get_data_coverage', { code: 'sh.600519' })
  for (const [name, args] of [['query_kline', { code: 'sh.600519', limit: 2 }], ['latest_bar_date', { code: 'sh.600519' }], ['get_instrument', { code: 'sh.600519' }], ['list_instruments', {}], ['list_news', {}], ['get_news', { id: 1 }], ['list_events', { pipeline_id: 1 }], ['get_event', { id: 1 }]]) await invoke('research', name, args)
  const file = await invoke('research', 'query_kline', { code: 'sh.600519', limit: 10 })
  assert.equal(file.result.result.data.count, 10)
  assert.equal(file.result.result.data.bars, undefined)
  const page = await invoke('research', 'read_kline_file', { file_id: file.result.result.data.file_id, offset: 5, limit: 5 }, undefined, undefined, undefined, file.session)
  assert.equal(page.result.result.data.rows.length, 5)
  assert.equal(page.result.result.data.next_offset, 10)
  assert.equal(page.result.result.data.has_more, false)
  const analysis = await invoke('research', 'analyze_kline', { file_id: file.result.result.data.file_id, comparison: 'lte', threshold_pct: -4 }, undefined, undefined, undefined, file.session)
  assert.equal(analysis.result.result.data.bar_count, 10)
  assert.equal(analysis.result.result.data.eligible_count, 9)
  assert.equal(analysis.result.result.data.matched_count, 0)
  assert.equal(analysis.result.result.data.chart.rows.length, 10)
  assert.equal(analysis.result.result.data.comparison, 'lte')
  const beforePython = s.requests.length
  const python = await invoke('research', 'execute_python', { file_ids: [file.result.result.data.file_id], code: "print('{}')" }, undefined, undefined, undefined, file.session)
  assert.equal(python.result.result.data.status, 'succeeded')
  assert.equal(python.result.result.data.input_count, 10)
  assert.equal(python.result.result.data.source, "print('{}')")
  assert.match(python.result.result.data.input_sha256, /^[a-f0-9]{64}$/)
  const pythonFeedback = JSON.parse(s.requests[beforePython + 1].body.messages.findLast(m => m.role === 'tool').content)
  assert.equal(pythonFeedback.data.source, undefined)
  assert.equal(pythonFeedback.data.source_retained_in_result_card, true)
  const etfs = await invoke('research', 'list_instruments', { asset_type: 'etf' })
  assert.equal(etfs.result.result.data.total, 0)
  const strategy = { name: 'approved strategy', universe: ['sh.600519'], period: '1d', body: { indicators: [{ type: 'MA', alias: 'average', params: { period: 2 } }], rules: [], execution: { fill_at: 'close' } } }
  const validate = async definition => {
    const checked = await invoke('strategy_lab', 'validate_strategy', { strategy: definition })
    assert.equal(checked.result.result.data.valid, true)
    assert.match(checked.result.result.data.validation_id, /^[a-f0-9]{64}$/)
    return checked
  }
  const checkedCreate = await validate(strategy)
  const created = await invoke('strategy_lab', 'create_strategy', { strategy, validation_id: checkedCreate.result.result.data.validation_id },
    'allow_once', 'R2', undefined, checkedCreate.session)
  assert.deepEqual(created.result.result.data, { id: 1, version: 1 })
  await s.api('agent/approvals/' + created.approval.id + '/decision', { decision: 'allow_once' })
  const rejectedStrategy = { ...strategy, name: 'rejected strategy' }, checkedReject = await validate(rejectedStrategy)
  await invoke('strategy_lab', 'create_strategy', { strategy: rejectedStrategy, validation_id: checkedReject.result.result.data.validation_id },
    'reject', 'R2', undefined, checkedReject.session)
  const list = await invoke('strategy_lab', 'list_strategies', {})
  assert.equal(list.result.result.data.count, 1)
  const job = await invoke('strategy_lab', 'run_backtest', { strategy_id: 1, expected_version: 1, initial_capital: 10000, start_date: '2026-09-01', end_date: '2026-09-10' })
  assert.equal(job.result.result.data.job_id, 1)
  const beforeIndicators = s.requests.length
  const indicatorData = (await invoke('strategy_lab', 'list_indicators', {})).result.result.data
  const indicators = indicatorData.items
  assert.equal(indicatorData.count, 7)
  assert.equal(indicatorData.count, indicators.length)
  const indicatorFeedback = JSON.parse(s.requests[beforeIndicators + 1].body.messages.findLast(m => m.role === 'tool').content)
  assert.deepEqual(indicatorFeedback.data, indicatorData)
  assert.equal(indicatorFeedback.untrusted_data, true)
  const evidenceSystem = s.requests[beforeIndicators + 1].body.messages.find(m => m.role === 'system').content
  const evidence = JSON.parse(evidenceSystem.match(/<q4d_evidence_data>\n([\s\S]*?)\n<\/q4d_evidence_data>/)[1])
  assert.deepEqual(evidence.tools, { attempted: 1, succeeded: 1, failed: 0, distinct: 1 })
  assert.equal(evidence.indicators.count, 7)
  assert.equal(evidence.indicators.groups.source.count, 5)
  assert.deepEqual(evidence.indicators.parameters.find(row => row.indicator === 'MA').fields.period,
    { present: true, required: true, has_default: false })
  for (const name of ['KDJ', 'MACD']) assert.deepEqual(evidence.indicators.parameters.find(row => row.indicator === name).fields.period,
    { present: false, required: false, has_default: false })
  assert.deepEqual(indicators.filter(indicator => Object.hasOwn(indicator.parameter_schema.properties, 'source')).map(indicator => indicator.name).sort(), ['BOLL', 'EMA', 'MA', 'MACD', 'RSI'])
  for (const name of ['ATR', 'KDJ']) {
    const indicator = indicators.find(indicator => indicator.name === name)
    assert.equal(indicator.parameter_schema.additionalProperties, false)
    assert.equal(Object.hasOwn(indicator.parameters, 'source'), false)
    assert.equal(Object.hasOwn(indicator.parameter_schema.properties, 'source'), false)
  }
  // Empty required says nothing about fields absent from properties.
  for (const name of ['KDJ', 'MACD']) {
    const indicator = indicators.find(indicator => indicator.name === name)
    assert.deepEqual(indicator.parameter_schema.required, [])
    assert.equal(indicator.parameter_schema.additionalProperties, false)
    assert.equal(Object.hasOwn(indicator.parameters, 'period'), false)
    assert.equal(Object.hasOwn(indicator.parameter_schema.properties, 'period'), false)
  }
  for (const [name, args] of [['get_strategy', { id: 1 }], ['validate_strategy', { strategy }], ['list_cost_models', {}], ['list_backtests', {}]]) await invoke('strategy_lab', name, args)
  const updatedStrategy = { ...strategy, name: 'updated strategy' }, checkedUpdate = await validate(updatedStrategy)
  await invoke('strategy_lab', 'update_strategy', { id: 1, expected_version: 1, strategy: updatedStrategy, validation_id: checkedUpdate.result.result.data.validation_id },
    'allow_once', 'R2', undefined, checkedUpdate.session)
  const pipeline = { name: 'safe draft', nodes: [{ node_key: 'filter', type: 'keyword_filter', config: { keywords: ['blocked'] } }, { node_key: 'delivery', type: 'delivery', config: { channel: 'email', title: 'preview', body: '{{.text}}' } }], edges: [{ from_node_key: 'filter', to_node_key: 'delivery', condition: null }] }
  const draft = await invoke('pipeline_builder', 'create_pipeline', { pipeline }, 'allow_once', 'R2')
  assert.equal(draft.result.result.data.status, 'draft')
  const enabled = await invoke('pipeline_builder', 'set_pipeline_status', { id: 1, expected_version: 1, status: 'enabled' }, 'allow_once', 'R3')
  assert.equal(enabled.result.result.data.status, 'enabled'); assert.equal(enabled.result.result.data.version, 2)
  const update = await invoke('pipeline_builder', 'update_pipeline', { id: 1, expected_version: 2, pipeline: { ...pipeline, name: 'enabled update' } }, 'allow_once', 'R3')
  assert.equal(update.result.result.data.version, 3)
  for (const [name, args] of [['get_pipeline_node_types', {}], ['get_pipeline', { id: 1 }], ['list_pipelines', {}], ['validate_pipeline', { pipeline }], ['dry_run_pipeline_safe', { pipeline, sample_event: { text: 'safe preview' } }]]) await invoke('pipeline_builder', name, args)
  await invoke('pipeline_builder', 'validate_pipeline', { pipeline: { ...pipeline, nodes: [{ node_key: 'invalid', type: 'delivery', config: { channel: 'email', body: '{{undefined}}' } }], edges: [] } }, undefined, undefined, 'agent_tool_invalid_arguments')
  const report = await invoke('strategy_lab', 'get_backtest_job', { id: 1 })
  assert.equal(report.result.result.data.status, 'succeed')
  await invoke('strategy_lab', 'get_backtest_report', { id: 1 })
  assert.equal(exercised.size, 31)
  for (const [profileID, name] of [['research', 'get_news'], ['research', 'get_event'], ['strategy_lab', 'get_strategy'], ['strategy_lab', 'get_backtest_job']]) {
    await invoke(profileID, name, { id: 9007199254740991 }, undefined, undefined, 'agent_tool_not_found')
  }
  const invalidParams = structuredClone(strategy)
  invalidParams.body.indicators[0].params.bogus = 1
  const validation = await invoke('strategy_lab', 'validate_strategy', { strategy: invalidParams })
  assert.equal(validation.result.result.data.valid, false)
  assert.equal(validation.result.result.data.errors[0].path, 'body.indicators[0].params')
  assert.match(validation.result.result.data.errors[0].message, /unknown parameter: bogus/)

  const invalidDSL = structuredClone(strategy)
  invalidDSL.body.rules = [{ name: 'buy', when: { cross_above: ['close', 'average'] }, then: { action: 'buy', size: 'all' } }]
  const badSession = await createProductSession(s, 'invalid-dsl', { ...sessionInput, profile: 'strategy_lab' })
  s.selectTool('validate_strategy', { strategy: invalidDSL })
  const beforeInvalid = s.requests.length
  const invalidRun = await sendMessage(s, badSession.session_id)
  assert.equal((await terminalRun(s, invalidRun.run_id)).state, 'completed')
  const rejected = eventFrames(await (await browserEvents(s, invalidRun.run_id)).text())
  assert.equal(rejected.find(e => e.type === 'tool.failed').data.code, 'agent_tool_context_or_arguments_rejected')
  assert.ok(!rejected.some(e => e.type === 'tool.started'))
  const rejectedCall = rejected.find(e => e.type === 'tool.failed').data.tool_call_id
  const rejectedResult = await s.api(`agent/runs/${invalidRun.run_id}/tools/${rejectedCall}`)
  assert.deepEqual(rejectedResult, { run_id: invalidRun.run_id, tool_call_id: rejectedCall, tool_name: 'validate_strategy', status: 'failed', execution_stage: 'pre_dispatch', error_code: 'agent_tool_context_or_arguments_rejected' })
  const missingCall = await s.api(`agent/runs/${invalidRun.run_id}/tools/01M00000000000000000000009`, undefined, 'GET', 404)
  assert.equal(missingCall.message, 'agent_tool_not_found')

  const feedback = JSON.stringify(s.requests[beforeInvalid + 1].body.messages.filter(m => m.role === 'tool'))
  assert.ok(feedback.includes('/strategy/body/rules/0/when') && feedback.includes('cross_above'))
  await s.restartRuntime()
  const again = await s.api(`agent/runs/${created.sent.run_id}/tools/${created.result.tool_call_id}`)
  assert.deepEqual(again, created.result)
  assert.deepEqual(await s.api(`agent/runs/${invalidRun.run_id}/tools/${rejectedCall}`), rejectedResult)
})

test('TEST-RESEARCH-01 product grant -> public AgentLoop -> real Go Gateway -> bounded market data -> final answer and cold replay', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe()
  assert.deepEqual(await s.api('agent/options'), { profiles: ['research'], text_only: false })
  const session = await researchSession(s, 'research'), sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const snapshot = await (await fetch(s.url + '/internal/v1/agent/bootstrap', { headers: { authorization: 'Bearer ' + controlToken } })).json()
  const claims = await (await fetch(`${s.url}/internal/v1/agent/runs/${sent.run_id}/authorization`, { headers: { authorization: 'Bearer ' + controlToken } })).json()
  assert.deepEqual(claims.allowed_tools, ['get_instrument', 'latest_bar_date', 'list_instruments', 'query_kline'])
  assert.equal(claims.envelope.tool_catalog_revision, snapshot.tool_catalog.revision)
  const business = s.requests.slice(2)
  assert.equal(business.length, 2)
  assert.deepEqual(business[0].body.tools.map(tool => tool.function.name).sort(), claims.allowed_tools.map(name => 'mcp__q4d__' + name))
  const result = JSON.parse(business[1].body.messages.find(message => message.role === 'tool').content)
  assert.equal(result.untrusted_data, true); assert.equal(result.data.count, 2); assert.equal(result.data.truncated, true)
  assert.equal(result.data.bars.at(-1).close, 109)
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.deepEqual(events.filter(event => event.type.startsWith('tool.')).map(event => event.type), ['tool.proposed', 'tool.started', 'tool.completed'])
  const proposal = events.find(event => event.type === 'tool.proposed')
  assert.notEqual(proposal.data.tool_call_id, 'model-reused-call')
  assert.equal(proposal.data.idempotency_key, `q4d:${sent.run_id}:${proposal.data.tool_call_id}`)
  assert.equal(events.at(-1).type, 'run.completed')
  assert.ok(events.some(event => event.type === 'message.completed' && event.data.text.includes('109')))
  const status = await s.control({ research_status: true })
  assert.equal(status.queries, 1); assert.equal(status.audits.length, 1)
  assert.equal(status.audits[0].Status, 'succeeded'); assert.equal(status.audits[0].ToolCallID, proposal.data.tool_call_id)
  assert.ok(status.audits[0].ResultRef)
  await s.restartRuntime()
  assert.equal((await sendMessage(s, session.session_id)).run_id, sent.run_id)
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
  assert.equal((await s.control({ research_status: true })).queries, 1)
  assert.equal(s.requests.length, 4)
  const history = await s.api('agent/sessions/' + session.session_id)
  assert.ok(history.transcript.items.some(item => item.role === 'tool'))
  for (const secret of [controlToken, bridgeToken, 'probe-fixture-model-secret', snapshot.mcp.runtime_token, 'run_capability']) {
    assert.ok(!JSON.stringify([business.map(value => value.body), events, history]).includes(secret))
    assert.ok(!s.logs().includes(secret))
  }
})
test('TEST-RESEARCH-02 forbidden write and forged context are rejected before any Gateway query', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe()
  for (const mode of ['business-write', 'business-forged']) {
    s.mode = mode
    const session = await researchSession(s, mode), sent = await sendMessage(s, session.session_id)
    assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
    const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
    assert.deepEqual(events.filter(event => event.type.startsWith('tool.')).map(event => event.type), ['tool.proposed', 'tool.failed'])
    assert.equal(events.find(event => event.type === 'tool.proposed').data.arguments_omitted, true)
  }
  const status = await s.control({ research_status: true })
  assert.equal(status.queries, 0); assert.equal(status.audits.length, 0)
})
test('TEST-RESEARCH-03 reused call IDs stay bounded and exhausted Tool budget requests a final answer', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe(); s.mode = 'repeat-tools'
  const session = await researchSession(s, 'budget'), sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const status = await s.control({ research_status: true })
  assert.equal(status.queries, 2); assert.equal(status.audits.length, 2)
  assert.notEqual(status.audits[0].ToolCallID, status.audits[1].ToolCallID)
  assert.equal(s.requests.length, 5) // Fixed two-turn probe plus three business model steps.
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.equal(events.filter(event => event.type === 'tool.completed').length, 2)
  assert.equal(events.at(-1).type, 'run.completed')
  assert.ok(!s.requests.at(-1).body.tools?.length)
  assert.ok(JSON.stringify(s.requests.at(-1).body.messages).includes('本次为收尾回答'))
})
for (const action of ['cancel', 'revoke']) test(`TEST-RESEARCH-04 ${action} during a running market query suppresses late results and further model steps`, { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe(); await s.control({ hold_tool: true })
  const session = await researchSession(s, action), sent = await sendMessage(s, session.session_id)
  const until = performance.now() + 4000
  while (!(await s.control({ research_status: true })).queries) {
    assert.ok(performance.now() < until, 'Tool did not dispatch')
    await new Promise(resolve => setTimeout(resolve, 25))
  }
  if (action === 'cancel') await s.api('agent/runs/' + sent.run_id + '/cancel', {})
  else await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  assert.equal((await terminalRun(s, sent.run_id, 3500)).state, action === 'cancel' ? 'cancelled' : 'failed')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.deepEqual(events.filter(event => event.type.startsWith('tool.')).map(event => event.type), ['tool.proposed', 'tool.started', 'tool.failed'])
  assert.equal(s.requests.length, 3)
  await s.restartRuntime()
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
  assert.equal((await s.control({ research_status: true })).queries, 1)
})
test('TEST-RESEARCH-05 Runtime SIGKILL leaves an unknown Tool outcome; authorized Session resume never redispatches it', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe(); await s.control({ hold_tool: true })
  const session = await researchSession(s, 'crash'), sent = await sendMessage(s, session.session_id)
  const until = performance.now() + 4000
  while (!(await s.control({ research_status: true })).queries) {
    assert.ok(performance.now() < until); await new Promise(resolve => setTimeout(resolve, 25))
  }
  await s.runtime.kill(); await s.restartRuntime()
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'recovering')
  assert.equal((await sendMessage(s, session.session_id)).run_id, sent.run_id)
  assert.equal((await s.control({ research_status: true })).queries, 1)
  // A new authorized request loads the Session and settles the old durable
  // proposal conservatively. GET/history and retries themselves never resume.
  await s.probe(true)
  s.mode = 'resume-final' // New turn answers without proposing a fresh query.
  const next = await sendMessage(s, session.session_id, { ...messageInput, client_request_id: 'after-crash' })
  assert.equal((await terminalRun(s, next.run_id)).state, 'completed')
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'interrupted')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.equal(events.find(event => event.type === 'tool.failed').data.code, 'agent_tool_outcome_unknown')
  assert.equal(events.at(-1).type, 'run.interrupted')
  assert.equal((await s.control({ research_status: true })).queries, 1)
  assert.equal(s.requests.length, 6)
})

test('TEST-RUNTIME-PROCESS-01 offline entrypoint runs Go research loop, SIGTERM and persisted replay', { timeout: 45000 }, async t => {
  const s = await setup(t, true, true)
  assert.equal((await fetch(`http://127.0.0.1:${s.runtime.port}/q4d/v1/health`)).status, 401)
  assert.equal((await s.probe()).status, 'ready')
  const session = await researchSession(s, 'entrypoint'), sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.ok(events.some(event => event.type === 'tool.completed'))
  assert.ok(events.some(event => event.type === 'message.completed' && event.data.text.includes('109')))
  assert.equal((await s.control({ research_status: true })).queries, 1)
  const prior = s.runtime
  await s.restartRuntime()
  assert.ok(prior.logs().includes('agent_runtime_stopped'))
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
  assert.equal((await sendMessage(s, session.session_id)).run_id, sent.run_id)
  assert.equal(s.requests.length, 4)
  for (const secret of [controlToken, bridgeToken, 'probe-fixture-model-secret', 'ambient-private-key-marker']) {
    assert.ok(!prior.logs().includes(secret)); assert.ok(!s.logs().includes(secret))
  }
})

test('TEST-RUNTIME-PROCESS-02 meter rejection blocks dispatch; SIGTERM settles an in-flight Run', { timeout: 45000 }, async t => {
  const s = await setup(t, false, true); await s.probe()
  const session = await createProductSession(s, 'entry-meter')
  const rejected = await sendMessage(s, session.session_id, { ...messageInput, content: [{ type: 'text', text: 'reject-meter-fixture' }] })
  assert.equal((await terminalRun(s, rejected.run_id)).state, 'failed')
  assert.equal(s.requests.length, 2)
  assert.ok(!s.logs().includes('private-meter-error-marker'))
  const next = await createProductSession(s, 'entry-shutdown'); s.mode = 'hold'
  const sending = sendMessage(s, next.session_id)
  await s.arrival.promise
  const sent = await sending
  const prior = s.runtime
  await s.restartRuntime()
  assert.ok(prior.logs().includes('agent_runtime_stopped'))
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'interrupted')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.equal(events.at(-1).type, 'run.interrupted')
  assert.equal(s.requests.length, 3)
})

test('TEST-RUNTIME-PROCESS-03 shutdown aborts a silent probe without waiting for its model deadline', { timeout: 45000 }, async t => {
  const s = await setup(t, false, true); s.mode = 'hold'
  const probing = s.probe(false, 503)
  await s.arrival.promise
  const started = performance.now()
  await s.runtime.close()
  assert.ok(performance.now() - started < 3000, 'shutdown waited for the 15-second model deadline')
  await probing
  assert.equal(s.requests.length, 1)
  assert.ok(s.runtime.logs().includes('agent_runtime_stopped'))
  await s.restartRuntime()
  assert.equal((await s.probe(true)).status, 'ready')
  assert.equal(s.requests.length, 3)
})

test('TEST-RUN-RECOVERY-01 background confirmation repairs lost reply without another submission', { timeout: 30000 }, async t => {
  const s = await setup(t); await s.probe()
  const session = await createProductSession(s, 'background-ack')
  await s.control({ lose_run_reply: true })
  const sent = await sendMessage(s, session.session_id, messageInput, 503)
  assert.ok(sent.run_id)
  const result = await s.control({ reconcile_runs: true })
  assert.equal(result.failed, false); assert.equal(result.acknowledged, 1)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  assert.equal(s.requests.length, 3)
})

test('TEST-RUN-RECOVERY-02 background cold settlement survives revoke without executing model or unknown Tool', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe(); await s.control({ hold_tool: true })
  const session = await researchSession(s, 'background-cold'), sent = await sendMessage(s, session.session_id)
  const until = performance.now() + 4000
  while (!(await s.control({ research_status: true })).queries) {
    assert.ok(performance.now() < until); await new Promise(resolve => setTimeout(resolve, 25))
  }
  await s.runtime.kill()
  await s.api('settings/llm-providers/fixture', { api_key_update: { action: 'revoke' } }, 'PATCH')
  await s.restartRuntime()
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'recovering')
  assert.equal((await s.control({ reconcile_runs: true })).failed, false)
  assert.equal((await s.api('agent/runs/' + sent.run_id)).state, 'interrupted')
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.equal(events.find(event => event.type === 'tool.failed').data.code, 'agent_tool_outcome_unknown')
  assert.equal((await s.api('agent/runs/' + sent.run_id + '/reconcile', {})).state, 'interrupted')
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
  assert.equal((await s.control({ research_status: true })).queries, 1)
  assert.equal(s.requests.length, 3)
})

test('TEST-BUDGET-TOOLS-01 provider limit larger than Run budget still generates and executes tools', { timeout: 30000 }, async t => {
  const s = await setup(t, true)
  await s.api('settings/llm-providers/fixture', { agent: { context_window: 262144, max_output_tokens: 204800 } }, 'PATCH')
  await s.runtime.call('refresh'); await s.probe(true)
  const session = await researchSession(s, 'large-model-cap')
  const sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  const business = s.requests.filter(r => r.body.tools?.some(tool => tool.function.name === 'mcp__q4d__query_kline'))
  assert.ok(business.length >= 1)
  assert.equal(business[0].body.max_tokens ?? business[0].body.max_completion_tokens, 1536)
  assert.equal((await s.control({ research_status: true })).queries, 1)
})
test('TEST-BUDGET-TOOLS-02 output recovery preserves executed results and never dispatches truncated arguments', { timeout: 30000 }, async t => {
  const s = await setup(t, true); await s.probe(); s.mode = 'recover-output'
  const session = await researchSession(s, 'recover-output')
  const sent = await sendMessage(s, session.session_id)
  assert.equal((await terminalRun(s, sent.run_id)).state, 'completed')
  assert.equal(s.requests.length, 5)
  const ledger = await s.control({ research_status: true })
  assert.equal(ledger.queries, 1); assert.equal(ledger.audits.length, 1)
  const events = eventFrames(await (await browserEvents(s, sent.run_id)).text())
  assert.equal(events.filter(e => e.type === 'tool.proposed').length, 1)
  assert.equal(events.filter(e => e.type === 'run.progress' && e.data.stage === 'output_recovery').length, 1)
  assert.equal(events.filter(e => e.type === 'usage.updated').reduce((sum, e) => sum + e.data.usage.output_tokens, 0), 552)
  const text = JSON.stringify(s.requests.at(-1).body.messages)
  assert.ok(text.includes('tool')); assert.ok(!text.includes('truncated-call'))
  await s.restartRuntime()
  assert.deepEqual(eventFrames(await (await browserEvents(s, sent.run_id)).text()), events)
})
