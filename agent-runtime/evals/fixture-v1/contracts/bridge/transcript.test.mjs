import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import { paginateTranscript, parseTranscriptQuery, serveTranscript } from '../../../../src/bridge/transcript.mjs'
import { EventEmitter } from 'node:events'

const ajv = new Ajv2020({ allErrors: true })
addFormats(ajv)
const valid = ajv.compile(JSON.parse(readFileSync(new URL('../../../../contracts/bridge-v1/transcript-page.schema.json', import.meta.url))))
const binding = { session_id: 'session-1', run_id: 'run-1', message_id: 'user-1' }
const user = { id: 'user-1', source: { kind: 'user' }, content: [{ type: 'text', text: 'query' }] }
const assistant = (id, content) => ({ id, source: { kind: 'model', private: 'provider-secret' }, content })
const event = (seq, type, data, surfaceOp) => ({ seq, type, data, time: 1000 + seq, ...(surfaceOp ? { surfaceOp } : {}) })
const history = () => [
  event(0, 'agent/inbox/spliced', { inserted: [user] }),
  event(1, 'turn/start', { turn: 'turn-1' }),
  event(2, 'user/message', user, 'append'),
  event(3, 'assistant/message', { turn: 'turn-1', message: assistant('assistant-1', [
    { type: 'reasoning', text: 'hidden-reasoning' },
    { type: 'tool-call', id: 'model-id', name: 'query_kline', arguments: '{broken json' },
  ]) }, 'append'),
  event(4, 'tool/result', { turn: 'turn-1', message: { id: 'tool-1', source: { kind: 'tool', callId: 'model-id' },
    content: [{ type: 'tool-result', toolCallId: 'model-id', isError: true, content: [{ type: 'text', text: 'rejected' }] }] } }, 'append'),
  event(5, 'assistant/message', { turn: 'turn-1', message: assistant('assistant-2', [{ type: 'text', text: 'answer' }]) }, 'append'),
  event(6, 'turn/end', { turn: 'turn-1' }),
]
const query = text => parseTranscriptQuery(new URLSearchParams(text))
const projection = {
  isAppendSurfaceEvent: event => event.surfaceOp === 'append',
  deriveEventMessage: event => event.data.message,
}
const page = (params = {}) => paginateTranscript({ sessionId: 'session-1', events: history(), bindings: [binding],
  query: query(''), ...projection, ...params })

test('TEST-TRANSCRIPT-01 strict decimal cursors, bounds and snapshot loss fail explicitly', () => {
  for (const input of ['limit=0', 'limit=101', 'limit=-1', 'limit=1.5', 'limit=01', 'limit=1e2',
    'before_seq=', 'before_seq=-1', 'before_seq=9007199254740992', 'before_seq=Infinity',
    'snapshot_seq=01', 'limit=1&limit=2', 'snapshot_seq=1&snapshot_seq=1', 'unknown=1']) {
    assert.throws(() => query(input), /agent_invalid_transcript_query/, input)
  }
  assert.deepEqual(query('before_seq=0&snapshot_seq=7&limit=1'), { before_seq: 0, snapshot_seq: 7, limit: 1 })
  assert.throws(() => page({ query: query('snapshot_seq=8') }), error => error.status === 409)
  assert.throws(() => page({ query: query('before_seq=8') }), /agent_invalid_transcript_query/)
  assert.deepEqual(page({ query: query('snapshot_seq=0') }).items, [])
})

test('TEST-TRANSCRIPT-02 stable archive IDs, strict payload and no internal metadata', () => {
  const result = page()
  assert.equal(valid(result), true, JSON.stringify(valid.errors))
  assert.deepEqual(result.items.map(row => row.seq), ['0', '3', '4', '5'])
  assert.deepEqual(result.items.map(row => row.role), ['user', 'assistant', 'tool', 'assistant'])
  assert.equal(result.items[1].content[0].arguments, '{broken json')
  assert.equal(result.items[2].content[0].is_error, true)
  for (const hidden of ['hidden-reasoning', 'model-id', 'provider-secret']) assert.ok(!JSON.stringify(result).includes(hidden))
  result.items[0].receipt = 'secret'
  assert.equal(valid(result), false)
  const untouched = page()
  untouched.items[0].content[0].text = 'mutated'
  assert.equal(user.content[0].text, 'query')
})

