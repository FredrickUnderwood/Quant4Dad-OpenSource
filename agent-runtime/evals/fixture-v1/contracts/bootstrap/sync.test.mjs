import test from 'node:test'
import assert from 'node:assert/strict'
import { generateKeyPairSync, sign } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { inspect } from 'node:util'
import { BootstrapSync } from '../../../../src/bootstrap/sync.mjs'
import { canonicalAuthorizationJSON as canonical, executionEnvelopeDigest, promptRequestHash,
  runtimeAudience, gatewayAudience, tokenType } from '../../../../src/auth/run-capability.mjs'
import { TrustedGateway } from '../../../../src/mcp/trusted-gateway.mjs'
import { queryTool } from '../../helpers/mcp-gateway.mjs'
import { host, send, fixture, clone, revise, controlToken } from './helpers.mjs'

function signed(snapshot, envelopeOverrides = {}) {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  snapshot.capability.public_keys = { 'key-1': publicKey.export({ format: 'jwk' }).x }
  const envelope = JSON.parse(readFileSync(new URL('../authorization/fixtures/valid.json', import.meta.url))).envelope
  Object.assign(envelope, { provider: snapshot.providers[0].id, model: snapshot.providers[0].default_model,
    model_config_revision: snapshot.model_config_revision, ...envelopeOverrides })
  const iat = Math.floor(Date.now() / 1000)
  const claims = { iss: snapshot.capability.issuer, aud: [runtimeAudience, gatewayAudience], iat, exp: iat + 300,
    jti: 'a'.repeat(32), session_id: 'session-1', envelope, allowed_tools: ['query_kline'],
    request_hash: promptRequestHash('session-1', 'query'), execution_envelope_digest: executionEnvelopeDigest(envelope) }
  const encode = value => Buffer.from(canonical(value)).toString('base64url')
  const signingInput = encode({ alg: 'Ed25519', kid: 'key-1', typ: tokenType }) + '.' + encode(claims)
  const capability = signingInput + '.' + sign(null, Buffer.from(signingInput), privateKey).toString('base64url')
  return { q4d_session_id: claims.session_id, run_id: envelope.run_id, client_request_id: envelope.client_request_id,
    request_hash: claims.request_hash, execution_envelope_digest: claims.execution_envelope_digest,
    content: [{ type: 'text', text: 'query' }], run_capability: capability }
}
function sync(t, client, options = {}) {
  const state = new BootstrapSync({ client, authorize: () => true, ...options })
  t.after(() => state.stop())
  return state
}
const unavailable = fn => assert.throws(fn, { message: 'agent_configuration_unavailable' })

test('TEST-BOOTSTRAP-SYNC-01 one refresh owns publication and concurrent readers never see a mixed snapshot', async t => {
  let complete
  const arrived = Promise.withResolvers()
  const server = await host(t, (_req, res) => { complete = () => send(res); arrived.resolve() })
  const state = sync(t, server.client)
  assert.deepEqual(state.status(), { phase: 'uninitialized', configuration_current: false })
  unavailable(() => state.readConfiguration())
  const first = state.refresh()
  const waiters = Array.from({ length: 20 }, () => state.refresh())
  assert.ok(waiters.every(p => p === first))
  await arrived.promise
  unavailable(() => state.readConfiguration())
  complete()
  await Promise.all([first, ...waiters])
  assert.equal(server.requests.length, 1)
  const old = state.readConfiguration()
  const updated = revise(); updated.providers[0].api_key = 'replacement-key'; updated.mcp.runtime_token += '-rotated'
  updated.capability.public_keys = { 'key-2': fixture.capability.public_keys['key-1'] }
  const secondArrived = Promise.withResolvers()
  server.setHandler((_req, res) => { complete = () => send(res, updated); secondArrived.resolve() })
  const second = state.refresh()
  await secondArrived.promise
  for (let i = 0; i < 20; i++) assert.equal(state.readConfiguration(), old)
  complete(); await second
  assert.deepEqual(state.readConfiguration(), updated)
  assert.equal(old.providers[0].api_key, fixture.providers[0].api_key)
  for (const text of [JSON.stringify(state), inspect(state, { showHidden: true })]) {
    for (const secret of [controlToken, updated.providers[0].api_key, updated.mcp.runtime_token, 'public_keys']) assert.ok(!text.includes(secret))
  }
})

