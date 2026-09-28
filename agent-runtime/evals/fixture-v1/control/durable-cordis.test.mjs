import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'
import { fixture, request, sse, runtimeRoot, bridgeToken } from '../helpers/durable-host.mjs'

test('TEST-DSH-06/10 Cordis request retry, HTTP replay and capabilities survive process restart', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  const input = request()
  const [accepted, duplicate] = await Promise.all([
    first.call('prompt', { request: input }), first.call('prompt', { request: input }),
  ])
  assert.equal(accepted.durable, true)
  assert.equal(accepted.state, 'completed')
  assert.deepEqual(duplicate, accepted)
  const before = await sse(await first.events(input.run_id))
  assert.equal(before[0].type, 'run.started')
  assert.equal(before.at(-1).type, 'run.completed')
  assert.ok(before.some(event => event.type === 'message.completed' && event.data.text === 'Q4D_T00_OK'))
  await first.close()

  const second = await env.launch()
  await second.call('resume', { sessionId: 'session-1' })
  assert.deepEqual(await second.call('prompt', { request: input }), accepted)
  assert.deepEqual(await sse(await second.events(input.run_id)), before)
  assert.deepEqual(await sse(await second.events(input.run_id, before[0].id)), before.slice(1))
  await assert.rejects(second.call('prompt', { request: request('changed') }), /agent_request_conflict/)
  await assert.rejects(second.call('prompt', { request: { ...input, execution_envelope_digest: 'sha256:' + 'b'.repeat(64) } }), /agent_request_conflict/)
  const events = await second.call('inspect', { sessionId: 'session-1' })
  assert.equal(events.filter(event => event.type === 'user/message' && event.data.id === accepted.message_id).length, 1)
  const capResponse = await fetch(`${second.url}/q4d/v1/capabilities`, { headers: { authorization: `Bearer ${bridgeToken}` } })
  const capabilities = await capResponse.json()
  const schema = JSON.parse(await readFile(join(runtimeRoot, 'contracts/bridge-v1/capabilities.schema.json')))
  const valid = new Ajv2020().compile(schema)
  assert.equal(valid(capabilities), true, JSON.stringify(valid.errors))
  assert.equal(capabilities.backend, 'cordis')
  assert.equal(capabilities.features.raw_provider_delta, false)
  assert.equal(capabilities.features.compaction_events, false)
  assert.equal((await fetch(`${second.url}/q4d/v1/capabilities`)).status, 401)
})

for (const fault of ['after_intent', 'after_inbox_flush', 'after_turn_flush', 'after_first_event', 'after_projection']) {
  test(`TEST-DSH-10 SIGKILL ${fault}: same request never inserts a second DSH inbox message`, { timeout: 20_000 }, async t => {
    const env = await fixture(t)
    const first = await env.launch()
    await first.call('create', { sessionId: 'session-1' })
    const input = request()
    await assert.rejects(first.call('prompt', { request: input, fault }), /fixture exited/)
    assert.equal((await first.exited)[1], 'SIGKILL')
    const second = await env.launch()
    await second.call('resume', { sessionId: 'session-1' })
    const recovered = await second.call('prompt', { request: input })
    assert.equal(recovered.durable, true)
    assert.ok(['completed', 'interrupted'].includes(recovered.state))
    if (fault !== 'after_inbox_flush') assert.equal(recovered.state, 'completed')
    assert.deepEqual(await second.call('prompt', { request: input }), recovered)
    const events = await second.call('inspect', { sessionId: 'session-1' })
    assert.equal(events.filter(event => event.type === 'agent/inbox/spliced' &&
      event.data.inserted.some(message => message.id === recovered.message_id)).length, 1)
    const userMessages = events.filter(event => event.type === 'user/message' && event.data.id === recovered.message_id).length
    assert.ok(userMessages <= 1)
    if (recovered.state === 'completed') assert.equal(userMessages, 1)
    const replayed = await sse(await second.events(input.run_id))
    assert.equal(replayed[0].type, 'run.started')
    assert.equal(replayed.at(-1).type, `run.${recovered.state}`)
    const next = await second.call('prompt', { request: request('query after crash', 'run-2') })
    assert.equal(next.state, 'completed')
  })
}

test('TEST-DSH-06 HTTP replay returns 410 for expired cursors after process restart', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await first.call('prompt', { request: request() })
  await first.call('prune', { runId: 'run-1', through: '1' })
  await first.close()
  const second = await env.launch()
  assert.equal((await second.events('run-1', '0')).status, 410)
  const remaining = await sse(await second.events('run-1', '1'))
  assert.equal(remaining[0].id, '2')
  assert.equal((await second.events('run-1', '999999999999999999999')).status, 400)
})

test('TEST-DSH-09 real DSH compaction changes model history and still permits Tool calls after restart', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await first.call('prompt', { request: request('query ' + 'historical market research context '.repeat(100)) })
  await first.call('prompt', { request: request('query with more research history '.repeat(100), 'run-2') })
  const before = await first.call('transcript', { sessionId: 'session-1' })
  const compacted = await first.call('compact', { sessionId: 'session-1' })
  assert.ok(compacted, 'compaction must execute, not return a no-op')
  const after = await first.call('transcript', { sessionId: 'session-1' })
  assert.ok(JSON.stringify(after).length < JSON.stringify(before).length)
  assert.ok(JSON.stringify(after).includes('Q4D_T00_SUMMARY'))
  const raw = await first.call('inspect', { sessionId: 'session-1' })
  assert.ok(raw.some(event => event.type === 'compaction/start'))
  assert.ok(raw.some(event => event.type === 'compaction/end'))
  const previousCalls = raw.filter(event => event.type === 'tool/call').length
  await first.close()

  const second = await env.launch()
  await second.call('resume', { sessionId: 'session-1' })
  const result = await second.call('prompt', { request: request('query after compaction', 'run-3') })
  assert.equal(result.state, 'completed')
  const resumed = await second.call('inspect', { sessionId: 'session-1' })
  assert.equal(resumed.filter(event => event.type === 'tool/call').length, previousCalls + 1)
  const actualInputs = (await readFile(join(env.directory, 'model-calls.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse)
  assert.ok(actualInputs.some(call => call.purpose === 'compaction'))
  assert.ok(JSON.stringify(actualInputs.at(-1).messages).includes('Q4D_T00_SUMMARY'))
  assert.ok(!JSON.stringify(actualInputs.at(-1).messages).includes('historical market research context '.repeat(100)))
})
