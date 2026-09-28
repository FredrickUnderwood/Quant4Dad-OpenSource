import assert from 'node:assert/strict'
import { createServer, request as httpRequest } from 'node:http'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { createBridgeHandler } from '../../../../src/bridge/http-handler.mjs'
import { projectRun } from '../../../../src/bridge/run-projection.mjs'
import { sessionRequest } from '../../../../fixtures/session-config.mjs'
import { sessionCreated } from '../../../../src/persistence/session-bindings.mjs'

const token = 'synthetic-bridge-secret'
const input = JSON.parse(readFileSync(new URL('./fixtures/prompt.valid.json', import.meta.url)))
const promptPath = `/q4d/v1/sessions/${input.q4d_session_id}/prompts`
const accepted = { run_id: input.run_id, message_id: 'message-1', durable: true, state: 'accepted' }
const headers = { authorization: `Bearer ${token}`, 'content-type': 'application/json' }

async function setup(t, backend = {}, options = {}) {
  const server = createServer(createBridgeHandler({ token, backend, ...options }))
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const url = `http://127.0.0.1:${server.address().port}`
  return {
    url, server,
    fetch: (path, options = {}) => fetch(url + path, { headers, ...options }),
    post: (body = input) => fetch(url + promptPath, { method: 'POST', headers, body: JSON.stringify(body) }),
  }
}
async function error(response, status, code) {
  assert.equal(response.status, status)
  assert.equal(response.headers.get('cache-control'), 'no-store')
  assert.deepEqual(await response.json(), { error: { code } })
}

test('TEST-HTTP-01 Bridge auth fails closed before routing or parsing and never echoes diagnostics', async t => {
  for (const value of ['', undefined, 'contains space', 'bad\nheader']) assert.throws(() => createBridgeHandler({ token: value, backend: {} }), /agent_bridge_token_required/)
  let calls = 0
  const host = await setup(t, { health() { calls++; throw new Error(`upstream ${token}`) } })
  for (const value of [undefined, 'Bearer wrong', `Basic ${token}`]) {
    const response = await host.fetch('/q4d/v1/health', { headers: value ? { authorization: value } : {} })
    await error(response, 401, 'agent_unauthorized')
  }
  assert.equal(calls, 0)
  await error(await host.fetch('/q4d/v1/health'), 500, 'agent_internal_error')
  assert.equal(calls, 1)
})

test('TEST-HTTP-02 prompt validation rejects forged fields, unsafe IDs and missing credentials before admission', async t => {
  let calls = 0
  const host = await setup(t, { admit() { calls++; return accepted } })
  const variants = [
    null, [], { ...input, run_capability: undefined }, { ...input, run_capability: 'bad\nvalue' },
    { ...input, run_capability: 'x'.repeat(8193) }, { ...input, run_id: '../events' },
    { ...input, q4d_session_id: 'other-session' }, { ...input, profile: 'ops' },
    { ...input, fault: 'after_intent' }, { ...input, run: { allowedTools: ['shell'] } },
    { ...input, content: [{ type: 'text', text: 'x'.repeat(32001) }] },
    { ...input, execution_envelope_digest: 'invalid' },
  ]
  for (const value of variants) await error(await host.post(value), 400, 'agent_invalid_request')
  assert.equal(calls, 0)
  const response = await host.post()
  assert.equal(response.status, 202)
  assert.deepEqual(await response.json(), accepted)
  assert.equal(calls, 1)
})

test('TEST-HTTP-03 bounded JSON rejects encoding, malformed UTF-8 and both fixed/chunked oversized bodies', async t => {
  let calls = 0
  const host = await setup(t, { admit() { calls++; return accepted } }, { maxBodyBytes: 1024 })
  for (const extra of [{ 'content-type': 'text/plain' }, { 'content-encoding': 'gzip' }]) {
    await error(await host.fetch(promptPath, { method: 'POST', headers: { ...headers, ...extra }, body: '{}' }), 415, 'agent_unsupported_media_type')
  }
  for (const body of ['{', Buffer.from([0xff])]) {
    await error(await host.fetch(promptPath, { method: 'POST', body }), 400, 'agent_invalid_json')
  }
  await error(await host.fetch(promptPath, { method: 'POST', body: 'x'.repeat(1025) }), 413, 'agent_request_too_large')
  const streamed = new ReadableStream({ start(controller) { controller.enqueue(Buffer.alloc(1025, 32)); controller.close() } })
  await error(await host.fetch(promptPath, { method: 'POST', body: streamed, duplex: 'half' }), 413, 'agent_request_too_large')
  assert.equal(calls, 0)
})