test('TEST-BOOTSTRAP-SYNC-02 304 confirms only an owned snapshot; failure clears authority and retries with no known version', async t => {
  const server = await host(t)
  const state = sync(t, server.client)
  await state.refresh()
  const previous = state.readConfiguration()
  server.setHandler((_req, res) => send(res, fixture, 304))
  await state.refresh()
  assert.equal(state.readConfiguration(), previous)
  server.setHandler((_req, res) => { res.writeHead(500); res.end('secret database error') })
  await assert.rejects(state.refresh(), { message: 'agent_bootstrap_unavailable' })
  unavailable(() => state.readConfiguration())
  assert.deepEqual(state.status(), { phase: 'unavailable', configuration_current: false, error: 'agent_bootstrap_unavailable' })
  server.setHandler((_req, res) => send(res, fixture, 304))
  await assert.rejects(state.refresh(), { message: 'agent_bootstrap_invalid' })
  assert.equal(server.requests.at(-1).url, '/internal/v1/agent/bootstrap')
  server.setHandler((_req, res) => send(res))
  await state.refresh()
  assert.deepEqual(state.readConfiguration(), fixture)
})

test('TEST-BOOTSTRAP-SYNC-03 malformed or contradictory replacement cannot retain a last-good admission path', async t => {
  const server = await host(t)
  const state = sync(t, server.client)
  for (const edit of [v => { v.providers[0].agent.protocol = 'anthropic-messages' },
    v => { v.providers[0].api_key = 'changed-without-model-revision' },
    v => { v.mcp.runtime_token += '-changed-without-bootstrap-revision' },
    v => { revise(v, '2', '3'); v.providers = [] }]) {
    server.setHandler((_req, res) => send(res)); await state.refresh()
    const changed = clone(); edit(changed)
    server.setHandler((_req, res) => send(res, changed))
    await assert.rejects(state.refresh(), { message: 'agent_bootstrap_invalid' })
    unavailable(() => state.readConfiguration())
  }
  server.setHandler((_req, res) => send(res, { ...revise(), providers: [] }))
  await state.refresh()
  assert.deepEqual(state.readConfiguration().providers, [])
  assert.equal(state.status().configuration_current, true) // This is not model readiness.
})

test('TEST-BOOTSTRAP-SYNC-04 monotonic age, backwards clock and clock failure close configuration gates', async t => {
  let now = 100
  const server = await host(t)
  const state = sync(t, server.client, { clock: () => now })
  await state.refresh()
  now += 14999; assert.equal(state.status().configuration_current, true)
  server.setHandler((_req, res) => send(res, fixture, 304))
  await state.refresh()
  now += 14999; assert.equal(state.status().configuration_current, true)
  now++; unavailable(() => state.readConfiguration())
  assert.equal(state.status().error, 'agent_bootstrap_stale')
  server.setHandler((_req, res) => send(res))
  await state.refresh(); assert.equal(server.requests.at(-1).url, '/internal/v1/agent/bootstrap')
  now--; unavailable(() => state.readConfiguration())
  now = NaN
  await assert.rejects(state.refresh(), { message: 'agent_bootstrap_unavailable' })
  assert.equal(state.status().configuration_current, false)
})

test('TEST-BOOTSTRAP-SYNC-05 existing Run contexts use rotated public keys, provider revisions and current live policy', async t => {
  const value = clone(), request = signed(value)
  let allowed = true, checks = 0
  const server = await host(t, (_req, res) => send(res, value))
  const state = sync(t, server.client, { authorize: claims => { checks++; assert.ok(Object.isFrozen(claims)); return allowed } })
  await state.refresh()
  const run = state.verifyPrompt(request)
  assert.equal(run.authorize(), true)
  allowed = false
  assert.throws(() => run.authorize(), { message: 'agent_capability_rejected' })
  allowed = true
  const rotated = revise(structuredClone(value), '2', '3')
  rotated.capability.public_keys['key-2'] = generateKeyPairSync('ed25519').publicKey.export({ format: 'jwk' }).x
  server.setHandler((_req, res) => send(res, rotated)); await state.refresh()
  assert.equal(run.authorize(), true) // Overlapping rotation retains a still-trusted old key.
  delete rotated.capability.public_keys['key-1']; revise(rotated, '2', '4')
  await state.refresh()
  assert.throws(() => run.authorize(), { message: 'agent_capability_rejected' })
  assert.throws(() => state.verifyPrompt(request), { message: 'agent_capability_rejected' })
  assert.ok(checks >= 4)
  const revoked = revise(structuredClone(value)); revoked.providers = []
  server.setHandler((_req, res) => send(res, revoked)); await state.refresh()
  assert.throws(() => run.authorize(), { message: 'agent_capability_rejected' })
  assert.throws(() => state.verifyPrompt(request), { message: 'agent_capability_rejected' })
})

