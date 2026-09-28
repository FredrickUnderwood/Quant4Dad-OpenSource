import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { RunSupervisor } from '../../../../src/runtime/run-supervisor.mjs'

test('long-run budget permits 600 model calls and 800 tools, then stops at the exact bound', t => {
  const budgets = JSON.parse(readFileSync(new URL('../../../../fixtures/long-run-budgets.json', import.meta.url)))
  for (const dimension of ['model', 'tool']) {
    const s = setup(t)
    const lease = s.begin({ ...s.run, expiresAt: Date.now() + budgets.wall_time_ms, claims: { envelope: { budgets } } })
    if (dimension === 'model') {
      for (let i = 0; i < 600; i++) lease.reserveModel(10000, 512).settle({ inputTokens: 10000, outputTokens: 128 })
      assert.equal(lease.snapshot().model_calls.used, 600)
      assert.throws(() => lease.reserveModel(10000, 512), { message: 'agent_run_budget_exceeded' })
    } else {
      for (let i = 0; i < 800; i++) lease.tool()
      assert.equal(lease.snapshot().tool_calls.used, 800)
      assert.throws(() => lease.tool(), { message: 'agent_run_budget_exceeded' })
    }
    assert.deepEqual(s.stops, ['agent_run_budget_exceeded'])
    assert.equal(lease.signal.aborted, true)
  }
})

function setup(t) {
  let status = { configuration_current: true, configuration_applied: true, revision: 'one' }, allowed = true
  const stops = []
  const supervisor = new RunSupervisor({ currentStatus: () => status })
  t.after(() => supervisor.close())
  const run = { sessionId: 'session-1', runId: 'run-1', expiresAt: Date.now() + 5000,
    authorize: () => allowed, claims: { envelope: { budgets: { max_turns: 2, max_tool_calls: 2, max_input_tokens: 100, max_output_tokens: 12 } } } }
  return { supervisor, run, stops, begin: (value = run) => supervisor.begin(value, code => stops.push(code)),
    set status(value) { status = value }, set allowed(value) { allowed = value } }
}
test('TEST-RUN-SUPERVISOR-01 exclusive Session ownership, exact generation and idempotent first stop', t => {
  const s = setup(t), lease = s.begin()
  assert.throws(() => s.begin(), { message: 'agent_run_in_progress' })
  s.status = { configuration_current: true, configuration_applied: true, revision: 'two' }
  s.supervisor.recheck()
  assert.equal(lease.signal.aborted, true)
  lease.stop('user')
  assert.deepEqual(s.stops, ['agent_configuration_unavailable'])
  lease.finish()
  assert.equal(s.begin().check(), true)
})
test('TEST-RUN-SUPERVISOR-02 current policy, expiry and shutdown cancel without waiting for a model chunk', t => {
  const s = setup(t), lease = s.begin()
  s.allowed = false; s.supervisor.recheck()
  assert.equal(lease.signal.aborted, true)
  assert.deepEqual(s.stops, ['agent_capability_rejected'])
  lease.finish(); s.allowed = true
  assert.throws(() => s.begin({ ...s.run, expiresAt: Date.now() - 1 }), { message: 'agent_capability_expired' })
  const next = s.begin(); s.supervisor.close()
  assert.equal(next.signal.aborted, true)
  assert.throws(() => s.begin(), { message: 'agent_runtime_unavailable' })
})
test('TEST-RUN-SUPERVISOR-03 reserve output even without usage; accumulated inputs and call count are bounded', t => {
  const s = setup(t), lease = s.begin()
  assert.equal(lease.dispatch(40, 8), 8)
  assert.equal(lease.dispatch(40, 8), 4)
  assert.throws(() => lease.dispatch(1, 8), { message: 'agent_run_budget_exceeded' })
  assert.deepEqual(s.stops, ['agent_run_budget_exceeded'])
  lease.finish()
  assert.throws(() => s.begin().dispatch(101, 8), { message: 'agent_run_budget_exceeded' })
})
test('TEST-RUN-SUPERVISOR-04 persistence failure still aborts and closes authority, invalid measurements never dispatch', t => {
  const s = setup(t)
  const lease = s.supervisor.begin(s.run, () => { throw new Error('disk unavailable') })
  s.allowed = false; s.supervisor.recheck()
  assert.equal(lease.signal.aborted, true)
  assert.equal(s.supervisor.toJSON().closed, true)
  const separate = setup(t)
  assert.throws(() => separate.begin().dispatch(Promise.resolve(1), 8), { message: 'agent_input_measurement_unavailable' })
})
test('TEST-RUN-SUPERVISOR-05 Tool calls have an independent cumulative budget and current policy gate', t => {
  const s = setup(t), lease = s.begin()
  lease.tool(); assert.equal(lease.dispatch(1, 4), 4); lease.tool()
  assert.throws(() => lease.tool(), { message: 'agent_run_budget_exceeded' })
  assert.equal(lease.signal.aborted, true); assert.deepEqual(s.stops, ['agent_run_budget_exceeded'])
  lease.finish()
  const next = s.begin(); s.allowed = false
  assert.throws(() => next.tool(), { message: 'agent_capability_rejected' })
})


