import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import { EventJournal } from '../../../../src/persistence/event-journal.mjs'
import { DurableLog } from '../../../../src/persistence/durable-log.mjs'
import { projectUsage, terminalData, projectRejectedTool } from '../../../../src/bridge/event-projection.mjs'

const json = url => JSON.parse(readFileSync(new URL(url, import.meta.url)))
const ajv = new Ajv2020({ strict: true })
addFormats(ajv)
ajv.addSchema(json('../../../../contracts/bridge-v1/tool-event.schema.json'))
const schema = json('../../../../contracts/bridge-v1/event.schema.json')
const valid = ajv.compile(schema)
const samples = json('./fixtures/events.valid.json')
const fact = type => ({ type, occurred_at: '2026-09-09T00:00:00.000Z', data: structuredClone(samples[type]) })
const envelope = type => ({ id: '1', run_id: 'run-1', session_id: 'session-1', schema_version: 1, ...fact(type) })
function setup(t) {
  const dir = mkdtempSync(join(tmpdir(), 'q4d-events-'))
  const path = join(dir, 'events.jsonl')
  const journal = new EventJournal(path, 'session-1', 'run-1')
  t.after(() => { journal.close(); rmSync(dir, { recursive: true, force: true }) })
  return { path, journal }
}

test('TEST-EVENT-02 every payload rejects extra fields and malformed required values', () => {
  assert.deepEqual(new Set(Object.keys(samples)), new Set(schema.properties.type.enum))
  for (const type of Object.keys(samples)) {
    const event = envelope(type)
    assert.equal(valid(event), true, `${type}: ${JSON.stringify(valid.errors)}`)
    for (const key of ['runtime_token', 'approval_receipt', 'raw_error', 'provider_metadata']) {
      assert.equal(valid({ ...event, data: { ...event.data, [key]: 'secret' } }), false, type)
    }
    for (const key of Object.keys(event.data)) {
      const data = { ...event.data }; delete data[key]
      assert.equal(valid({ ...event, data }), false, `${type} must require ${key}`)
    }
  }
  for (const value of [-1, 0.5, Number.MAX_SAFE_INTEGER + 1, '10']) {
    const event = envelope('usage.updated'); event.data.usage.input_tokens = value
    assert.equal(valid(event), false)
  }
  const usage = envelope('usage.updated'); usage.data.usage.provider = 'secret'
  assert.equal(valid(usage), false)
  const failed = envelope('run.failed'); failed.data.retryable = true
  assert.equal(valid(failed), false)
})

test('TEST-EVENT-03 normalized usage and terminal data never copy upstream diagnostics or invent counts', () => {
  assert.deepEqual(projectUsage({ inputTokens: 3, outputTokens: 8, cacheReadTokens: 5, cacheWriteTokens: 2,
    reasoningTokens: 4, totalTokens: 18, provider_secret: 'secret' }), {
    input_tokens: 3, output_tokens: 8, cache_read_tokens: 5, cache_write_tokens: 2, reasoning_tokens: 4, total_tokens: 18,
  })
  assert.deepEqual(projectUsage({ inputTokens: 0, outputTokens: 0 }), { input_tokens: 0, output_tokens: 0 })
  for (const [kind, code] of [['error', 'agent_model_error'], ['max-tokens', 'agent_model_limit'],
    ['blocked', 'agent_run_blocked'], ['future', 'agent_run_failed']]) {
    assert.deepEqual(terminalData('failed', { kind, error: { secret: 'sensitive path' } }), { code, retryable: false })
  }
  assert.deepEqual(terminalData('interrupted'), samples['run.interrupted'])
  assert.deepEqual(terminalData('cancelled'), samples['run.cancelled'])
})

