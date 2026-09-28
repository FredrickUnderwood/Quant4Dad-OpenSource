import test from 'node:test'
import assert from 'node:assert/strict'
import { updateTransaction } from '../../../../src/operations/update-transaction.mjs'

function fixture(fault = '') {
  const calls = [], state = { active: 'old', stopped: false, candidate: false }
  const steps = Object.fromEntries(['preflight', 'drain', 'stopPrevious', 'stage', 'startCandidate', 'verifyCandidate', 'stopCandidate', 'seal',
    'activate', 'healthCandidate', 'startPrevious', 'healthPrevious', 'rollback'].map(name => [name, async () => {
    calls.push(name)
    if (name === 'activate') state.active = 'candidate' // acknowledgement may be lost
    if (name === fault) throw new Error('fixture fault')
    if (name === 'rollback') state.active = 'old'
    if (name === 'stopPrevious') state.stopped = true
    if (name === 'startPrevious') { assert.equal(state.candidate, false); assert.equal(state.active, 'old'); state.stopped = false }
    if (name === 'startCandidate') state.candidate = true
    if (name === 'stopCandidate') state.candidate = false
  }]))
  steps.isCandidateActive = async () => state.active === 'candidate'
  return { calls, state, steps }
}
test('update transaction restores previous container after candidate faults and lost activation acknowledgement', async () => {
  for (const fault of ['stage', 'startCandidate', 'verifyCandidate', 'seal', 'activate', 'healthCandidate']) {
    const f = fixture(fault)
    assert.equal((await updateTransaction(f.steps)).status, 'restored_previous', fault)
    assert.equal(f.state.active, 'old'); assert.equal(f.state.stopped, false); assert.equal(f.state.candidate, false)
  }
  const success = fixture()
  assert.equal((await updateTransaction(success.steps)).status, 'updated')
  assert.equal(success.state.active, 'candidate'); assert.equal(success.state.stopped, true)
  assert.equal(success.calls.includes('startPrevious'), false)
})
test('update never restarts previous data if rollback would lose new writes or stopping the candidate fails', async () => {
  const f = fixture('healthCandidate')
  f.steps.rollback = async () => { throw new Error('new committed data') }
  await assert.rejects(updateTransaction(f.steps), /reconciliation_required/)
  assert.equal(f.calls.includes('startPrevious'), false)
  const unclean = fixture('stopCandidate')
  await assert.rejects(updateTransaction(unclean.steps), /reconciliation_required/)
  assert.equal(unclean.calls.includes('startPrevious'), false)
  const preflight = fixture('preflight')
  await assert.rejects(updateTransaction(preflight.steps)); assert.deepEqual(preflight.calls, ['preflight'])
})