test('TEST-RUN-SUPERVISOR-06 verified usage settles only its own reservation, including cached input', t => {
  const s = setup(t), lease = s.begin()
  const first = lease.reserveModel(90, 10)
  first.settle({ inputTokens: 2, cacheReadTokens: 3, cacheWriteTokens: 1, outputTokens: 2, reasoningTokens: 1 })
  first.settle({ inputTokens: 0, outputTokens: 0 })
  assert.equal(lease.snapshot().input_tokens.used, 6)
  assert.equal(lease.snapshot().output_tokens.used, 2)
  const second = lease.reserveModel(90, 10)
  assert.equal(second.maxTokens, 10)
  second.settle({ inputTokens: 4, outputTokens: 3 })
  assert.equal(lease.snapshot().input_tokens.used, 10)
  assert.equal(lease.snapshot().output_tokens.used, 5)
})
test('TEST-RUN-SUPERVISOR-07 missing usage stays reserved; invalid usage cannot refund', t => {
  const s = setup(t), lease = s.begin()
  const first = lease.reserveModel(90, 10)
  assert.throws(() => lease.reserveModel(11, 1), { message: 'agent_run_budget_exceeded' })
  first.settle({ inputTokens: 1, outputTokens: 1 })
  assert.equal(lease.snapshot().input_tokens.used, 90)
  const next = setup(t), invalid = next.begin(), reservation = invalid.reserveModel(40, 8)
  assert.throws(() => reservation.settle({ inputTokens: 1, outputTokens: 9 }), { message: 'agent_input_measurement_unavailable' })
  assert.equal(invalid.snapshot().output_tokens.used, 8)
})
test('TEST-RUN-SUPERVISOR-08 limit failures record dimension, used and requested values', t => {
  const s = setup(t), facts = []
  const lease = s.supervisor.begin(s.run, (code, budget) => facts.push({ code, budget }))
  lease.tool(); lease.tool()
  assert.throws(() => lease.tool())
  assert.deepEqual(facts, [{ code: 'agent_run_budget_exceeded', budget: { dimension: 'tool_calls', used: 2, limit: 2, requested: 1 } }])
})


test('TEST-RUN-SUPERVISOR-09 zero-filled provider usage does not create free budget', t => {
  const s = setup(t), lease = s.begin()
  lease.reserveModel(90, 10).settle({ inputTokens: 0, outputTokens: 0 })
  assert.equal(lease.snapshot().input_tokens.used, 90)
  assert.equal(lease.snapshot().output_tokens.used, 10)
  assert.throws(() => lease.reserveModel(11, 2), { message: 'agent_run_budget_exceeded' })
})

test('TEST-RUN-SUPERVISOR-10 confirmed pre-output rejection refunds only output once, retaining calls and input', t => {
  const s = setup(t), lease = s.begin(), reservation = lease.reserveModel(40, 8)
  reservation.rejectBeforeOutput(); reservation.rejectBeforeOutput()
  reservation.settle({ inputTokens: 1, outputTokens: 1 })
  assert.equal(lease.snapshot().output_tokens.used, 0)
  assert.equal(lease.snapshot().input_tokens.used, 40)
  assert.equal(lease.snapshot().model_calls.used, 1)
  assert.equal(lease.reserveModel(40, 8).maxTokens, 8)
})
