import test from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import { EventEmitter } from 'node:events'
import { syncBuiltinESMExports } from 'node:module'
import { RunPolicy } from '../../../../src/auth/run-policy.mjs'
import { canonicalAuthorizationJSON } from '../../../../src/auth/run-capability.mjs'

async function fixture(t, responseDelay = 800) {
  let now = 0, mode = 'ready', inFlight = 0, maxInFlight = 0
  t.mock.timers.enable({ apis: ['setInterval', 'setTimeout'] })
  t.mock.method(performance, 'now', () => now)
  const id = '01K00000000000000000000000', iat = Math.floor(Date.now() / 1000)
  const claims = { iat, exp: iat + 30, envelope: { run_id: id, budgets: { wall_time_ms: 30000 } } }
  const diagnostics = [], starts = [], completed = []
  t.mock.method(http, 'request', (_url, _options, callback) => {
    const req = new EventEmitter()
    let ended = false, started
    req.destroy = () => { if (!ended) { ended = true; inFlight-- } }
    req.end = () => {
      started = now; starts.push(now); inFlight++; maxInFlight = Math.max(maxInFlight, inFlight)
      if (mode === 'hold') return
      const respond = () => {
        if (ended) return
        ended = true; inFlight--
        const res = new EventEmitter(); res.destroy = () => {}; res.statusCode = mode === 'deny' ? 403 : 200; res.complete = true
        res.headers = { 'content-type': 'application/json', 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' }
        res.rawHeaders = Object.entries(res.headers).flat()
        callback(res); res.emit('data', Buffer.from(canonicalAuthorizationJSON(claims))); res.emit('end')
        if (res.statusCode === 200) completed.push(started)
      }
      if (starts.length === 1) queueMicrotask(respond); else setTimeout(respond, responseDelay)
    }
    return req
  })
  syncBuiltinESMExports()
  const policy = new RunPolicy({ bootstrapURL: 'http://127.0.0.1:1/internal/v1/agent/bootstrap',
    controlToken: 'fixture-control-token-0123456789-abcdef', diagnostic: row => diagnostics.push(row) })
  t.after(async () => { await policy.close(); t.mock.restoreAll(); syncBuiltinESMExports() })
  await policy.prepare({ run_id: id })
  const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve() }
  const advance = async (value, runCatchup = true) => {
    const delta = value - now; now = value; t.mock.timers.tick(delta); await flush()
    if (runCatchup) { t.mock.timers.tick(0); await flush() }
  }
  return { policy, id, claims, diagnostics, starts, completed, advance,
    get maxInFlight() { return maxInFlight }, get now() { return now },
    set now(value) { now = value }, set mode(value) { mode = value } }
}

function expireExactly(s) {
  // No timer runs after this jump: an in-flight or queued refresh cannot
  // extend the latest successful request's original two-second authority.
  s.now = s.completed.at(-1) + 2000
  assert.equal(s.policy.authorize(s.claims), false)
  const row = s.diagnostics.at(-1)
  assert.equal(row.reason, 'grant_stale'); assert.equal(row.age_ms, 2000)
  assert.ok(row.last_refresh_elapsed_ms >= 0)
  assert.equal(row.last_refresh_elapsed_ms + row.since_refresh_completed_ms, 2000)
  if (row.refresh_pending) assert.equal(row.pending_age_ms, s.now - s.starts.at(-1))
  assert.ok(row.poll_delay_ms >= 0)
}

test('RUN-POLICY-SCHEDULING-01 early refresh tolerates 600ms scheduling drift with an 800ms response', async t => {
  const s = await fixture(t)
  await s.advance(500); await s.advance(750)
  // The only deliberate pause is 750–1350ms. All later boundaries are checked.
  await s.advance(1350)
  assert.equal(s.policy.authorize(s.claims), true)
  assert.deepEqual(s.diagnostics, [{ event: 'agent_run_policy_refresh_slow', run_id: s.id, elapsed_ms: 850, poll_delay_ms: 350 }])
  for (let at = 1400; at <= 5100; at += 50) {
    await s.advance(at); assert.equal(s.policy.authorize(s.claims), true, `authority gap at ${at}ms`)
  }
  assert.deepEqual(s.starts, [0, 500, 1350, 2150, 2950, 3750, 4550])
  assert.equal(s.maxInFlight, 1)
  assert.equal(s.diagnostics.filter(row => row.event === 'agent_run_policy_refresh_slow').length, 1)
  expireExactly(s)
})

test('RUN-POLICY-SCHEDULING-02 a successful response catches up a tick skipped in flight before a later timer stall', async t => {
  const s = await fixture(t)
  await s.advance(500); await s.advance(1000); await s.advance(1300)
  // The 1000ms tick was skipped in flight. Completion starts the due refresh
  // at 1300ms instead of leaving it for the 1500ms tick, delayed to 1934ms.
  assert.deepEqual(s.starts, [0, 500, 1300])
  await s.advance(1400); await s.advance(1934)
  for (let at = 1950; at <= 5100; at += 50) {
    await s.advance(at); assert.equal(s.policy.authorize(s.claims), true, `authority gap at ${at}ms`)
  }
  assert.deepEqual(s.starts, [0, 500, 1300, 2100, 2900, 3700, 4500])
  assert.equal(s.maxInFlight, 1)
  assert.ok(!s.diagnostics.some(row => row.reason === 'grant_stale'))
  expireExactly(s)
})

test('RUN-POLICY-SCHEDULING-03 fast responses retain the 500ms periodic cadence without extra requests', async t => {
  const s = await fixture(t, 20)
  for (let at = 10; at <= 3000; at += 10) await s.advance(at)
  assert.deepEqual(s.starts, [0, 500, 1000, 1500, 2000, 2500, 3000])
  assert.deepEqual(s.diagnostics, [])
  assert.equal(s.maxInFlight, 1)
})

test('RUN-POLICY-SCHEDULING-04 forget and close cancel an already queued catch-up task', async t => {
  for (const action of ['forget', 'close']) await t.test(action, async t => {
    const s = await fixture(t)
    await s.advance(500); await s.advance(1300, false)
    assert.deepEqual(s.starts, [0, 500]) // successful response queued, not executed
    if (action === 'forget') s.policy.forget(s.id); else await s.policy.close()
    await s.advance(1300)
    for (let at = 1350; at <= 3000; at += 50) await s.advance(at)
    assert.deepEqual(s.starts, [0, 500])
    assert.equal(s.policy.authorize(s.claims), false)
  })
})

test('RUN-POLICY-SCHEDULING-05 denied or timed-out requests revoke authority without catch-up retries', async t => {
  for (const mode of ['deny', 'hold']) await t.test(mode, async t => {
    const s = await fixture(t); s.mode = mode
    for (let at = 50; at <= 3000; at += 50) await s.advance(at)
    assert.deepEqual(s.starts, [0, 500])
    assert.equal(s.policy.authorize(s.claims), false)
    assert.equal(s.diagnostics.at(-1).reason, mode === 'deny' ? 'http_status' : 'request_timeout')
    assert.equal(s.maxInFlight, 1)
  })
})