test('TEST-HTTP-04 slow and aborted bodies never reach the backend', async t => {
  let calls = 0
  const host = await setup(t, { admit() { calls++; return accepted } }, { bodyTimeoutMs: 40 })
  await new Promise((resolve, reject) => {
    const req = httpRequest(host.url + promptPath, { method: 'POST', headers }, res => {
      const chunks = []
      res.on('data', chunk => chunks.push(chunk))
      res.on('end', () => {
        try {
          assert.equal(res.statusCode, 408)
          assert.deepEqual(JSON.parse(Buffer.concat(chunks)), { error: { code: 'agent_request_timeout' } })
          resolve()
        } catch (error) { reject(error) }
      })
    })
    req.on('error', reject)
    req.write('{') // No EOF: total body deadline, not an inactivity reset.
  })
  await new Promise(resolve => {
    const req = httpRequest(host.url + promptPath, { method: 'POST', headers })
    req.on('error', () => {})
    host.server.once('request', incoming => {
      incoming.once('aborted', resolve)
      incoming.once('data', () => req.destroy()) // Abort only after the server has received a partial body.
    })
    req.write('{')
  })
  assert.equal(calls, 0)
})

test('TEST-HTTP-05 routes reject wrong methods, query credentials, extra controls and unsupported operations', async t => {
  const host = await setup(t)
  const wrongMethod = await host.fetch(promptPath)
  assert.equal(wrongMethod.headers.get('allow'), 'POST')
  await error(wrongMethod, 405, 'agent_method_not_allowed')
  await error(await host.fetch('/q4d/v1/runs/run-1?run_capability=secret'), 400, 'agent_invalid_query')
  await error(await host.fetch('/q4d/v1/sessions'), 405, 'agent_method_not_allowed')
  await error(await host.fetch('/q4d/v1/runs/run-1/cancel', { method: 'POST', body: '{"session_id":"other"}' }), 400, 'agent_invalid_request')
  await error(await host.post(), 503, 'agent_runtime_unavailable')
})

test('TEST-HTTP-06 timeout retains the admission slot until completion and never implicitly cancels work', async t => {
  const deferred = Promise.withResolvers()
  let calls = 0
  const host = await setup(t, { admit() { calls++; return calls === 1 ? deferred.promise : accepted } },
    { operationTimeoutMs: 40, maxPendingOperations: 1 })
  await error(await host.post(), 504, 'agent_operation_timeout')
  await error(await host.post(), 503, 'agent_bridge_busy')
  assert.equal(calls, 1)
  deferred.resolve(accepted)
  assert.equal((await host.post()).status, 202)
  assert.equal(calls, 2)
})

test('TEST-HTTP-07 backend response contracts prevent false durable acknowledgement or credential leakage', async t => {
  let result = { ...accepted, durable: false }
  const host = await setup(t, { admit: () => result })
  await error(await host.post(), 500, 'agent_backend_contract_error')
  result = { ...accepted, run_capability: token }
  await error(await host.post(), 500, 'agent_backend_contract_error')
  result = { ...accepted, run_id: 'other-run' }
  await error(await host.post(), 500, 'agent_backend_contract_error')
})

test('TEST-HTTP-08 approval requires a bound one-shot decision and never reflects the receipt', async t => {
  const calls = []
  const host = await setup(t, { decide: (...args) => calls.push(args) })
  const body = { run_id: 'run-1', tool_call_id: '01K4M000000000000000000000', decision: 'allow_once' }
  const path = '/q4d/v1/approvals/approval-1/decision'
  const post = value => host.fetch(path, { method: 'POST', body: JSON.stringify(value) })
  await error(await post(body), 400, 'agent_invalid_request')
  await error(await post({ ...body, decision: 'reject', approval_receipt: 'secret' }), 400, 'agent_invalid_request')
  await error(await post({ ...body, decision: 'allow_always' }), 400, 'agent_invalid_request')
  assert.equal(calls.length, 0)
  const response = await post({ ...body, approval_receipt: 'secret' })
  assert.equal(response.status, 200)
  assert.deepEqual(await response.json(), body)
  assert.equal(calls[0][0], 'approval-1')
  assert.equal(calls[0][1].approval_receipt, 'secret')
})

