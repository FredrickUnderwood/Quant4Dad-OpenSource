import { sendJSON } from './http-errors.mjs'

const terminal = type => /^run\.(completed|failed|interrupted|cancelled)$/.test(type)

/** Replay and attach in one synchronous turn: no gap between history and live
 * events. Slow readers disconnect and can recover from their last durable ID. */
export function serveEventStream(req, res, journal, { live = true, maxPendingBytes = 256 * 1024 } = {}) {
  let history
  try { history = journal.replay(req.headers['last-event-id'] ?? '0') }
  catch (error) {
    const expired = error.message === 'agent_event_cursor_expired'
    sendJSON(res, expired ? 410 : 400, { error: { code: expired ? 'agent_event_cursor_expired' : 'agent_invalid_event_cursor' } })
    return
  }
  let unsubscribe = () => {}
  let heartbeat
  const cleanup = () => { unsubscribe(); clearInterval(heartbeat) }
  res.once('close', cleanup)
  res.once('error', cleanup)
  res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-store', 'x-accel-buffering': 'no' })
  res.flushHeaders()
  const write = event => {
    if (res.destroyed || res.writableEnded) { cleanup(); return }
    res.write(`id: ${event.id}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
    if (res.writableLength > maxPendingBytes) { cleanup(); res.destroy(); return }
    if (terminal(event.type)) { cleanup(); res.end() }
  }
  for (const event of history) write(event)
  if (!live || journal.terminal || res.destroyed) { res.end(); cleanup(); return }
  unsubscribe = journal.subscribe(write)
  // Transport keepalives are comments, not new durable domain events/IDs.
  heartbeat = setInterval(() => {
    res.write(': keepalive\n\n')
    if (res.writableLength > maxPendingBytes) { cleanup(); res.destroy() }
  }, 15_000)
  heartbeat.unref()
}
