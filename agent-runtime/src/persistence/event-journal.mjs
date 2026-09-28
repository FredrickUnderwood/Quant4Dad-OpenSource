import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'
import addFormats from 'ajv-formats'
import { DurableLog } from './durable-log.mjs'

const ajv = new Ajv2020({ strict: true })
addFormats(ajv)
ajv.addSchema(JSON.parse(readFileSync(new URL('../../contracts/bridge-v1/tool-event.schema.json', import.meta.url))))
const validEvent = ajv.compile(JSON.parse(readFileSync(new URL('../../contracts/bridge-v1/event.schema.json', import.meta.url))))
const cursorPattern = /^(0|[1-9][0-9]*)$/

/**
 * A durable projection of flushed DSH facts and adapter-owned Tool dispatch
 * observations for one Run. Persist the logical proposal before dispatch;
 * its identity cannot be reconstructed from the model's Tool callId.
 */
export class EventJournal {
  #log
  #sessionId
  #runId
  #events = []
  #sources = new Map()
  #prunedThrough = 0n
  #listeners = new Set()
  #terminal
  #tools = new Map()
  #proposals = new Map()
  #started = false
  #completedMessages = new Set()

  get terminal() { return this.#terminal }
  snapshot() { return structuredClone(this.#events) }
  subscribe(listener) {
    this.#listeners.add(listener)
    return () => this.#listeners.delete(listener)
  }

  constructor(path, sessionId, runId) {
    this.#sessionId = sessionId
    this.#runId = runId
    this.#log = new DurableLog(path)
    try { for (const row of this.#log.records) this.#apply(row) }
    catch (error) { this.#log.close(); throw error }
  }

  #apply(row) {
    if (row.kind === 'prune') {
      if (typeof row.through !== 'string' || !cursorPattern.test(row.through) ||
          BigInt(row.through) < this.#prunedThrough || BigInt(row.through) > BigInt(this.#events.length)) {
        throw new Error('agent_journal_corrupt')
      }
      this.#prunedThrough = BigInt(row.through)
      return
    }
    const event = row.event
    if (this.#terminal || row.kind !== 'event' || !row.sourceKey || this.#sources.has(row.sourceKey) || !validEvent(event) ||
        event.run_id !== this.#runId || event.session_id !== this.#sessionId || event.id !== String(this.#events.length + 1)) {
      throw new Error('agent_journal_corrupt')
    }
    this.#checkTransition(event)
    this.#sources.set(row.sourceKey, event)
    this.#events.push(event)
    if (event.type === 'run.started') this.#started = true
    if (event.type === 'message.completed') this.#completedMessages.add(event.data.message_id)
    if (event.type === 'tool.proposed') this.#proposals.set(event.data.tool_call_id, event.data)
    if (event.type.startsWith('tool.') || event.type === 'approval.required') this.#tools.set(event.data.tool_call_id, event.type)
    if (/^run\.(completed|failed|interrupted|cancelled)$/.test(event.type)) this.#terminal = event.type
  }

  #checkTransition(event) {
    if (event.type === 'run.started' ? this.#started : !this.#started) throw new Error('agent_invalid_run_transition')
    if (['message.delta', 'message.completed'].includes(event.type) && this.#completedMessages.has(event.data.message_id)) {
      throw new Error('agent_message_terminal')
    }
    if (event.type === 'tool.proposed' && (event.data.idempotency_key !== `q4d:${this.#runId}:${event.data.tool_call_id}` ||
        [...this.#proposals.values()].some(value => value.source_seq === event.data.source_seq))) {
      throw new Error('agent_tool_identity_conflict')
    }
    const proposal = this.#proposals.get(event.data.tool_call_id)
    if (proposal?.arguments_omitted && ['approval.required', 'tool.started', 'tool.completed'].includes(event.type)) {
      throw new Error('agent_rejected_tool_transition')
    }
    if (event.type === 'approval.required' && proposal?.name !== event.data.name) throw new Error('agent_tool_identity_conflict')
    const previous = this.#tools.get(event.data.tool_call_id)
    const allowed = {
      'tool.proposed': [undefined],
      'approval.required': ['tool.proposed'],
      'tool.started': ['tool.proposed', 'approval.required'],
      'tool.completed': ['tool.started'],
      'tool.failed': ['tool.proposed', 'approval.required', 'tool.started'],
    }[event.type]
    if (allowed && !allowed.includes(previous)) throw new Error('agent_invalid_tool_transition')
    if (/^run\.(completed|failed|interrupted|cancelled)$/.test(event.type) &&
        [...this.#tools.values()].some(type => !['tool.completed', 'tool.failed'].includes(type))) {
      throw new Error('agent_unsettled_tool')
    }
  }

  append(sourceKey, { type, occurred_at, data }) {
    if (typeof sourceKey !== 'string' || !sourceKey) throw new Error('agent_invalid_event')
    const previous = this.#sources.get(sourceKey)
    if (previous) {
      if (JSON.stringify([type, occurred_at, data]) !== JSON.stringify([previous.type, previous.occurred_at, previous.data])) {
        throw new Error('agent_event_projection_conflict')
      }
      return structuredClone(previous)
    }
    if (this.terminal) throw new Error('agent_run_terminal')
    const event = { id: String(this.#events.length + 1), run_id: this.#runId, session_id: this.#sessionId,
      type, occurred_at, schema_version: 1, data }
    if (!validEvent(event)) throw new Error('agent_invalid_event')
    this.#checkTransition(event)
    const row = this.#log.append({ kind: 'event', sourceKey, event })
    this.#apply(row)
    // The committed log is authoritative; a disconnected or failing consumer
    // must never turn an acknowledged append into an apparent write failure.
    for (const listener of this.#listeners) {
      try { listener(structuredClone(event)) } catch { this.#listeners.delete(listener) }
    }
    return structuredClone(event)
  }

  replay(lastEventId = '0') {
    if (typeof lastEventId !== 'string' || !cursorPattern.test(lastEventId)) throw new Error('agent_invalid_event_cursor')
    const cursor = BigInt(lastEventId)
    if (cursor < this.#prunedThrough) throw new Error('agent_event_cursor_expired')
    if (cursor > BigInt(this.#events.length)) throw new Error('agent_invalid_event_cursor')
    return structuredClone(this.#events.filter(event => BigInt(event.id) > cursor))
  }

  // T-00 retention semantics only. Physical journal rotation belongs to T-11.
  pruneThrough(lastEventId) {
    this.replay(lastEventId)
    this.#apply(this.#log.append({ kind: 'prune', through: lastEventId }))
  }

  close() { this.#listeners.clear(); this.#log.close() }
}
