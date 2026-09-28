import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { generateKeyPairSync, randomBytes, sign } from 'node:crypto'
import { readFile, readdir } from 'node:fs/promises'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { bootstrapDshHost } from '../helpers/bootstrap-dsh-host.mjs'
import { host, clone, revise, send, controlToken } from '../contracts/bootstrap/helpers.mjs'
import { canonicalAuthorizationJSON as canonical, executionEnvelopeDigest, promptRequestHash, tokenType } from '../../../src/auth/run-capability.mjs'
import { sessionProvisionHash } from '../../../src/persistence/session-bindings.mjs'
import { profileIdentity } from '../../../src/runtime/profile-agreement.mjs'

const bridgeToken = 'fixture-session-bridge-token-abcdef-0123456789'
const digest = 'sha256:' + 'a'.repeat(64)
const profile = { id: 'text_only', revision: digest, systemPrompt: 'Answer the user.', promptBundleDigest: digest,
  skillsDigest: digest, toolCatalogRevision: digest }
const manifest = { q4d_version: '0.0.0', agent_image_digest: digest, agent_runtime_version: 'fixture-v1',
  adapter_version: 'fixture-v1', dsh_version: '0.1.2-alpha.5', bridge_protocol: 1 }
async function setup(t, requireProfileAgreement = false, selectedProfile = profile) {
  const profile = selectedProfile
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  let snapshot = clone(), mode = 'text', failures = 0
  const calls = [], arrivals = [], closed = []
  const model = createServer((req, res) => {
    let body = ''
    req.on('data', bytes => { body += bytes })
    req.on('end', () => {
      calls.push({ body: JSON.parse(body), authorization: req.headers.authorization })
      arrivals.splice(0).forEach(resolve => resolve())
      res.on('close', () => closed.push(calls.length))
      if (mode === 'hold' || mode === 'length-then-hold' && failures > 0) return
      const request = calls.at(-1).body
      const reviewing = request.messages.some(m => typeof m.content === 'string' && m.content.startsWith('You are the Q4D strategy answer evidence reviewer'))
      if (reviewing && mode === 'review-hold') return
      const summarizing = request.messages.some(m => typeof m.content === 'string' && m.content.startsWith('You are now acting as a compaction engine'))
      if (mode.startsWith('overflow') && !summarizing && (mode === 'overflow-always' || failures++ === 0)) {
        res.writeHead(400, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ error: { code: 'context_length_exceeded', message: 'maximum context length exceeded private-provider-marker' } }))
        return
      }
      const limited = mode === 'length' || ['length-once', 'length-then-hold'].includes(mode) && failures++ === 0 || mode === 'overflow-summary-length' && summarizing
      res.writeHead(200, { 'content-type': 'text/event-stream' })
      const delta = mode === 'tool' ? { role: 'assistant', tool_calls: [{ index: 0, id: 'forged-call', type: 'function',
        function: { name: 'shell', arguments: '{"command":"whoami"}' } }] } : mode === 'reasoning-only' ? { role: 'assistant', reasoning_content: 'synthetic private reasoning' } : { role: 'assistant', content: reviewing ? (mode === 'review-pass' ? '{"verdict":"supported","issues":[]}' : '{"verdict":"unsupported","issues":["no evidence"]}') : 'hello' }
      for (const value of [
        { choices: [{ delta, index: 0, finish_reason: null }] },
        { choices: [{ delta: {}, index: 0, finish_reason: mode === 'tool' ? 'tool_calls' : limited ? 'length' : 'stop' }],
          usage: { prompt_tokens: 3, completion_tokens: limited && mode !== 'length' ? (request.max_tokens ?? request.max_completion_tokens) : 1 } },
      ]) res.write('data: ' + JSON.stringify(value) + '\n\n')
      res.end('data: [DONE]\n\n')
    })
  })
  t.after(() => { model.closeAllConnections(); return new Promise(resolve => model.close(resolve)) })
  await new Promise(resolve => model.listen(0, '127.0.0.1', resolve))
  snapshot.providers[0].base_url = `http://127.0.0.1:${model.address().port}/v1`
  snapshot.providers[0].api_key = 'fixture-session-provider-secret'
  snapshot.capability.public_keys = { 'key-1': publicKey.export({ format: 'jwk' }).x }
  if (requireProfileAgreement) snapshot.profiles = [profileIdentity(profile)]
  const source = await host(t, (req, res) => send(res, snapshot, req.url.endsWith(snapshot.revision) ? 304 : 200))
  const start = root => bootstrapDshHost(t, { url: source.url, token: controlToken, root,
    fixture: 'session-runtime-host.ts', env: { Q4D_SESSION_BRIDGE_TOKEN: bridgeToken,
      Q4D_SESSION_CONFIG: JSON.stringify({ profile, manifest, requireProfileAgreement }) } })
  let runtime = await start()
  async function http(path, body, status = body === undefined ? 200 : 200, headers = {}) {
    const response = await fetch(`http://127.0.0.1:${runtime.port}/q4d/v1/${path}`, {
      method: body === undefined ? 'GET' : 'POST', headers: { authorization: `Bearer ${bridgeToken}`,
        ...(body === undefined ? {} : { 'content-type': 'application/json' }), ...headers },
      body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(7000),
    })
    const text = await response.text()
    assert.equal(response.status, status, text + '\n' + runtime.logs())
    return response.headers.get('content-type').includes('text/event-stream') ? text : JSON.parse(text)
  }
  async function session(id = 'session-1') {
    const body = { q4d_session_id: id, provider: 'fixture', model: 'fixture-model', profile: profile.id,
      profile_revision: profile.revision, model_config_revision: snapshot.model_config_revision }
    return http('sessions', { ...body, provision_request_hash: sessionProvisionHash(body) })
  }
  async function request(runId = 'run-1', { id = 'session-1', text = 'hi', budgets = {}, envelope = {} } = {}) {
    const now = Math.floor(Date.now() / 1000)
    const e = { ...manifest, actor_id: 'local-user', run_id: runId, client_request_id: 'client-' + runId,
      product_profile: profile.id, profile_revision: digest, prompt_bundle_digest: digest, skills_digest: digest,
      tool_catalog_revision: digest, provider: 'fixture', model: 'fixture-model', model_config_revision: snapshot.model_config_revision,
      budgets: { max_turns: 1, max_tool_calls: 0, max_input_tokens: 1000, max_output_tokens: 128, wall_time_ms: 20000, ...budgets }, ...envelope }
    const claims = { iss: snapshot.capability.issuer, aud: ['q4d-agent-runtime', 'q4d-internal-mcp'], iat: now,
      exp: now + Math.ceil(e.budgets.wall_time_ms / 1000), jti: randomBytes(16).toString('hex'), session_id: id, request_hash: promptRequestHash(id, text),
      envelope: e, execution_envelope_digest: executionEnvelopeDigest(e), allowed_tools: [] }
    const encode = value => Buffer.from(canonical(value)).toString('base64url')
    const input = encode({ alg: 'Ed25519', kid: 'key-1', typ: tokenType }) + '.' + encode(claims)
    const token = input + '.' + sign(null, Buffer.from(input), privateKey).toString('base64url')
    await runtime.call('authorize', claims)
    return { q4d_session_id: id, run_id: runId, client_request_id: e.client_request_id, request_hash: claims.request_hash,
      execution_envelope_digest: claims.execution_envelope_digest, content: [{ type: 'text', text }], run_capability: token }
  }
  async function terminal(runId = 'run-1') {
    for (let n = 0; n < 150; n++) {
      const value = await http('runs/' + runId)
      if (value.terminal) return value
      await delay(20)
    }
    throw new Error('Run did not terminate: ' + runtime.logs())
  }
  return { calls, closed, http, session, request, terminal, get runtime() { return runtime },
    set mode(value) { mode = value; failures = 0 }, get snapshot() { return snapshot }, set snapshot(value) { snapshot = value },
    arrival: () => new Promise(resolve => arrivals.push(resolve)),
    restart: async (kill = false) => { const root = runtime.directory; await (kill ? runtime.kill() : runtime.close()); runtime = await start(root) },
    async events(runId = 'run-1') {
      const text = await http(`runs/${runId}/events`)
      return text.split('\n').filter(line => line.startsWith('data: ')).map(line => JSON.parse(line.slice(6)))
    },
  }
}

