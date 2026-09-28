import assert from 'node:assert/strict'
import { appendFile, readdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import { fixture, request, sse, runtimeRoot, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'

const ajv = new Ajv2020({ allErrors: true })
addFormats(ajv)
const valid = ajv.compile(JSON.parse(await readFile(join(runtimeRoot, 'contracts/bridge-v1/transcript-page.schema.json'))))
async function page(host, query = '', sessionId = 'session-1') {
  const response = await host.transcript(sessionId, query)
  assert.equal(response.status, 200, await response.clone().text())
  assert.equal(response.headers.get('cache-control'), 'no-store')
  const body = await response.json()
  assert.equal(valid(body), true, JSON.stringify(valid.errors))
  return body
}
async function files(directory) {
  const entries = await readdir(directory, { recursive: true, withFileTypes: true })
  return entries.filter(entry => entry.isFile()).map(entry => join(entry.parentPath, entry.name)).sort()
}
async function stored(directory) {
  return Promise.all((await files(directory)).map(async path => [path, await readFile(path, 'utf8')]))
}

test('TEST-TRANSCRIPT-HTTP-01 empty/cold sessions, auth, query bounds and isolation', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await first.call('create', { sessionId: 'session-2' })
  const empty = await page(first)
  assert.deepEqual(empty.items, [])
  assert.equal(empty.has_more, false)
  assert.equal((await fetch(`${first.url}/q4d/v1/sessions/session-1`)).status, 401)
  assert.equal((await fetch(`${first.url}/q4d/v1/sessions/session-1`, {
    method: 'POST', headers: { authorization: `Bearer ${bridgeToken}` },
  })).status, 405)
  for (const query of ['limit=0', 'limit=101', 'limit=1&limit=2', 'snapshot_seq=-1', 'before_seq=01', 'unknown=1']) {
    assert.equal((await first.transcript('session-1', query)).status, 400, query)
  }
  assert.equal((await first.transcript('session-1', 'snapshot_seq=9007199254740991')).status, 409)
  assert.equal((await first.transcript('missing')).status, 404)
  await first.call('prompt', { request: request() })
  assert.deepEqual((await page(first, '', 'session-2')).items, [])
  assert.deepEqual((await page(first, `snapshot_seq=${empty.snapshot_seq}`)).items, [])
  await first.close()
  const second = await env.launch()
  assert.deepEqual((await page(second, '', 'session-2')).items, [])
  const before = await stored(env.directory)
  await page(second)
  assert.deepEqual(await stored(env.directory), before, 'cold GET must not write or repair')
})

test('TEST-TRANSCRIPT-HTTP-02 stable pagination through concurrent new Runs and cold process restart', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  for (let i = 1; i <= 3; i++) await first.call('prompt', { request: request(`query ${i}`, `run-${i}`) })
  const all = await page(first)
  assert.equal(all.items.length, 12)
  assert.deepEqual(all.items.map(item => item.role), Array(3).fill(['user', 'assistant', 'tool', 'assistant']).flat())
  let current = await page(first, 'limit=3')
  let collected = current.items
  await first.call('prompt', { request: request('query added during paging', 'run-4') })
  await first.close()
  const second = await env.launch()
  while (current.has_more) {
    current = await page(second, `limit=3&snapshot_seq=${current.snapshot_seq}&before_seq=${current.next_before_seq}`)
    collected = [...current.items, ...collected]
  }
  assert.deepEqual(collected, all.items)
  assert.equal(new Set(collected.map(item => item.seq)).size, collected.length)
  assert.equal((await page(second)).items.length, 16)
  await second.call('resume', { sessionId: 'session-1' })
  assert.deepEqual(await page(second, `snapshot_seq=${all.snapshot_seq}`), all)
  assert.deepEqual((await page(second, 'before_seq=0')).items, [])
})

test('TEST-TRANSCRIPT-HTTP-03 compaction changes model context while preserving paged user history and IDs', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await first.call('prompt', { request: request('query ' + 'market history '.repeat(250)) })
  await first.call('prompt', { request: request('query ' + 'second market history '.repeat(250), 'run-2') })
  const before = await page(first)
  assert.ok(await first.call('compact', { sessionId: 'session-1' }))
  assert.ok(JSON.stringify(await first.call('transcript', { sessionId: 'session-1' })).includes('Q4D_T00_SUMMARY'))
  assert.deepEqual((await page(first)).items, before.items)
  assert.deepEqual(await page(first, `snapshot_seq=${before.snapshot_seq}`), before)
  await first.close()
  const second = await env.launch()
  assert.deepEqual((await page(second)).items, before.items)
  await second.call('resume', { sessionId: 'session-1' })
  await second.call('prompt', { request: request('query after compaction', 'run-3') })
  const after = await page(second)
  assert.deepEqual(after.items.slice(0, before.items.length), before.items)
  assert.equal(after.items.length, before.items.length + 4)
  assert.ok(!JSON.stringify(after).includes('Q4D_T00_SUMMARY'))
})