test('TEST-HTTP-09 Run projection distinguishes live approval, cancellation, recovery and committed terminal', () => {
  const binding = { session_id: 's', run_id: 'r', message_id: 'm', execution_envelope_digest: input.execution_envelope_digest,
    request_hash: input.request_hash }
  const events = [{ id: '1', type: 'run.started', data: {} }, { id: '2', type: 'approval.required', data: { tool_call_id: 'tool' } }]
  const journal = { snapshot: () => events }
  assert.equal(projectRun(binding, journal).state, 'recovering')
  assert.equal(projectRun(binding, journal, { active: true }).state, 'waiting_approval')
  assert.equal(projectRun(binding, journal, { active: true, cancelling: true }).state, 'cancelling')
  events.push({ id: '3', type: 'tool.failed', data: { tool_call_id: 'tool' } })
  assert.equal(projectRun(binding, journal, { active: true }).state, 'running')
  events.push({ id: '4', type: 'run.completed', data: {} })
  journal.terminal = 'run.completed'
  const final = projectRun(binding, journal, { active: true, cancelling: true })
  assert.equal(final.state, 'completed')
  assert.equal(final.terminal, true)
  assert.equal(final.last_event_id, '4')
  assert.equal(final.durable, true)
  assert.equal(Object.hasOwn(final, 'request_hash'), false)
})

test('TEST-HTTP-10 Session create/close validate identities, reject injected controls and enforce backend response schemas', async t => {
  const request = sessionRequest()
  const binding = { request, runtime_provenance: { bridge_protocol: 1, adapter_version: 't00', backend: 'cordis',
    dsh_version: '0.1.2-alpha.5', session_format: 0, event_journal_format: 1, session_binding_format: 1 } }
  let created = sessionCreated(binding)
  let closed = { session_id: 'session-1', dsh_session_id: 'session-1', durable: true, loaded: false }
  let calls = 0
  const host = await setup(t, { createSession() { calls++; return created }, closeSession: () => closed })
  const post = (path, body) => host.fetch(path, { method: 'POST', body: JSON.stringify(body) })
  for (const field of ['cwd', 'plugins', 'tools', 'api_key', 'run_capability', 'fault', 'dsh_session_id']) {
    await error(await post('/q4d/v1/sessions', { ...request, [field]: 'forged' }), 400, 'agent_invalid_request')
  }
  assert.equal(calls, 0)
  assert.deepEqual(await (await post('/q4d/v1/sessions', request)).json(), created)
  created = { ...created, model: 'different' }
  await error(await post('/q4d/v1/sessions', request), 500, 'agent_backend_contract_error')
  created = { ...sessionCreated(binding), api_key: 'secret' }
  await error(await post('/q4d/v1/sessions', request), 500, 'agent_backend_contract_error')
  await error(await post('/q4d/v1/sessions/session-1/close', { cancel: true }), 400, 'agent_invalid_request')
  assert.deepEqual(await (await post('/q4d/v1/sessions/session-1/close', {})).json(), closed)
  closed = { ...closed, loaded: true }
  await error(await post('/q4d/v1/sessions/session-1/close', {}), 500, 'agent_backend_contract_error')
})


test('runtime health is authenticated, read-only and separate from business readiness', async t => {
  const host = await setup(t, { runtimeHealth: () => ({ status: 'ready' }), health: () => ({ status: 'unavailable' }) })
  await error(await host.fetch('/q4d/v1/runtime-health', { headers: {} }), 401, 'agent_unauthorized')
  assert.deepEqual(await (await host.fetch('/q4d/v1/runtime-health')).json(), { status: 'ready' })
  assert.deepEqual(await (await host.fetch('/q4d/v1/health')).json(), { status: 'unavailable' })
  await error(await host.fetch('/q4d/v1/runtime-health', { method: 'POST', body: '{}' }), 405, 'agent_method_not_allowed')
  await error(await host.fetch('/q4d/v1/runtime-health?override=true'), 400, 'agent_invalid_query')
})
