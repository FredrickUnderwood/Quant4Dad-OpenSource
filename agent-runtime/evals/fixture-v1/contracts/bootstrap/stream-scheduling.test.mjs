import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { setImmediate as yieldToIO } from 'node:timers/promises'
import { DshBootstrapApplier } from '../../../../src/bootstrap/applier.mjs'
import { RunPolicy } from '../../../../src/auth/run-policy.mjs'
import { RunSupervisor } from '../../../../src/runtime/run-supervisor.mjs'
import { canonicalAuthorizationJSON } from '../../../../src/auth/run-capability.mjs'
import { clone, controlToken } from './helpers.mjs'

async function applierFixture(t, stream) {
  const snapshot = clone()
  let derived
  const status = () => ({ configuration_current: true, configuration_applied: true, revision: snapshot.revision })
  const applier = new DshBootstrapApplier({
    store: { async write(value) { derived = value; return { async remove() {} } } }, currentStatus: status,
    createGeneration: async () => ({
      async describe() { return snapshot.providers.map(p => ({ provider: derived.routes[p.id], model: p.default_model,
        context_window: p.agent.context_window, max_output_tokens: p.agent.max_output_tokens })) },
      stream, async close() {},
    }),
  })
  const stage = await applier.prepare(snapshot); stage.commit()
  t.after(async () => { applier.invalidate(); await applier.drain() })
  return { applier, status, request: { provider: snapshot.providers[0].id, model: snapshot.providers[0].default_model, messages: [] } }
}

test('STREAM-SCHEDULING-01 buffered real HTTP chunks allow live policy refresh throughout synchronous validation work', { timeout: 10000 }, async t => {
  const id = '01K00000000000000000000001', iat = Math.floor(Date.now() / 1000)
  const claims = { iat, exp: iat + 30, envelope: { run_id: id, budgets: { wall_time_ms: 30000 } } }
  let reads = 0
  const server = createServer((req, res) => {
    if (req.url === '/chunks') { res.end(' '.repeat(1600)); return }
    assert.equal(req.url, `/internal/v1/agent/runs/${id}/authorization`)
    assert.equal(req.headers.authorization, `Bearer ${controlToken}`)
    reads++
    res.writeHead(200, { 'content-type': 'application/json', 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' })
    res.end(canonicalAuthorizationJSON(claims))
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  const origin = `http://127.0.0.1:${server.address().port}`
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  const diagnostics = []
  const policy = new RunPolicy({ bootstrapURL: origin + '/internal/v1/agent/bootstrap', controlToken, diagnostic: row => diagnostics.push(row) })
  t.after(() => policy.close())
  const { applier, request } = await applierFixture(t, async function* () {
    const response = await fetch(origin + '/chunks')
    for await (const bytes of response.body) for (const byte of bytes) yield { type: 'text-delta', text: String.fromCharCode(byte) }
  })
  await policy.prepare({ run_id: id })
  let count = 0
  for await (const chunk of applier.stream(request, () => {
    // Bounded stand-in for synchronous catalog/signature/session processing.
    // Real HTTP authority and its unchanged two-second age are not mocked.
    const through = performance.now() + 1.5
    while (performance.now() < through) { /* synchronous per-chunk work */ }
    return policy.authorize(claims)
  })) { assert.equal(chunk.type, 'text-delta'); count++ }
  assert.equal(count, 1600)
  assert.ok(reads >= 4, `expected live refreshes during the stream, got ${reads}`)
  assert.ok(!diagnostics.some(row => row.event === 'agent_run_policy_rejected'), JSON.stringify(diagnostics))
})

for (const change of ['policy', 'generation', 'abort']) {
  test(`STREAM-SCHEDULING-02 ${change} changed during a yield suppresses the pending chunk and closes the iterator`, async t => {
    let allowed = true, closed = false, count = 0, changedAt
    const abort = new AbortController()
    const { applier, request } = await applierFixture(t, async function* () {
      try {
        for (let i = 0; i < 64; i++) {
          if (i === 31) setImmediate(() => {
            changedAt = count
            if (change === 'policy') allowed = false
            else if (change === 'generation') applier.invalidate()
            else abort.abort()
          })
          yield { type: 'text-delta', text: 'must not escape after authority changes' }
        }
      } finally { closed = true }
    })
    await assert.rejects(async () => {
      for await (const chunk of applier.stream({ ...request, signal: abort.signal }, () => allowed)) count++
    }, { message: change === 'generation' ? 'agent_configuration_unavailable' : 'agent_capability_rejected' })
    assert.ok(changedAt < 64)
    assert.equal(count, changedAt)
    assert.equal(closed, true)
    await yieldToIO()
  })
}

test('STREAM-SCHEDULING-03 a silent backend still stops through the supervisor without waiting for a chunk', { timeout: 2000 }, async t => {
  let allowed = true, closed = false, delivered = 0
  const { applier, request, status } = await applierFixture(t, async function* ({ signal }) {
    try {
      await new Promise(resolve => signal.addEventListener('abort', resolve, { once: true }))
      yield { type: 'text-delta', text: 'suppressed after cancellation' }
    } finally { closed = true }
  })
  const supervisor = new RunSupervisor({ currentStatus: status })
  t.after(() => supervisor.close())
  const stops = []
  const lease = supervisor.begin({ sessionId: 'session', runId: 'run', expiresAt: Date.now() + 5000, authorize: () => allowed,
    claims: { envelope: { budgets: { max_turns: 1, max_tool_calls: 0, max_input_tokens: 100, max_output_tokens: 100 } } } }, code => stops.push(code))
  const keepAlive = setTimeout(() => {}, 1500); t.after(() => clearTimeout(keepAlive))
  const timer = setTimeout(() => { allowed = false }, 20); t.after(() => clearTimeout(timer))
  await assert.rejects(async () => {
    for await (const chunk of applier.stream({ ...request, signal: lease.signal }, lease.check)) delivered++
  }, { message: 'agent_capability_rejected' })
  assert.equal(delivered, 0); assert.equal(closed, true)
  assert.deepEqual(stops, ['agent_capability_rejected'])
})
