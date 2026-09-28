import { inspect, isDeepStrictEqual } from 'node:util'
import { setImmediate as yieldToIO } from 'node:timers/promises'
import { deriveDshConfiguration } from './derive.mjs'
import { BootstrapError, failBootstrap } from './validation.mjs'

/** Owns isolated DSH generations. prepare() may await IO/plugin registration;
 * commit() only swaps memory. The public facade checks the control-plane gate
 * at model dispatch and before releasing every chunk, never exposes raw ctx. */
export class DshBootstrapApplier {
  #store
  #factory
  #currentStatus
  #active
  #pending = false
  #cleanup = new Set()
  #cleanupFailed = false
  constructor({ store, createGeneration, currentStatus }) {
    if (typeof store?.write !== 'function' || typeof createGeneration !== 'function' || typeof currentStatus !== 'function') failBootstrap('agent_bootstrap_configuration_invalid')
    this.#store = store
    this.#factory = createGeneration
    this.#currentStatus = currentStatus
  }
  toJSON() { return { type: 'DshBootstrapApplier', active: Boolean(this.#active), cleanup_failed: this.#cleanupFailed } }
  [inspect.custom]() { return this.toJSON() }
  #dispose(generation) {
    if (!generation) return
    generation.abort.abort()
    const cleanup = (async () => {
      try { await generation.backend.close() } finally { await generation.files.remove() }
    })().catch(() => { this.#cleanupFailed = true }).finally(() => this.#cleanup.delete(cleanup))
    this.#cleanup.add(cleanup)
  }
  invalidate() {
    const old = this.#active
    this.#active = undefined
    this.#dispose(old)
  }
  async drain() { await Promise.all([...this.#cleanup]) }

  async prepare(snapshot, { signal } = {}) {
    if (this.#pending) failBootstrap('agent_bootstrap_apply_failed')
    this.#pending = true
    let files, backend
    try {
      await this.drain()
      if (this.#cleanupFailed) failBootstrap('agent_bootstrap_storage_failed')
      signal?.throwIfAborted()
      const derived = deriveDshConfiguration(snapshot)
      files = await this.#store.write(derived, { signal })
      backend = await this.#factory({ ...files, signal })
      const expected = snapshot.providers.map(p => ({ provider: derived.routes[p.id], model: p.default_model,
        context_window: p.agent.context_window, max_output_tokens: p.agent.max_output_tokens }))
      const actual = await backend.describe()
      const order = (a, b) => a.provider < b.provider ? -1 : a.provider > b.provider ? 1 : a.model.localeCompare(b.model)
      if (!isDeepStrictEqual([...actual].sort(order), expected.sort(order))) failBootstrap('agent_bootstrap_apply_failed')
      signal?.throwIfAborted()
      const generation = { files, backend, routes: derived.routes, snapshot, abort: new AbortController() }
      let settled = false
      return Object.freeze({ revision: snapshot.revision, model_config_revision: snapshot.model_config_revision,
        commit: () => {
          if (settled || signal?.aborted) failBootstrap('agent_bootstrap_apply_failed')
          settled = true
          this.invalidate()
          this.#active = generation
        },
        discard: async () => {
          if (settled) return
          settled = true
          this.#dispose(generation)
          await this.drain()
        } })
    } catch {
      if (backend) await backend.close().catch(() => {})
      if (files) await files.remove().catch(() => { this.#cleanupFailed = true })
      failBootstrap('agent_bootstrap_apply_failed')
    } finally { this.#pending = false }
  }
  #requireCurrent(generation = this.#active) {
    const status = this.#currentStatus()
    if (!generation || generation !== this.#active || generation.abort.signal.aborted ||
        status.configuration_current !== true || status.configuration_applied !== true ||
        status.revision !== generation.snapshot.revision) failBootstrap('agent_configuration_unavailable')
    return generation
  }
  async *stream(request, authorize) {
    const generation = this.#requireCurrent()
    const provider = generation.snapshot.providers.find(p => p.id === request.provider && p.default_model === request.model)
    if (!provider || typeof authorize !== 'function') failBootstrap('agent_capability_rejected')
    const check = () => {
      this.#requireCurrent(generation)
      if (request.signal?.aborted) failBootstrap('agent_capability_rejected')
      let allowed
      try { allowed = authorize() } catch { failBootstrap('agent_capability_rejected') }
      if (allowed !== true) {
        if (allowed instanceof Promise) allowed.catch(() => {})
        failBootstrap('agent_capability_rejected')
      }
      this.#requireCurrent(generation)
    }
    check()
    const maxTokens = request.maxTokens ?? provider.agent.max_output_tokens
    if (!Number.isSafeInteger(maxTokens) || maxTokens < 1 || maxTokens > provider.agent.max_output_tokens) failBootstrap('agent_capability_rejected')
    const signal = AbortSignal.any([generation.abort.signal, ...(request.signal ? [request.signal] : [])])
    try {
      let chunksSinceYield = 0, lastYield = performance.now()
      for await (const chunk of generation.backend.stream({ ...request, maxTokens, provider: generation.routes[provider.id], signal })) {
        // Buffered async iterators can drain entirely through microtasks. Bound
        // that work so policy refreshes, cancellation and sockets can progress.
        if (++chunksSinceYield >= 32 || performance.now() - lastYield >= 8) {
          await yieldToIO()
          chunksSinceYield = 0; lastYield = performance.now()
        }
        // Authority/generation/abort may change while yielded: validate before
        // releasing this chunk, never reuse a pre-yield authorization decision.
        check()
        // Provider error bodies may contain request credentials or internals.
        if (chunk.type === 'finish' && ['error', 'aborted'].includes(chunk.reason?.kind)) {
          if (chunk.reason.kind === 'error' && chunk.reason.failure?.code === 'CONTEXT_WINDOW_EXCEEDED') {
            yield { type: 'finish', reason: { kind: 'error', failure: {
              code: 'CONTEXT_WINDOW_EXCEEDED', message: 'agent_context_window_exceeded' } } }
            return
          }
          failBootstrap('agent_model_request_failed')
        }
        yield chunk
      }
      check()
    } catch (error) {
      if (generation.abort.signal.aborted) failBootstrap('agent_configuration_unavailable')
      if (error?.code === 'CONTEXT_WINDOW_EXCEEDED') {
        check()
        yield { type: 'finish', reason: { kind: 'error', failure: {
          code: 'CONTEXT_WINDOW_EXCEEDED', message: 'agent_context_window_exceeded' } } }
        return
      }
      if (error instanceof BootstrapError) throw new BootstrapError(error.code)
      failBootstrap('agent_model_request_failed')
    }
  }
}
