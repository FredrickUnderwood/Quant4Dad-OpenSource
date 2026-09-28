import assert from 'node:assert/strict'
import test from 'node:test'
import { createServer } from 'node:http'
import { setTimeout as delay } from 'node:timers/promises'
import { RunPolicy } from '../../../../src/auth/run-policy.mjs'
import { canonicalAuthorizationJSON } from '../../../../src/auth/run-capability.mjs'

const id = '01K00000000000000000000000'
const token = 'fixture-control-token-0123456789-abcdef'
async function setup(t, diagnostic) {
  const now = Math.floor(Date.now() / 1000)
  // Cache tests isolate transport/freshness; signature/schema checks are exercised
  // by run-capability contracts and the actual Go -> DSH process fixture.
  const claims = { iat: now, exp: now + 30, envelope: { run_id: id, budgets: { wall_time_ms: 30000 } } }
  let mode = 'ready', held
  const paths = [], diagnostics = []
  const server = createServer((req, res) => {
    paths.push(req.url)
    assert.equal(req.headers.authorization, 'Bearer ' + token)
    assert.equal(req.headers.cookie, undefined)
    const respond = () => {
      if (mode === 'disconnect') { res.destroy(); return }
      if (mode === 'redirect') { res.writeHead(302, { location: '/redirect-target' }); res.end(); return }
      res.writeHead(mode === 'deny' ? 403 : 200, { 'content-type': 'application/json', 'cache-control': mode === 'headers' ? 'public' : 'no-store', 'x-content-type-options': 'nosniff' })
      const raw = canonicalAuthorizationJSON(claims)
      res.end(mode === 'duplicate' ? raw.replace('"exp":', '"exp":1,"exp":') : mode === 'oversized' ? ' '.repeat(8193) : raw)
    }
    if (mode === 'hold') held = respond
    else respond()
  })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  const policy = new RunPolicy({ bootstrapURL: `http://127.0.0.1:${server.address().port}/internal/v1/agent/bootstrap`, controlToken: token,
    diagnostic: diagnostic ?? (row => diagnostics.push(row)) })
  t.after(async () => { await policy.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)) })
  return { policy, claims, paths, diagnostics, get mode() { return mode }, set mode(v) { mode = v }, release: () => held() }
}
test('RUN-POLICY-01 preflight requires an exact current grant and never uses cookies or redirects', async t => {
  const s = await setup(t)
  assert.equal(s.policy.authorize(s.claims), false)
  await s.policy.prepare({ run_id: id })
  assert.equal(s.policy.authorize(s.claims), true)
  assert.equal(s.policy.authorize({ ...s.claims, iat: s.claims.iat - 1 }), false)
  assert.deepEqual(s.paths, [`/internal/v1/agent/runs/${id}/authorization`])
  assert.ok(!JSON.stringify(s.policy).includes(token))
  s.policy.forget(id); assert.equal(s.policy.authorize(s.claims), false)
  s.mode = 'redirect'
  await assert.rejects(s.policy.prepare({ run_id: id }), { message: 'agent_capability_rejected' })
  assert.equal(s.paths.length, 2)
})
test('RUN-POLICY-02 denial and silent control failures invalidate cached authority within two seconds', async t => {
  const s = await setup(t)
  await s.policy.prepare({ run_id: id })
  s.mode = 'hold'
  await delay(2200)
  assert.equal(s.policy.authorize(s.claims), false)
  s.mode = 'ready'
  await s.policy.prepare({ run_id: id }); assert.equal(s.policy.authorize(s.claims), true)
  s.mode = 'deny'
  await assert.rejects(s.policy.prepare({ run_id: id }))
  assert.equal(s.policy.authorize(s.claims), false)
})
test('RUN-POLICY-03 invalid identities, duplicate members and oversized bodies cannot grant authority', async t => {
  const s = await setup(t)
  for (const bad of [id + '\n', '../bootstrap', '', 1]) await assert.rejects(s.policy.prepare({ run_id: bad }))
  assert.equal(s.paths.length, 0)
  for (const mode of ['duplicate', 'oversized']) {
    s.mode = mode; await assert.rejects(s.policy.prepare({ run_id: id }))
    assert.equal(s.policy.authorize(s.claims), false)
  }
})
test('RUN-POLICY-04 forgetting a completed Run cannot be undone by an in-flight policy response', async t => {
  const s = await setup(t); s.mode = 'hold'
  const rejected = assert.rejects(s.policy.prepare({ run_id: id }))
  while (s.paths.length === 0) await delay(5)
  s.policy.forget(id); s.mode = 'ready'; s.release()
  await rejected
  assert.equal(s.policy.authorize(s.claims), false)
  await s.policy.prepare({ run_id: id }); assert.equal(s.policy.authorize(s.claims), true)
  await s.policy.close(); assert.equal(s.policy.authorize(s.claims), false)
  await assert.rejects(s.policy.prepare({ run_id: id }))
})