test('TEST-EVENT-04 run/message ordering is checked before writing and again on restore', t => {
  const { path, journal } = setup(t)
  assert.throws(() => journal.append('early', fact('message.completed')), /agent_invalid_run_transition/)
  journal.append('run', fact('run.started'))
  assert.throws(() => journal.append('repeat-run', fact('run.started')), /agent_invalid_run_transition/)
  journal.append('message', fact('message.completed'))
  assert.throws(() => journal.append('late', fact('message.delta')), /agent_message_terminal/)
  assert.throws(() => journal.append('duplicate', fact('message.completed')), /agent_message_terminal/)
  assert.equal(journal.snapshot().length, 2)
  journal.close()
  const log = new DurableLog(path)
  log.append({ kind: 'event', sourceKey: 'corrupt', event: { ...envelope('run.failed'), id: '3', data: { raw_error: 'secret' } } })
  log.close()
  assert.throws(() => new EventJournal(path, 'session-1', 'run-1'), /agent_journal_corrupt/)
})

test('TEST-EVENT-05 Tool source identity cannot be reused or escalate a rejected proposal', t => {
  const { journal } = setup(t)
  journal.append('run', fact('run.started'))
  const proposal = fact('tool.proposed'); proposal.data.arguments_omitted = true
  journal.append('tool', proposal)
  for (const type of ['tool.started', 'tool.completed', 'approval.required']) {
    assert.throws(() => journal.append(type, fact(type)), /agent_rejected_tool_transition/)
  }
  const other = fact('tool.proposed')
  other.data.tool_call_id = '01K4M000000000000000000001'
  other.data.idempotency_key = `q4d:run-1:${other.data.tool_call_id}`
  assert.throws(() => journal.append('same-source', other), /agent_tool_identity_conflict/)
  other.data.source_seq = '6'; other.data.idempotency_key = `q4d:other-run:${other.data.tool_call_id}`
  assert.throws(() => journal.append('wrong-run', other), /agent_tool_identity_conflict/)
  const retained = fact('tool.proposed'); retained.data.arguments_omitted = true
  retained.data.arguments = { run_capability: 'rejected-argument' }
  assert.throws(() => journal.append('unredacted', retained), /agent_invalid_event/)
  journal.append('failed', fact('tool.failed'))
  journal.append('done', fact('run.completed'))
})

test('TEST-EVENT-06 rejected Tool recovery preserves one durable ID across proposal/result crash', t => {
  const { path, journal } = setup(t)
  journal.append('run', fact('run.started'))
  const call = { seq: 5, time: 1000, type: 'tool/call', data: { turn: 0, step: 0, callId: 'repeat', name: 'shell', arguments: 'secret malformed' } }
  const result = { seq: 6, time: 1001, type: 'tool/result', sourceEventSeqs: [5], data: { turn: 0, step: 0,
    message: { content: [{ type: 'tool-result', isError: true, content: [{ type: 'text', text: 'Error: agent_tool_forbidden' }] }] } } }
  assert.throws(() => projectRejectedTool(journal, result, [call, result], 'run-1', () => { throw new Error('crash') }), /crash/)
  const proposal = journal.snapshot().at(-1)
  assert.deepEqual(proposal.data.arguments, {})
  assert.equal(proposal.data.arguments_omitted, true)
  journal.close()
  const restored = new EventJournal(path, 'session-1', 'run-1')
  t.after(() => restored.close())
  projectRejectedTool(restored, result, [call, result], 'run-1')
  projectRejectedTool(restored, result, [call, result], 'run-1')
  assert.equal(restored.snapshot().length, 3)
  assert.equal(restored.snapshot().at(-1).data.tool_call_id, proposal.data.tool_call_id)
  assert.equal(restored.snapshot().at(-1).data.code, 'agent_tool_forbidden')
  assert.ok(!readFileSync(path, 'utf8').includes('secret malformed'))
  assert.throws(() => projectRejectedTool(restored, { ...result, sourceEventSeqs: [999] }, [call, result], 'run-1'), /agent_tool_source_missing/)
  const unobserved = { ...call, seq: 7 }
  const success = { ...result, seq: 8, sourceEventSeqs: [7], data: { ...result.data,
    message: { content: [{ type: 'tool-result', isError: false, content: [] }] } } }
  assert.throws(() => projectRejectedTool(restored, success, [unobserved, success], 'run-1'), /agent_unobserved_tool_dispatch/)
  assert.equal(restored.snapshot().length, 3)
})