test('TEST-TRANSCRIPT-HTTP-04 cold reads preserve unclaimed admission and torn tail without synthetic recovery', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await assert.rejects(first.call('prompt', { request: request(), fault: 'after_inbox_flush' }), /fixture exited/)
  const sessionFile = (await files(join(env.directory, 'sessions'))).find(path => path.endsWith('.jsonl'))
  assert.ok(sessionFile)
  await appendFile(sessionFile, '{"torn":')
  const before = await stored(env.directory)
  const second = await env.launch()
  const cold = await page(second)
  assert.equal(cold.items.filter(row => row.role === 'user').length, 1)
  assert.equal(cold.items.find(row => row.role === 'user').content[0].text, 'query')
  assert.deepEqual(await page(second), cold)
  assert.deepEqual(await stored(env.directory), before)
  await second.call('resume', { sessionId: 'session-1' })
  assert.deepEqual(await page(second, `snapshot_seq=${cold.snapshot_seq}`), cold)
  const user = cold.items.find(row => row.role === 'user')
  assert.equal((await page(second)).items.filter(row => row.message_id === user.message_id).length, 1)
  await second.call('prompt', { request: request('query next', 'run-2') })
  assert.deepEqual((await page(second)).items.find(row => row.message_id === user.message_id), user)
})

test('TEST-TRANSCRIPT-HTTP-05 pending approval is read without dispatch; transcript reload works after SSE 410', { timeout: 20_000 }, async t => {
  const gateway = await gatewayFixture(t)
  const env = await fixture(t)
  const approval = Promise.withResolvers()
  const options = { env: { Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken },
    onEvent: event => { if (event.event === 'approval_required') approval.resolve(event.call) } }
  const first = await env.launch(options)
  await first.call('create', { sessionId: 'session-1' })
  const input = request('gateway-approval')
  const run = gateway.issueRun('run-1', { sessionId: 'session-1' })
  const ack = await first.call('prompt', { request: input, run })
  const proposed = await approval.promise
  const pending = await page(first)
  assert.equal(pending.items[0].message_id, ack.message_id)
  assert.equal(pending.items[1].content[0].type, 'tool_request')
  assert.equal(gateway.executions.length, 0)
  assert.equal(gateway.attempts.length, 1)
  await first.call('cancel', { sessionId: 'session-1', runId: 'run-1' })
  await sse(await first.events('run-1'))
  assert.deepEqual(await page(first, `snapshot_seq=${pending.snapshot_seq}`), pending)
  const finished = await page(first)
  assert.ok(finished.items.some(row => row.role === 'tool' && row.content[0].is_error))
  await first.call('prune', { runId: 'run-1', through: '1' })
  assert.equal((await first.events('run-1', '0')).status, 410)
  await first.close()
  const second = await env.launch(options)
  assert.deepEqual((await page(second)).items, finished.items)
  assert.equal(gateway.attempts.length, 1)
  for (const hidden of [gateway.runtimeToken, run.capability, proposed.tool_call_id, 'q4d-query']) {
    assert.ok(!JSON.stringify(finished).includes(hidden))
  }
})

test('TEST-TRANSCRIPT-HTTP-06 corrupt committed history is an error, never a shortened successful page', { timeout: 20_000 }, async t => {
  const env = await fixture(t)
  const first = await env.launch()
  await first.call('create', { sessionId: 'session-1' })
  await first.call('prompt', { request: request() })
  await first.close()
  const sessionFile = (await files(join(env.directory, 'sessions'))).find(path => path.endsWith('.jsonl'))
  const lines = (await readFile(sessionFile, 'utf8')).split('\n')
  assert.ok(lines.some(line => line.includes('turn/end')), 'corruption must precede a committed Turn boundary')
  lines[1] = 'corrupt secret local path'
  await writeFile(sessionFile, lines.join('\n'))
  const second = await env.launch()
  const response = await second.transcript()
  assert.equal(response.status, 500)
  assert.deepEqual(await response.json(), { error: { code: 'agent_transcript_unavailable' } })
})