test('TEST-BOOTSTRAP-SYNC-06 revoked or unavailable state prevents a previously prepared Gateway call from dispatching', async t => {
  const value = clone(), request = signed(value)
  const destination = await host(t)
  value.mcp.url = destination.url.replace('/internal/v1/agent/bootstrap', '/internal/mcp')
  const server = await host(t, (_req, res) => send(res, value))
  let now = 100
  const state = sync(t, server.client, { clock: () => now })
  for (const mode of ['revoke', 'transport-rotation', 'failure', 'stale']) {
    server.setHandler((_req, res) => send(res, value)); await state.refresh()
    const gateway = new TrustedGateway({ url: value.mcp.url, runtimeToken: value.mcp.runtime_token, catalog: [queryTool] })
    gateway.beginRun('session-1', state.verifyPrompt(request))
    const call = gateway.prepare('session-1', 'query_kline', { symbol: 'TEST', limit: 1 })
    if (mode === 'failure') {
      server.setHandler((_req, res) => { res.writeHead(401); res.end('secret') })
      await assert.rejects(state.refresh(), { message: 'agent_bootstrap_unauthorized' })
    } else if (mode === 'stale') {
      now += 15000
    } else {
      const changed = revise(structuredClone(value), mode === 'revoke' ? '3' : '2', '3')
      if (mode === 'revoke') changed.providers = []
      else changed.mcp.runtime_token += '-rotation'
      server.setHandler((_req, res) => send(res, changed)); await state.refresh()
    }
    assert.throws(() => gateway.prepare('session-1', 'query_kline', { symbol: 'TEST', limit: 1 }), { message: 'agent_capability_rejected' })
    await assert.rejects(gateway.invoke(call), { message: 'agent_capability_rejected' })
    gateway.endRun('session-1', request.run_id)
  }
  assert.equal(destination.requests.length, 0)
})

test('TEST-BOOTSTRAP-SYNC-07 polling is immediate, serial, recovers after failure and stop aborts without late publication', async t => {
  const third = Promise.withResolvers(), closed = Promise.withResolvers()
  let requests = 0, completeSecond
  const second = Promise.withResolvers()
  const server = await host(t, (req, res) => {
    requests++
    if (requests === 1) { res.writeHead(500); res.end(); return }
    if (requests === 2) { completeSecond = () => send(res); second.resolve(); return }
    req.on('close', closed.resolve); third.resolve()
  })
  const state = sync(t, server.client, { pollIntervalMs: 5 })
  state.start(); state.start()
  await second.promise
  assert.equal(state.status().configuration_current, false)
  const secondPending = state.refresh()
  completeSecond(); await secondPending
  assert.equal(state.status().configuration_current, true)
  await third.promise
  assert.equal(requests, 3)
  await state.stop(); await closed.promise
  assert.equal(state.status().phase, 'stopped')
  unavailable(() => state.readConfiguration())
  await assert.rejects(state.refresh(), { message: 'agent_bootstrap_stopped' })
  assert.throws(() => state.start(), { message: 'agent_bootstrap_stopped' })
  assert.equal(requests, 3)
})

test('TEST-BOOTSTRAP-SYNC-08 missing or asynchronous live policy never becomes Run authority', async t => {
  const value = clone(), request = signed(value)
  const server = await host(t, (_req, res) => send(res, value))
  for (const authorize of [undefined, null]) {
    assert.throws(() => new BootstrapSync({ client: server.client, authorize }), { message: 'agent_bootstrap_configuration_invalid' })
  }
  for (const authorize of [async () => true, async () => { throw new Error('secret') },
    () => { throw new Error('secret') }, () => undefined]) {
    const state = sync(t, server.client, { authorize })
    await state.refresh()
    assert.throws(() => state.verifyPrompt(request), { message: 'agent_capability_rejected' })
  }
})

test('TEST-BOOTSTRAP-SYNC-09 a valid signature cannot override the current provider, model or model revision', async t => {
  const server = await host(t)
  for (const edit of [{ provider: 'other' }, { model: 'other' }, { model_config_revision: '3'.repeat(32) }]) {
    const value = clone(), request = signed(value, edit)
    server.setHandler((_req, res) => send(res, value))
    let checks = 0
    const state = sync(t, server.client, { authorize: () => { checks++; return true } })
    await state.refresh()
    assert.throws(() => state.verifyPrompt(request), { message: 'agent_capability_rejected' })
    assert.equal(checks, 0)
  }
})
