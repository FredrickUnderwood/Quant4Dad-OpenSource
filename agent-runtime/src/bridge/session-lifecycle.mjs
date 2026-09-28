import { sessionCreated } from '../persistence/session-bindings.mjs'

export const sessionPreset = binding => `q4d-bridge-v1:${binding.request.provision_request_hash}`

/** One process owns this controller and its generation. Serialize lifecycle and
 * durable admission per Session, never hold a lock for a complete model Run. */
export class SessionLifecycle {
  #bindings
  #backend
  #provenance
  #tails = new Map()
  constructor({ bindings, backend, provenance }) {
    this.#bindings = bindings
    this.#backend = backend
    this.#provenance = structuredClone(provenance)
  }
  serialized(id, operation) {
    const previous = this.#tails.get(id) ?? Promise.resolve()
    const result = previous.then(operation)
    const tail = result.catch(() => {}).finally(() => { if (this.#tails.get(id) === tail) this.#tails.delete(id) })
    this.#tails.set(id, tail)
    return result
  }
  #ownedHeader(binding, header) {
    if (header?.id !== binding.request.q4d_session_id || header.agentPreset !== sessionPreset(binding)) {
      throw new Error('agent_session_unbound')
    }
  }
  #ready(id) {
    const binding = this.#bindings.get(id)
    if (!binding) throw new Error('agent_session_not_found')
    if (!binding.durable) throw new Error('agent_session_provisioning')
    return binding
  }
  provision(request) {
    // Snapshot before waiting: callers cannot mutate a queued request/hash.
    request = structuredClone(request)
    return this.serialized(request.q4d_session_id, async () => {
      let binding = this.#bindings.match(request)
      let stored = await this.#backend.readHeader(request.q4d_session_id)
      const live = this.#backend.liveHeader(request.q4d_session_id)
      if (!binding) {
        if (stored || live) throw new Error('agent_session_unbound')
        await this.#backend.resolve(request, true)
        binding = this.#bindings.prepare(request, this.#provenance)
        await this.#backend.checkpoint?.('after_session_intent')
      }
      if (stored) this.#ownedHeader(binding, stored)
      if (live) this.#ownedHeader(binding, live)
      if (!stored) {
        if (binding.durable) throw new Error('agent_session_storage_missing')
        const config = await this.#backend.resolve(binding.request, true)
        await this.#backend.materialize(binding, config)
        stored = await this.#backend.readHeader(request.q4d_session_id)
        this.#ownedHeader(binding, stored)
        await this.#backend.checkpoint?.('after_session_materialized')
      }
      binding = this.#bindings.materialized(request.q4d_session_id)
      await this.#backend.checkpoint?.('after_session_commit')
      // Retrying creation on a cold Session does not resume or execute it.
      return sessionCreated(binding)
    })
  }
  withActive(id, authorize, operation) {
    return this.serialized(id, async () => {
      const binding = this.#ready(id)
      const config = await this.#backend.resolve(binding.request, false)
      await authorize(config)
      const live = this.#backend.liveHeader(id)
      if (live) this.#ownedHeader(binding, live)
      else {
        const stored = await this.#backend.readHeader(id)
        if (!stored) throw new Error('agent_session_storage_missing')
        this.#ownedHeader(binding, stored)
        await this.#backend.resume(binding, config)
        this.#ownedHeader(binding, this.#backend.liveHeader(id))
      }
      return operation()
    })
  }
  close(id) {
    return this.serialized(id, async () => {
      const binding = this.#ready(id)
      if (this.#backend.isBusy(id)) throw new Error('agent_run_in_progress')
      const stored = await this.#backend.readHeader(id)
      if (!stored) throw new Error('agent_session_storage_missing')
      this.#ownedHeader(binding, stored)
      const live = this.#backend.liveHeader(id)
      if (live) {
        this.#ownedHeader(binding, live)
        await this.#backend.unload(id)
      }
      await this.#backend.checkpoint?.('after_session_close')
      return { session_id: id, dsh_session_id: id, durable: true, loaded: false }
    })
  }
}