test('TEST-SESSION-RUNTIME-01 real Bridge -> durable Session -> AgentLoop -> bootstrap pi-ai with history and idempotent retry', { timeout: 25000 }, async t => {
  const s = await setup(t)
  assert.equal((await s.session()).durable, true)
  const request = await s.request()
  const ack = await s.http('sessions/session-1/prompts', request, 202)
  assert.equal((await s.terminal()).state, 'completed')
  assert.equal(s.calls[0].authorization, 'Bearer fixture-session-provider-secret')
  assert.equal(s.calls[0].body.max_tokens ?? s.calls[0].body.max_completion_tokens, 128)
  assert.equal(s.calls[0].body.tools, undefined)
  assert.deepEqual(await s.http('sessions/session-1/prompts', request, 202), ack)
  const events = await s.events()
  assert.equal(events.filter(e => e.type === 'run.completed').length, 1)
  assert.equal(events.find(e => e.type === 'message.completed').data.text, 'hello')
  await s.http('sessions/session-1/prompts', await s.request('run-2', { text: 'continue' }), 202)
  assert.equal((await s.terminal('run-2')).state, 'completed')
  assert.equal(s.calls.length, 2)
  assert.ok(s.calls[1].body.messages.some(m => m.role === 'assistant' && m.content === 'hello'))
  await s.http('sessions/session-1/close', {})
  await s.restart()
  assert.deepEqual(await s.http('sessions/session-1/prompts', request, 403), { error: { code: 'agent_capability_rejected' } })
  await s.http('sessions/session-1/prompts', await s.request('run-3'), 202)
  assert.equal((await s.terminal('run-3')).state, 'completed')
  assert.equal(s.calls.length, 3)
  assert.equal((await s.http('runs/run-1')).state, 'completed')
})

