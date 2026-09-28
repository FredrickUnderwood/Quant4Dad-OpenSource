/** Candidate Bridge v1 archive projection. DSH's public surface helpers are
 * injected by the adapter; this module has no dependency on a private log. */
export class TranscriptError extends Error {
  constructor(code, status = 400) { super(code); this.status = status }
}

export function parseTranscriptQuery(params) {
  const query = {}
  for (const [key, value] of params) {
    if (!['before_seq', 'snapshot_seq', 'limit'].includes(key) || Object.hasOwn(query, key) ||
        !/^(0|[1-9][0-9]{0,15})$/.test(value) || !Number.isSafeInteger(Number(value))) {
      throw new TranscriptError('agent_invalid_transcript_query')
    }
    query[key] = Number(value)
  }
  query.limit ??= 100
  if (query.limit < 1 || query.limit > 100) throw new TranscriptError('agent_invalid_transcript_query')
  return Object.freeze(query)
}

function blocksFor(message) {
  let omitted = false
  const content = []
  for (const block of message.content) {
    if (block.type === 'text') content.push({ type: 'text', text: block.text })
    else if (block.type === 'tool-call') {
      // An archival model request, never an authorized Tool operation. Even
      // invalid JSON remains display text. Provider call IDs are not exported.
      content.push({ type: 'tool_request', name: block.name, arguments: block.arguments })
    } else if (block.type === 'tool-result') {
      const text = block.content.filter(child => child.type === 'text').map(child => ({ type: 'text', text: child.text }))
      omitted ||= block.content.some(child => !['text', 'reasoning'].includes(child.type))
      content.push({ type: 'tool_result', is_error: block.isError === true, content: text })
    } else if (block.type !== 'reasoning') omitted = true
  }
  return { content, omitted_content: omitted }
}

export function paginateTranscript({ sessionId, events, bindings, query,
  isAppendSurfaceEvent, deriveEventMessage, maxResponseBytes = 1024 * 1024 }) {
  const end = events.at(-1)?.seq + 1 || 0
  const snapshot = query.snapshot_seq ?? end
  const before = query.before_seq ?? snapshot
  if (snapshot > end) throw new TranscriptError('agent_transcript_snapshot_unavailable', 409)
  if (before > snapshot) throw new TranscriptError('agent_invalid_transcript_query')
  const byMessage = new Map(bindings.filter(binding => binding.session_id === sessionId)
    .map(binding => [binding.message_id, binding]))
  const admitted = new Set()
  const turns = new Map()
  let turn
  const items = []
  const add = (event, message, binding, role) => {
    const projected = blocksFor(message)
    if (!projected.content.length && !projected.omitted_content) return
    items.push({ seq: String(event.seq), message_id: message.id, run_id: binding.run_id,
      role, occurred_at: new Date(event.time).toISOString(), ...projected })
  }
  for (const event of events) {
    if (event.seq >= snapshot || event.seq >= before) break
    if (event.type === 'turn/start') turn = event.data.turn
    if (event.type === 'agent/inbox/spliced') {
      const inserted = event.data.inserted.filter(message => byMessage.has(message.id) && !admitted.has(message.id))
      // P0 admits exactly one message per splice. Reject rather than assign
      // duplicate seq cursors if a future composition starts batching prompts.
      if (inserted.length > 1) throw new TranscriptError('agent_transcript_batch_unsupported', 500)
      for (const message of inserted) {
        admitted.add(message.id)
        add(event, message, byMessage.get(message.id), 'user')
      }
    } else if (event.type === 'user/message' && isAppendSurfaceEvent(event)) {
      const binding = byMessage.get(event.data.id)
      if (binding && admitted.has(binding.message_id)) turns.set(turn, binding)
    } else if (['assistant/message', 'tool/result'].includes(event.type) && isAppendSurfaceEvent(event)) {
      const binding = turns.get(event.data.turn)
      const message = deriveEventMessage(event)
      if (binding && message) add(event, message, binding, event.type === 'tool/result' ? 'tool' : 'assistant')
    }
  }
  const page = { session_id: sessionId, snapshot_seq: String(snapshot), items: [], has_more: false, next_before_seq: null }
  // Reserve enough bytes for the envelope and the longest possible cursor.
  const overhead = Buffer.byteLength(JSON.stringify({ ...page, has_more: false, next_before_seq: '9007199254740991' }))
  let bytes = overhead
  for (let i = items.length - 1; i >= 0; i--) {
    const size = Buffer.byteLength(JSON.stringify(items[i])) + (page.items.length ? 1 : 0)
    if (page.items.length >= query.limit || bytes + size > maxResponseBytes) {
      if (!page.items.length) throw new TranscriptError('agent_transcript_item_too_large', 413)
      page.has_more = true
      page.next_before_seq = page.items[0].seq
      break
    }
    page.items.unshift(items[i])
    bytes += size
  }
  return page
}

export async function serveTranscript(req, res, sessionId, read, projection) {
  const abort = new AbortController()
  const closed = () => abort.abort()
  res.once('close', closed)
  try {
    // Validate before reading from persistence; caller already authenticated.
    const query = parseTranscriptQuery(new URL(req.url, 'http://bridge.invalid').searchParams)
    const { events, bindings } = await read(sessionId, abort.signal)
    const page = paginateTranscript({ sessionId, events, bindings, query, ...projection })
    if (res.destroyed) return
    res.writeHead(200, { 'content-type': 'application/json', 'cache-control': 'no-store' })
    res.end(JSON.stringify(page))
  } catch (error) {
    if (res.destroyed) return
    const known = error instanceof TranscriptError
    res.writeHead(known ? error.status : 500, { 'content-type': 'application/json', 'cache-control': 'no-store' })
    res.end(JSON.stringify({ error: { code: known ? error.message : 'agent_transcript_unavailable' } }))
  } finally { res.off('close', closed) }
}
