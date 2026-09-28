import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'
import { DurableLog } from './durable-log.mjs'

const ajv = new Ajv2020({ strict: true })
const readSchema = name => JSON.parse(readFileSync(new URL(`../../contracts/bridge-v1/${name}.schema.json`, import.meta.url)))
export const validSessionCreate = ajv.compile(readSchema('session-create'))
export const validSessionCreated = ajv.compile(readSchema('session-created'))
const fields = ['model', 'model_config_revision', 'profile', 'profile_revision', 'provider']

// Versioned flat string-only creation-parameter domain, sorted keys. Session ID
// is the separate idempotency key; Go may hash parameters before allocating it.
// This is not a general-purpose canonical JSON implementation for Tool inputs.
export function sessionProvisionHash(request) {
  const canonical = JSON.stringify(Object.fromEntries(fields.map(key => [key, request[key]])))
  return 'sha256:' + createHash('sha256').update(canonical).digest('hex')
}

export function checkSessionRequest(request) {
  if (!validSessionCreate(request)) throw new Error('agent_invalid_request')
  if (sessionProvisionHash(request) !== request.provision_request_hash) throw new Error('agent_session_conflict')
}

export function sessionCreated(binding) {
  const request = binding.request
  return { session_id: request.q4d_session_id, dsh_session_id: request.q4d_session_id, durable: true,
    provider: request.provider, model: request.model, profile: request.profile,
    created_profile_revision: request.profile_revision, created_model_config_revision: request.model_config_revision,
    provision_request_hash: request.provision_request_hash, runtime_provenance: structuredClone(binding.runtime_provenance) }
}

/** Only provisioning intent and immutable creation provenance. Product metadata
 * stays in Go; conversation and Run facts stay in DSH/the event journal. */
export class SessionBindings {
  #log
  #sessions = new Map()
  constructor(path) {
    this.#log = new DurableLog(path)
    try { for (const row of this.#log.records) this.#apply(row) }
    catch { this.#log.close(); throw new Error('agent_session_index_corrupt') }
  }
  #apply(row) {
    if (row.kind === 'intent' && Object.keys(row).length === 2) {
      const binding = row.binding
      if (!binding || Object.keys(binding).length !== 2 || !binding.runtime_provenance) throw new Error()
      checkSessionRequest(binding.request)
      if (!validSessionCreated(sessionCreated(binding)) || this.#sessions.has(binding.request.q4d_session_id)) throw new Error()
      this.#sessions.set(binding.request.q4d_session_id, { ...binding, durable: false })
    } else if (row.kind === 'materialized' && Object.keys(row).length === 2) {
      const binding = this.#sessions.get(row.session_id)
      if (!binding || binding.durable) throw new Error()
      binding.durable = true
    } else throw new Error()
  }
  get(id) { return structuredClone(this.#sessions.get(id)) }
  match(request) {
    checkSessionRequest(request)
    const previous = this.get(request.q4d_session_id)
    if (previous && [...fields, 'provision_request_hash'].some(key => previous.request[key] !== request[key])) {
      throw new Error('agent_session_conflict')
    }
    return previous
  }
  prepare(request, runtimeProvenance) {
    const previous = this.match(request)
    if (previous) return previous
    const binding = { request: structuredClone(request), runtime_provenance: structuredClone(runtimeProvenance) }
    if (!validSessionCreated(sessionCreated(binding))) throw new Error('agent_invalid_session_provenance')
    this.#apply(this.#log.append({ kind: 'intent', binding }))
    return this.get(request.q4d_session_id)
  }
  materialized(id) {
    const binding = this.get(id)
    if (!binding) throw new Error('agent_session_not_found')
    if (!binding.durable) this.#apply(this.#log.append({ kind: 'materialized', session_id: id }))
    return this.get(id)
  }
  close() { this.#log.close() }
}