test('TEST-TRANSCRIPT-03 exclusive paging has no gaps under append, replacements or sparse positions', () => {
  const first = page({ query: query('limit=2') })
  assert.deepEqual(first.items.map(row => row.seq), ['4', '5'])
  assert.equal(first.next_before_seq, '4')
  const events = history()
  events.push(event(7, 'assistant/message', { turn: 'turn-1', message: assistant('summary', [{ type: 'text', text: 'summary' }]) }, { op: 'replace', start: 2, end: 5 }))
  events.push(event(8, 'assistant/message', { turn: 'turn-1', message: assistant('new', [{ type: 'text', text: 'new' }]) }, 'append'))
  const second = page({ events, query: query('limit=2&snapshot_seq=7&before_seq=4') })
  assert.deepEqual([...second.items, ...first.items], page().items)
  assert.equal(second.has_more, false)
  assert.equal(second.next_before_seq, null)
  assert.deepEqual(page({ events, query: query('snapshot_seq=7') }).items, page().items)
  assert.equal(page({ events }).items.length, 5)
  assert.deepEqual(page({ query: query('before_seq=0') }).items, [])
})

test('TEST-TRANSCRIPT-04 admission survives an unclaimed inbox; internal context and empty reasoning stay private', () => {
  assert.equal(page({ events: history().slice(0, 1) }).items[0].message_id, user.id)
  const events = history()
  events.push(event(7, 'user/message', { ...user, id: 'plugin', source: { kind: 'plugin' }, content: [{ type: 'text', text: 'plugin-secret' }] }, 'append'))
  events.push(event(8, 'assistant/message', { turn: 'turn-1', message: assistant('reasoning', [{ type: 'reasoning', text: 'private' }]) }, 'append'))
  events.push(event(9, 'assistant/message', { turn: 'turn-1', message: assistant('image', [{ type: 'image', attachment: { private: 'path' } }]) }, 'append'))
  const result = page({ events })
  assert.equal(result.items.length, 5)
  assert.equal(result.items.at(-1).omitted_content, true)
  assert.deepEqual(result.items.at(-1).content, [])
  assert.equal(valid(result), true, JSON.stringify(valid.errors))
  assert.ok(!JSON.stringify(result).includes('plugin-secret'))
  assert.deepEqual(page({ bindings: [{ ...binding, session_id: 'other' }] }).items, [])
})

test('TEST-TRANSCRIPT-05 response byte bound shortens whole pages without dropping Unicode records', () => {
  const events = history()
  events[5] = event(5, 'assistant/message', { turn: 'turn-1', message: assistant('assistant-2', [{ type: 'text', text: '市场📈'.repeat(200) }]) }, 'append')
  const last = page({ events, query: query('limit=1') })
  const limit = Buffer.byteLength(JSON.stringify(last)) + 50
  const result = page({ events, maxResponseBytes: limit })
  assert.deepEqual(result.items, last.items)
  assert.ok(Buffer.byteLength(JSON.stringify(result)) <= limit)
  assert.equal(result.has_more, true)
  const older = page({ events, query: query(`before_seq=${result.next_before_seq}`), maxResponseBytes: limit })
  assert.deepEqual([...older.items, ...result.items], page({ events }).items)
  assert.throws(() => page({ events, maxResponseBytes: 500 }), error => error.status === 413)
})

test('TEST-TRANSCRIPT-06 read errors are sanitized, invalid queries never read and disconnect aborts', async () => {
  const response = () => Object.assign(new EventEmitter(), {
    destroyed: false,
    writeHead(status, headers) { this.status = status; this.headers = headers },
    end(body) { this.body = body },
  })
  let reads = 0
  const read = async () => { reads++; throw new Error('/secret/path provider-key') }
  const bad = response()
  await serveTranscript({ url: '/?limit=0' }, bad, 'session-1', read, projection)
  assert.equal(bad.status, 400)
  assert.equal(reads, 0)
  const failed = response()
  await serveTranscript({ url: '/' }, failed, 'session-1', read, projection)
  assert.equal(failed.status, 500)
  assert.deepEqual(JSON.parse(failed.body), { error: { code: 'agent_transcript_unavailable' } })
  const closed = response()
  await serveTranscript({ url: '/' }, closed, 'session-1', async (_id, signal) => {
    closed.destroyed = true
    closed.emit('close')
    assert.equal(signal.aborted, true)
    return { events: [], bindings: [] }
  }, projection)
  assert.equal(closed.body, undefined)
  assert.equal(closed.listenerCount('close'), 0)
})
