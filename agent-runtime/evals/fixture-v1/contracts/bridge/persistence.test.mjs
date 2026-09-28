import assert from 'node:assert/strict'
import { appendFileSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { DurableLog } from '../../../../src/persistence/durable-log.mjs'
import { RequestIndex } from '../../../../src/persistence/request-index.mjs'
import { EventJournal } from '../../../../src/persistence/event-journal.mjs'

function file(t) {
  const directory = mkdtempSync(join(tmpdir(), 'q4d-durable-'))
  t.after(() => rmSync(directory, { recursive: true, force: true }))
  return join(directory, 'journal.jsonl')
}
const binding = {
  session_id: 'session-1', dsh_session_id: 'dsh-1', run_id: 'run-1', client_request_id: 'request-1',
  request_hash: 'sha256:' + 'a'.repeat(64), message_id: 'message-1', execution_envelope_digest: 'sha256:' + 'b'.repeat(64),
}
const event = type => ({ type, occurred_at: '2026-09-09T00:00:00.000Z', data: ({
  'run.started': { message_id: 'message-1', execution_envelope_digest: 'sha256:' + 'a'.repeat(64) },
  'message.completed': { message_id: 'answer-1', text: 'answer' },
  'run.cancelled': { reason: 'user' },
})[type] ?? {} })

test('TEST-DURABLE-01 partial trailing writes recover; complete corrupt records fail closed', t => {
  const path = file(t)
  let log = new DurableLog(path)
  log.append({ test: 'committed' })
  log.close()
  const committed = readFileSync(path)
  appendFileSync(path, '{"sequence":2,"data":')
  log = new DurableLog(path)
  assert.deepEqual(log.records, [{ test: 'committed' }])
  log.append({ test: 'after-recovery' })
  log.close()
  log = new DurableLog(path)
  assert.equal(log.records.length, 2)
  log.close()
  writeFileSync(path, committed.toString().replace('committed', 'corrupted'))
  assert.throws(() => new DurableLog(path), /agent_journal_corrupt/)
})

test('TEST-DURABLE-02 request retries preserve preallocated message identity across restart', t => {
  const path = file(t)
  let index = new RequestIndex(path)
  assert.deepEqual(index.prepare(binding), binding)
  index.close()
  index = new RequestIndex(path)
  t.after(() => index.close())
  assert.deepEqual(index.prepare({ ...binding, message_id: 'new-random-message' }), binding)
  for (const field of ['session_id', 'dsh_session_id', 'client_request_id', 'request_hash', 'execution_envelope_digest']) {
    assert.throws(() => index.prepare({ ...binding, [field]: 'changed' }), /agent_request_conflict/)
  }
  assert.throws(() => index.prepare({ ...binding, run_id: 'other-run' }), /agent_request_conflict/)
  assert.equal(readFileSync(path, 'utf8').trim().split('\n').length, 1)
})

test('TEST-DURABLE-03 event projection retry/restart keeps event IDs and detects conflicting facts', t => {
  const path = file(t)
  let journal = new EventJournal(path, 'session-1', 'run-1')
  const first = journal.append('dsh:1', event('run.started'))
  const second = journal.append('dsh:2', event('message.completed'))
  journal.close()
  journal = new EventJournal(path, 'session-1', 'run-1')
  t.after(() => journal.close())
  assert.deepEqual(journal.append('dsh:1', event('run.started')), first)
  assert.deepEqual(journal.replay(first.id), [second])
  assert.throws(() => journal.append('dsh:1', event('run.failed')), /agent_event_projection_conflict/)
  const third = journal.append('dsh:3', event('run.completed'))
  assert.equal(third.id, '3')
  for (const cursor of ['-1', '01', 'NaN', '4']) assert.throws(() => journal.replay(cursor), /agent_invalid_event_cursor/)
})

test('TEST-DURABLE-04 expired cursor remains expired after restart; boundary cursor still replays', t => {
  const path = file(t)
  let journal = new EventJournal(path, 'session-1', 'run-1')
  journal.append('1', event('run.started'))
  journal.append('2', event('message.completed'))
  journal.pruneThrough('1')
  journal.close()
  journal = new EventJournal(path, 'session-1', 'run-1')
  t.after(() => journal.close())
  assert.throws(() => journal.replay('0'), /agent_event_cursor_expired/)
  assert.equal(journal.replay('1')[0].id, '2')
  assert.throws(() => new EventJournal(path, 'other-session', 'run-1'), /agent_journal_corrupt/)
})

test('TEST-DURABLE-05 subscribers see committed facts once; terminal and disconnected consumers cannot mutate history', t => {
  const path = file(t)
  const journal = new EventJournal(path, 'session-1', 'run-1')
  t.after(() => journal.close())
  const seen = []
  journal.subscribe(() => { throw new Error('disconnected reader') })
  const stop = journal.subscribe(value => {
    const restored = new EventJournal(path, 'session-1', 'run-1')
    assert.deepEqual(restored.replay().at(-1), value)
    restored.close()
    seen.push(structuredClone(value))
    value.data.untrusted = true
  })
  const first = journal.append('1', event('run.started'))
  journal.append('1', event('run.started'))
  assert.deepEqual(seen, [first])
  assert.deepEqual(journal.replay(), [first])
  stop()
  journal.append('2', event('run.cancelled'))
  assert.equal(seen.length, 1)
  assert.throws(() => journal.append('3', event('message.completed')), /agent_run_terminal/)
  assert.throws(() => journal.append('4', event('run.completed')), /agent_run_terminal/)
  assert.equal(journal.terminal, 'run.cancelled')
})

test('TEST-DURABLE-06 Tool events require an adapter identity and reject receipt/context fields', t => {
  const journal = new EventJournal(file(t), 'session-1', 'run-1')
  t.after(() => journal.close())
  const proposed = { ...event('tool.proposed'), data: {
    tool_call_id: '01K4M000000000000000000000', source_seq: '5', name: 'query_kline', arguments: { symbol: '000001', limit: 2 },
    idempotency_key: 'q4d:run-1:01K4M000000000000000000000',
  } }
  assert.throws(() => journal.append('missing', event('tool.started')), /agent_invalid_event/)
  for (const secret of ['run_capability', 'runtime_token', 'approval_receipt', 'model_call_id']) {
    assert.throws(() => journal.append(secret, { ...proposed, data: { ...proposed.data, [secret]: 'secret' } }), /agent_invalid_event/)
  }
  journal.append('run', event('run.started'))
  assert.equal(journal.append('proposal', proposed).id, '2')
})

test('TEST-DURABLE-07 pending Tools settle before Run terminal; result and start cannot arrive twice', t => {
  const journal = new EventJournal(file(t), 'session-1', 'run-1')
  t.after(() => journal.close())
  const tool_call_id = '01K4M000000000000000000000'
  const fact = (type, data = {}) => ({ ...event(type), data: { tool_call_id, ...data } })
  journal.append('run', event('run.started'))
  journal.append('1', fact('tool.proposed', { source_seq: '5', name: 'query_kline', arguments: {}, idempotency_key: `q4d:run-1:${tool_call_id}` }))
  assert.throws(() => journal.append('result', fact('tool.completed')), /agent_invalid_tool_transition/)
  assert.throws(() => journal.append('terminal', event('run.completed')), /agent_unsettled_tool/)
  journal.append('2', fact('approval.required', { name: 'query_kline', approval_id: 'approval-1', arguments_hash: 'sha256:' + 'a'.repeat(64), risk: 'R3', expires_at: '2026-09-09T01:00:00.000Z' }))
  journal.append('3', fact('tool.failed', { code: 'agent_approval_denied' }))
  assert.throws(() => journal.append('late', fact('tool.started')), /agent_invalid_tool_transition/)
  assert.throws(() => journal.append('duplicate', fact('tool.failed', { code: 'agent_tool_failed' })), /agent_invalid_tool_transition/)
  journal.append('4', event('run.completed'))
  assert.equal(journal.snapshot().length, 5)
})