test('RUN-POLICY-05 rejection diagnostics identify safe transport causes without changing fail-closed behavior', async t => {
  const s = await setup(t)
  for (const [mode, reason] of [['deny', 'http_status'], ['redirect', 'http_status'], ['headers', 'invalid_headers'],
    ['duplicate', 'invalid_body'], ['oversized', 'body_too_large'], ['disconnect', 'transport_failed'], ['hold', 'request_timeout']]) {
    s.mode = 'ready'; await s.policy.prepare({ run_id: id })
    s.mode = mode
    await assert.rejects(s.policy.prepare({ run_id: id }), { message: 'agent_capability_rejected' })
    assert.equal(s.policy.authorize(s.claims), false)
    const row = s.diagnostics.at(-1)
    assert.equal(row.reason, reason); assert.equal(row.run_id, id)
    assert.equal(row.event, 'agent_run_policy_rejected')
    assert.ok(row.elapsed_ms >= 0)
    if (mode === 'deny') assert.equal(row.http_status, 403)
    assert.ok(Object.keys(row).every(key => ['event', 'run_id', 'reason', 'elapsed_ms', 'http_status'].includes(key)))
  }
  const logs = JSON.stringify(s.diagnostics)
  assert.ok(!logs.includes(token)); assert.ok(!logs.includes('wall_time_ms'))
})

test('RUN-POLICY-06 stalled event-loop grants report age once and remain unauthorized until refreshed', async t => {
  const s = await setup(t)
  await s.policy.prepare({ run_id: id })
  // Deliberately prevent refresh timers from running, modeling a synchronous
  // runtime stall without weakening the production two-second authority bound.
  const until = performance.now() + 2050
  while (performance.now() < until) { /* controlled event-loop stall */ }
  assert.equal(s.policy.authorize(s.claims), false)
  assert.equal(s.policy.authorize(s.claims), false)
  assert.equal(s.diagnostics.length, 1)
  assert.equal(s.diagnostics[0].reason, 'grant_stale')
  assert.ok(s.diagnostics[0].age_ms >= 2000)
  assert.equal(s.diagnostics[0].refresh_pending, false)
  await s.policy.prepare({ run_id: id })
  assert.equal(s.policy.authorize(s.claims), true)
})

test('RUN-POLICY-07 synchronous rejection paths are classified once and logger failures cannot grant authority', async t => {
  const s = await setup(t)
  assert.equal(s.policy.authorize(s.claims), false)
  assert.equal(s.policy.authorize(s.claims), false)
  assert.equal(s.diagnostics.length, 1)
  assert.equal(s.diagnostics.at(-1).reason, 'grant_missing')
  await s.policy.prepare({ run_id: id })
  assert.equal(s.policy.authorize({ ...s.claims, exp: s.claims.exp + 1 }), false)
  assert.equal(s.diagnostics.at(-1).reason, 'claims_mismatch')
  await s.policy.prepare({ run_id: id })
  const now = t.mock.method(Date, 'now', () => (s.claims.exp + 1) * 1000)
  try {
    assert.equal(s.policy.authorize(s.claims), false)
    assert.equal(s.diagnostics.at(-1).reason, 'grant_expired')
  } finally { now.mock.restore() }
  await s.policy.prepare({ run_id: id })
  await s.policy.close()
  assert.equal(s.policy.authorize(s.claims), false)
  assert.equal(s.diagnostics.at(-1).reason, 'policy_closed')

  const broken = await setup(t, () => { throw new Error('log sink unavailable') })
  assert.equal(broken.policy.authorize(broken.claims), false)
  await broken.policy.prepare({ run_id: id })
  broken.mode = 'deny'
  await assert.rejects(broken.policy.prepare({ run_id: id }), { message: 'agent_capability_rejected' })
  assert.equal(broken.policy.authorize(broken.claims), false)
})
