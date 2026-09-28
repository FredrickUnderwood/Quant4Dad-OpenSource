import test from 'node:test'
import assert from 'node:assert/strict'
import { ApprovalWaiters, argumentsHash } from '../../../../src/runtime/approval-waiters.mjs'

test('TEST-APPROVAL-WAITER-01 binds immutable arguments, caller, decision and expiry', async () => {
  const waiters = new ApprovalWaiters(), controller = new AbortController()
  const call = { run_id: 'owned-run', tool_call_id: 'owned-call', name: 'create_strategy', arguments: { nested: { '中': [1, true, null] } } }
  const challenge = { approval_id: 'a'.repeat(64), arguments_hash: argumentsHash(call.arguments), risk: 'R2', expires_at: new Date(Date.now() + 10000).toISOString() }
  const observed = []
  const waiting = waiters.wait(call, challenge, controller.signal, (...v) => observed.push(v))
  assert.equal(observed[0][0], 'approval.required')
  assert.throws(() => waiters.decide(challenge.approval_id, { run_id: 'other-run', tool_call_id: 'owned-call', decision: 'allow_once', approval_receipt: 'a'.repeat(86) }))
  const reject = { run_id: 'owned-run', tool_call_id: 'owned-call', decision: 'reject' }
  waiters.decide(challenge.approval_id, reject)
  assert.equal(await waiting, undefined)
  assert.equal(controller.signal.aborted, false)
  waiters.decide(challenge.approval_id, reject) // Lost HTTP acknowledgement.
  assert.throws(() => waiters.decide(challenge.approval_id, { ...reject, decision: 'allow_once', approval_receipt: 'a'.repeat(86) }))
  assert.throws(() => waiters.wait(call, { ...challenge, approval_id: 'b'.repeat(64), arguments_hash: 'sha256:' + '0'.repeat(64) }, controller.signal, () => {}))
  const pending = waiters.wait(call, { ...challenge, approval_id: 'c'.repeat(64) }, controller.signal, () => {})
  controller.abort()
  await assert.rejects(pending, /agent_tool_cancelled/)
  assert.throws(() => waiters.decide('c'.repeat(64), reject))
})
