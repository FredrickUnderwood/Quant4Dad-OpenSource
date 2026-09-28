import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { EventJournal } from '../../../../src/persistence/event-journal.mjs'
import { serveEventStream } from '../../../../src/bridge/event-stream.mjs'

// A Writable-shaped slow consumer: never drains unless the test asks it to.
class Response extends EventEmitter {
  writableLength = 0
  writableEnded = false
  destroyed = false
  body = ''
  writeHead(status, headers) { this.status = status; this.headers = headers }
  flushHeaders() {}
  write(chunk) { this.body += chunk; this.writableLength += Buffer.byteLength(chunk); return false }
  end() { this.writableEnded = true; this.emit('close') }
  destroy() { this.destroyed = true; this.emit('close') }
}
function setup(t) {
  const directory = mkdtempSync(join(tmpdir(), 'q4d-sse-'))
  const journal = new EventJournal(join(directory, 'events.jsonl'), 'session-1', 'run-1')
  t.after(() => { journal.close(); rmSync(directory, { recursive: true, force: true }) })
  return journal
}
const event = type => ({ type, occurred_at: '2026-09-09T00:00:00.000Z', data: ({
  'run.started': { message_id: 'message-1', execution_envelope_digest: 'sha256:' + 'a'.repeat(64) },
  'message.completed': { message_id: 'answer-1', text: 'answer' },
  'run.cancelled': { reason: 'user' },
})[type] ?? {} })

test('TEST-SSE-01 replay and subscribe share one cursor; terminal closes the stream', t => {
  const journal = setup(t)
  journal.append('1', event('run.started'))
  journal.append('2', event('message.completed'))
  const res = new Response()
  serveEventStream({ headers: { 'last-event-id': '1' } }, res, journal)
  assert.equal(res.status, 200)
  assert.equal(res.writableEnded, false)
  journal.append('2', event('message.completed'))
  journal.append('3', event('run.completed'))
  assert.deepEqual([...res.body.matchAll(/^id: (\d+)$/gm)].map(match => match[1]), ['2', '3'])
  assert.equal(res.writableEnded, true)
})

test('TEST-SSE-02 slow/disconnected reader is detached; committed events remain replayable', t => {
  const journal = setup(t)
  const res = new Response()
  serveEventStream({ headers: {} }, res, journal, { maxPendingBytes: 1 })
  journal.append('1', event('run.started'))
  assert.equal(res.destroyed, true)
  const buffered = res.body
  journal.append('2', event('message.completed'))
  assert.equal(res.body, buffered)
  assert.equal(journal.replay().length, 2)
  const expired = new Response()
  journal.pruneThrough('1')
  serveEventStream({ headers: {} }, expired, journal)
  assert.equal(expired.status, 410)
  assert.equal(expired.writableEnded, true)
})