test('TEST-SESSION-RUNTIME-02 accepted held HTTP can be cancelled and its durable terminal survives restart', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'hold'
  const arrival = s.arrival(), request = await s.request()
  await s.http('sessions/session-1/prompts', request, 202); await arrival
  await s.http('runs/run-1/cancel', {})
  assert.equal((await s.terminal()).state, 'cancelled')
  assert.deepEqual((await s.events()).at(-1).data, { reason: 'user' })
  await s.restart()
  assert.equal((await s.http('runs/run-1')).state, 'cancelled')
  assert.equal(s.calls.length, 1)
})

test('TEST-SESSION-RUNTIME-03 bootstrap replacement interrupts the Run and never reports user cancellation', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'hold'
  const arrival = s.arrival()
  await s.http('sessions/session-1/prompts', await s.request(), 202); await arrival
  s.snapshot = { ...revise(structuredClone(s.snapshot)), providers: [] }
  await s.runtime.call('refresh')
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal((await s.events()).at(-1).data.code, 'agent_run_blocked')
  const stops = await readFile(join(s.runtime.directory, 'state/run-stops.jsonl'), 'utf8')
  assert.ok(stops.includes('agent_configuration_unavailable'))
  assert.equal(s.calls.length, 1)
})

test('TEST-SESSION-RUNTIME-04 quiet model requests end on live revocation or capability deadline', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'hold'
  let arrival = s.arrival()
  await s.http('sessions/session-1/prompts', await s.request(), 202); await arrival
  await s.runtime.call('revoke', { runId: 'run-1' })
  assert.equal((await s.terminal()).state, 'failed')
  arrival = s.arrival()
  await s.http('sessions/session-1/prompts', await s.request('run-2', { budgets: { wall_time_ms: 2500 } }), 202); await arrival
  assert.equal((await s.terminal('run-2')).state, 'failed')
  assert.equal((await s.events('run-2')).at(-1).data.code, 'agent_run_blocked')
})

test('TEST-SESSION-RUNTIME-05 budget refusal and model readiness prevent dispatch; envelope mismatch rejects admission', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session()
  await s.http('sessions/session-1/prompts', await s.request('wrong', { envelope: { prompt_bundle_digest: 'sha256:' + 'b'.repeat(64) } }), 403)
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: { max_input_tokens: 99 } }), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal((await s.events()).at(-1).data.code, 'agent_model_limit')
  await s.runtime.call('unready')
  await s.http('sessions/session-1/prompts', await s.request('run-2'), 503)
  assert.equal(s.calls.length, 0)
})

test('TEST-SESSION-RUNTIME-06 unexpected Tool calls fail without another model step or Tool execution', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'tool'
  await s.http('sessions/session-1/prompts', await s.request(), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal(s.calls.length, 1)
  assert.equal((await s.events()).some(e => e.type.startsWith('tool.')), false)
})

