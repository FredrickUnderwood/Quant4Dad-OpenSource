import { DurableLog } from './durable-log.mjs'

const fields = ['session_id', 'dsh_session_id', 'run_id', 'client_request_id', 'request_hash', 'message_id', 'execution_envelope_digest']

/** Maps a pre-created DSH message identity to a Run, before inbox insertion. */
export class RequestIndex {
  #log
  #runs = new Map()
  #clients = new Map()

  constructor(path) {
    this.#log = new DurableLog(path)
    try {
      for (const record of this.#log.records) this.#apply(record)
    } catch (error) { this.#log.close(); throw error }
  }

  #apply(record) {
    if (Object.keys(record).length !== fields.length || fields.some(key => typeof record[key] !== 'string' || !record[key])) {
      throw new Error('agent_request_index_corrupt')
    }
    const client = JSON.stringify([record.session_id, record.client_request_id])
    if (this.#runs.has(record.run_id) || this.#clients.has(client)) throw new Error('agent_request_index_corrupt')
    this.#runs.set(record.run_id, record)
    this.#clients.set(client, record.run_id)
  }

  get(runId) { return structuredClone(this.#runs.get(runId)) }
  list() { return structuredClone([...this.#runs.values()]) }

  prepare(record) {
    const existing = this.#runs.get(record.run_id)
    if (existing) {
      if (fields.filter(key => key !== 'message_id').some(key => existing[key] !== record[key])) {
        throw new Error('agent_request_conflict')
      }
      return structuredClone(existing)
    }
    if (this.#clients.has(JSON.stringify([record.session_id, record.client_request_id]))) throw new Error('agent_request_conflict')
    if (Object.keys(record).length !== fields.length || fields.some(key => typeof record[key] !== 'string' || !record[key])) {
      throw new Error('agent_invalid_request_binding')
    }
    const committed = this.#log.append(record)
    this.#apply(committed)
    return structuredClone(committed)
  }

  close() { this.#log.close() }
}