test('TEST-SESSION-RUNTIME-07 cold unfinished Run recovers interrupted without reissuing its model request', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'hold'
  const arrival = s.arrival()
  await s.http('sessions/session-1/prompts', await s.request(), 202); await arrival
  await s.restart(true)
  assert.equal((await s.http('runs/run-1')).state, 'recovering')
  // A new authorized request resumes the Session; old inbox work has no lease.
  s.mode = 'text'
  await s.http('sessions/session-1/prompts', await s.request('run-2'), 202)
  assert.equal((await s.terminal('run-2')).state, 'completed')
  assert.equal((await s.http('runs/run-1')).state, 'interrupted')
  assert.equal(s.calls.length, 2)
})

test('TEST-SESSION-RUNTIME-08 independent sessions cannot borrow authority; shutdown flushes terminal and removes credentials', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); await s.session('session-2'); s.mode = 'hold'
  const arrival = s.arrival()
  const first = await s.request()
  await s.http('sessions/session-1/prompts', first, 202); await arrival
  await s.http('sessions/session-2/prompts', { ...first, q4d_session_id: 'session-2' }, 403)
  await s.http('sessions/session-1/prompts', await s.request('run-2'), 409)
  await s.runtime.close()
  assert.deepEqual(await readdir(join(s.runtime.directory, 'snapshots')), [])
  const events = await readFile(join(s.runtime.directory, 'state/events-run-1.jsonl'), 'utf8')
  assert.ok(events.includes('run.interrupted'))
  for (const secret of [controlToken, bridgeToken, first.run_capability, s.snapshot.providers[0].api_key, s.snapshot.mcp.runtime_token]) {
    assert.ok(!events.includes(secret)); assert.ok(!s.runtime.logs().includes(secret))
  }
})


test('TEST-SESSION-RUNTIME-OUTPUT-LIMIT per-call truncation records a precise durable budget reason', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'length'
  await s.http('sessions/session-1/prompts', await s.request(), 202)
  assert.equal((await s.terminal()).state, 'failed')
  const failure = (await s.events()).find(e => e.type === 'run.failed')
  assert.equal(failure.data.code, 'agent_model_limit')
  assert.deepEqual(failure.data.budget, { dimension: 'output_per_call', used: 1, limit: 128, requested: 128 })
  await s.restart()
  assert.deepEqual((await s.events()).find(e => e.type === 'run.failed').data, failure.data)
})

const recoveryBudgets = { max_turns: 6, max_input_tokens: 64000, max_output_tokens: 4096 }
test('TEST-OUTPUT-RECOVERY-01 truncated generation retries inside the same native Turn and accounts for both requests', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'length-once'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: recoveryBudgets }), 202)
  const outcome = await s.terminal()
  if (outcome.state !== 'completed') {
    t.diagnostic(s.runtime.logs())
    const paths = await readdir(join(s.runtime.directory, 'state/sessions'), { recursive: true })
    for (const path of paths.filter(p => p.endsWith('.jsonl'))) {
      const raw = await readFile(join(s.runtime.directory, 'state/sessions', path), 'utf8')
      t.diagnostic(raw.split('\n').filter(line => line.includes('error') || line.includes('turn/end')).join('\n'))
    }
  }
  assert.equal(outcome.state, 'completed')
  assert.equal(s.calls.length, 2)
  assert.equal(s.calls[0].body.max_tokens ?? s.calls[0].body.max_completion_tokens, 512)
  assert.ok(s.calls[1].body.messages.some(m => m.role === 'system' && m.content.includes('上一次生成')))
  const events = await s.events()
  assert.equal(events.filter(e => e.type === 'run.progress' && e.data.stage === 'output_recovery').length, 1)
  assert.equal(events.filter(e => e.type === 'message.completed').length, 1)
  assert.equal(events.filter(e => e.type === 'usage.updated').reduce((sum, e) => sum + e.data.usage.output_tokens, 0), 513)
  await s.restart()
  assert.deepEqual(await s.events(), events)
})
test('TEST-OUTPUT-RECOVERY-02 repeated truncations stop after two recoveries with the exact final limit', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'length'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal(s.calls.length, 3)
  const events = await s.events()
  assert.equal(events.filter(e => e.type === 'run.progress' && e.data.stage === 'output_recovery').length, 2)
  assert.equal(events.at(-1).data.budget.dimension, 'output_per_call')
  assert.equal(events.at(-1).data.budget.limit, 512)
})
test('TEST-CONTEXT-RECOVERY-01 provider overflow compacts below the pressure threshold and retries with reduced history', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session()
  const text = 'historical data '.repeat(180)
  await s.http('sessions/session-1/prompts', await s.request('seed', { text, budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal('seed')).state, 'completed')
  s.mode = 'overflow-once'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { text: 'continue analysis', budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal()).state, 'completed')
  assert.equal(s.calls.length, 4)
  const events = await s.events()
  assert.ok(events.some(e => e.type === 'context.compacted'))
  assert.ok(events.some(e => e.type === 'run.progress' && e.data.stage === 'context_recovery'))
  assert.ok(!JSON.stringify(events).includes('private-provider-marker'))
  assert.ok(JSON.stringify(s.calls[3].body).length < JSON.stringify(s.calls[1].body).length)
  await s.restart()
  assert.deepEqual(await s.events(), events)
})
test('TEST-CONTEXT-RECOVERY-02 unchanged or repeatedly rejected history stops without looping', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session()
  await s.http('sessions/session-1/prompts', await s.request('seed', { text: 'historical data '.repeat(180), budgets: recoveryBudgets }), 202)
  await s.terminal('seed'); s.mode = 'overflow-always'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal(s.calls.length, 4)
  assert.equal((await s.events()).at(-1).data.budget.dimension, 'context_window')
})

test('TEST-CONTEXT-RECOVERY-03 a truncated summary preserves history, accounts for tokens and stops without retrying the original request', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session()
  const text = 'historical data '.repeat(180)
  await s.http('sessions/session-1/prompts', await s.request('seed', { text, budgets: recoveryBudgets }), 202)
  await s.terminal('seed'); s.mode = 'overflow-summary-length'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal(s.calls.length, 3)
  const events = await s.events()
  assert.ok(events.some(e => e.type === 'run.progress' && e.data.stage === 'compaction_failed'))
  assert.ok(!events.some(e => e.type === 'context.compacted'))
  assert.equal(events.filter(e => e.type === 'usage.updated').reduce((sum, e) => sum + e.data.usage.output_tokens, 0), 512)
  s.mode = 'text'
  await s.http('sessions/session-1/prompts', await s.request('after', { budgets: recoveryBudgets }), 202)
  assert.equal((await s.terminal('after')).state, 'completed')
  assert.ok(JSON.stringify(s.calls.at(-1).body.messages).includes(text))
})

test('TEST-OUTPUT-RECOVERY-03 cancellation aborts a silent recovery request and prevents further attempts', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'length-then-hold'
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: recoveryBudgets }), 202)
  for (let i = 0; i < 100 && s.calls.length < 2; i++) await delay(20)
  assert.equal(s.calls.length, 2)
  await s.http('runs/run-1/cancel', {})
  assert.equal((await s.terminal()).state, 'cancelled')
  assert.equal(s.calls.length, 2)
  assert.ok((await s.events()).some(e => e.type === 'run.progress' && e.data.stage === 'output_recovery'))
})

test('TEST-EMPTY-ANSWER reasoning-only stop cannot be projected as a completed user answer', { timeout: 25000 }, async t => {
  const s = await setup(t); await s.session(); s.mode = 'reasoning-only'
  await s.http('sessions/session-1/prompts', await s.request(), 202)
  assert.equal((await s.terminal()).state, 'failed')
  assert.equal(s.calls.length, 1)
  const events = await s.events()
  assert.equal(events.at(-1).data.code, 'agent_model_error')
  assert.ok(!events.some(e => e.type === 'message.completed'))
  assert.equal(events.filter(e => e.type === 'usage.updated').reduce((n, e) => n + e.data.usage.output_tokens, 0), 1)
  assert.ok(!JSON.stringify(events).includes('synthetic private reasoning'))
})

test('TEST-PROFILE-AGREEMENT rolling API changes block new work but retain drain and cold reads', { timeout: 25000 }, async t => {
  const s = await setup(t, true)
  await s.session()
  assert.equal((await s.http('capabilities')).profiles_aligned, true)
  s.snapshot = revise({ ...structuredClone(s.snapshot), profiles: [{ ...profileIdentity(profile), promptBundleDigest: 'sha256:' + 'b'.repeat(64) }] })
  await s.runtime.call('refresh')
  assert.equal((await s.http('capabilities')).profiles_aligned, false)
  assert.equal((await s.http('health')).status, 'unavailable')
  assert.equal((await s.http('runtime-health')).status, 'ready')
  const request = await s.request()
  await s.http('sessions/session-1/prompts', request, 503)
  assert.equal(s.calls.length, 0)
  assert.equal((await s.http('maintenance/drain', {})).active_runs, 0)
  assert.equal((await s.http('runtime-health')).status, 'ready')
  assert.equal((await s.http('sessions/session-1?limit=1')).session_id, 'session-1')
  await s.http('maintenance/resume', {})
  assert.match(s.runtime.logs(), /agent_runtime_readiness_changed/)
  assert.match(s.runtime.logs(), /profile_mismatch/)
  await s.restart()
  assert.equal((await s.http('capabilities')).profiles_aligned, false)
  assert.equal((await s.http('maintenance/drain', {})).active_runs, 0)
  assert.equal((await s.http('runtime-health')).status, 'ready')
  s.snapshot = revise({ ...structuredClone(s.snapshot), profiles: [profileIdentity(profile)] }, '4')
  await s.runtime.call('refresh')
  await s.http('maintenance/resume', {})
  assert.equal((await s.http('health')).status, 'ready')
  assert.equal((await s.http('runtime-health')).status, 'ready')
  s.snapshot = { ...structuredClone(s.snapshot), revision: 'invalid' }
  await assert.rejects(s.runtime.call('refresh'))
  assert.equal((await s.http('health')).status, 'unavailable')
  assert.equal((await s.http('runtime-health')).status, 'unavailable')
})

for (const mode of ['review-pass', 'review-fail', 'review-no-budget']) {
  test(`TEST-STRATEGY-ANSWER ${mode} is metered and only reviewed text survives refresh`, { timeout: 25000 }, async t => {
    const s = await setup(t, false, { ...profile, id: 'strategy_lab' })
    s.mode = mode
    await s.session()
    await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: {
      max_turns: mode === 'review-no-budget' ? 1 : 3, max_input_tokens: 10000, max_output_tokens: 4096,
    } }), 202)
    assert.equal((await s.terminal()).state, 'completed', s.runtime.logs())
    assert.equal(s.calls.length, mode === 'review-no-budget' ? 1 : 2)
    if (s.calls.length === 2) assert.equal(s.calls[1].body.tools, undefined)
    const events = await s.events()
    const answer = events.findLast(e => e.type === 'message.completed').data.text
    assert.equal(answer === 'hello', mode === 'review-pass')
    assert.equal(events.filter(e => e.type === 'usage.updated').length, s.calls.length)
    await s.restart()
    const replay = await s.events()
    assert.equal(replay.findLast(e => e.type === 'message.completed').data.text, answer)
    if (mode !== 'review-pass') {
      for (const file of await readdir(s.runtime.directory, { recursive: true })) {
        if (file.endsWith('.jsonl')) assert.ok(!(await readFile(join(s.runtime.directory, file), 'utf8')).includes('hello'), file)
      }
    }
  })
}

test('TEST-STRATEGY-ANSWER cancellation during review reveals no unverified text and retries nothing', { timeout: 25000 }, async t => {
  const s = await setup(t, false, { ...profile, id: 'strategy_lab' })
  s.mode = 'review-hold'
  await s.session()
  await s.http('sessions/session-1/prompts', await s.request('run-1', { budgets: { max_turns: 3, max_input_tokens: 10000, max_output_tokens: 4096 } }), 202)
  for (let n = 0; s.calls.length < 2 && n < 100; n++) await delay(20)
  assert.equal(s.calls.length, 2)
  await s.http('runs/run-1/cancel', {})
  assert.equal((await s.terminal()).state, 'cancelled')
  const events = await s.events()
  assert.ok(!events.some(e => e.type === 'message.completed' && e.data.text.includes('hello')))
  assert.equal(events.filter(e => e.type === 'usage.updated').length, 1, 'cancelled review retains already consumed candidate tokens')
  assert.equal(s.calls.length, 2)
})
