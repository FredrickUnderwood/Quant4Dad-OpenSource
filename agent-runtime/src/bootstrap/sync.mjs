import { inspect, isDeepStrictEqual } from 'node:util'
import { createRunVerifier, runtimeAudience } from '../auth/run-capability.mjs'
import { BootstrapError, failBootstrap, validateBootstrap } from './validation.mjs'

/** One owner of a complete configuration. An optional applier must prepare and
 * acknowledge a full DSH generation before publication. Neither memory receipt
 * nor DSH application is model probe readiness or production authorization. */
export class BootstrapSync {
  #client
  #authorize
  #applier
  #clock
  #interval
  #maxAge
  #current
  #confirmedAt
  #phase = 'uninitialized'
  #error
  #pending
  #controller = new AbortController()
  #polling = false
  #timer
  #stopped = false
  #invalidations = new Set()
  /** @param {{client: {read: Function}, authorize: Function, applier?: {prepare: Function, invalidate: Function, drain: Function}, pollIntervalMs?: number, maxAgeMs?: number, clock?: () => number}} options */
  constructor({ client, authorize, applier, pollIntervalMs = 5000, maxAgeMs = 15000, clock = () => performance.now() } = {}) {
    if (typeof client?.read !== 'function' || typeof authorize !== 'function' || typeof clock !== 'function' ||
        !Number.isSafeInteger(pollIntervalMs) || pollIntervalMs < 1 || pollIntervalMs > 5000 ||
        !Number.isSafeInteger(maxAgeMs) || maxAgeMs < pollIntervalMs || maxAgeMs > 15000) {
      failBootstrap('agent_bootstrap_configuration_invalid')
    }
    if (applier && ['prepare', 'invalidate', 'drain'].some(method => typeof applier[method] !== 'function')) failBootstrap('agent_bootstrap_configuration_invalid')
    this.#client = client
    this.#authorize = authorize
    this.#applier = applier
    this.#interval = pollIntervalMs
    this.#maxAge = maxAgeMs
    this.#clock = clock
  }
  #invalidate(code) {
    this.#current = undefined
    this.#confirmedAt = undefined
    this.#error = code
    this.#phase = this.#stopped ? 'stopped' : 'unavailable'
    this.#applier?.invalidate()
    for (const listener of this.#invalidations) {
      try { listener() } catch { /* listeners cannot restore configuration authority */ }
    }
  }
  subscribeInvalidation(listener) {
    if (typeof listener !== 'function') failBootstrap('agent_bootstrap_configuration_invalid')
    this.#invalidations.add(listener)
    return () => this.#invalidations.delete(listener)
  }
  #time() {
    let now
    try { now = this.#clock() } catch { /* fixed diagnostic below */ }
    if (!Number.isFinite(now) || now < 0) failBootstrap('agent_bootstrap_unavailable')
    return now
  }
  #expire() {
    if (!this.#current) return
    try {
      const age = this.#time() - this.#confirmedAt
      if (age < 0 || age >= this.#maxAge) this.#invalidate('agent_bootstrap_stale')
    } catch { this.#invalidate('agent_bootstrap_unavailable') }
  }
  #requireCurrent() {
    this.#expire()
    if (!this.#current || this.#stopped) failBootstrap('agent_configuration_unavailable')
    return this.#current
  }
  status() {
    this.#expire()
    return Object.freeze({ phase: this.#phase, configuration_current: Boolean(this.#current),
      ...(this.#applier ? { configuration_applied: Boolean(this.#current) } : {}),
      ...(this.#current ? { revision: this.#current.snapshot.revision,
        model_config_revision: this.#current.snapshot.model_config_revision } : {}),
      ...(this.#error ? { error: this.#error } : {}) })
  }
  toJSON() { return this.status() }
  [inspect.custom]() { return this.status() }

  // Trusted adapter access only: the immutable return value contains credentials.
  // Do not log/persist it or use a saved reference as proof of current authority.
  readConfiguration() { return this.#requireCurrent().snapshot }

  refresh() {
    if (this.#stopped) return Promise.reject(new BootstrapError('agent_bootstrap_stopped'))
    if (this.#pending) return this.#pending
    this.#expire()
    const known = this.#current?.snapshot.revision ?? ''
    this.#pending = (async () => {
      let staged
      try {
        const result = await this.#client.read(known, { signal: this.#controller.signal })
        if (this.#stopped) failBootstrap('agent_bootstrap_stopped')
        const now = this.#time()
        let candidate
        if (result.unchanged === true) {
          if (!known || this.#current?.snapshot.revision !== known || result.revision !== known) failBootstrap()
          candidate = this.#current
        } else {
          const snapshot = validateBootstrap(result.snapshot)
          if (this.#current && ((snapshot.revision === known && !isDeepStrictEqual(snapshot, this.#current.snapshot)) ||
              (snapshot.model_config_revision === this.#current.snapshot.model_config_revision &&
                !isDeepStrictEqual(snapshot.providers, this.#current.snapshot.providers)))) failBootstrap()
          candidate = { snapshot }
          candidate.verifier = createRunVerifier({ issuer: snapshot.capability.issuer, audience: runtimeAudience,
            keys: snapshot.capability.public_keys, authorize: claims => {
              const current = this.#requireCurrent()
              if (current !== candidate || claims.envelope.model_config_revision !== snapshot.model_config_revision ||
                  !snapshot.providers.some(p => p.id === claims.envelope.provider && p.default_model === claims.envelope.model)) return false
              return this.#authorize(claims)
            } })
          if (this.#applier) {
            if (this.#current?.snapshot.revision === snapshot.revision) candidate = this.#current
            else {
              this.#invalidate('agent_configuration_unavailable')
              this.#phase = 'applying'
              staged = await this.#applier.prepare(snapshot, { signal: this.#controller.signal })
              if (this.#stopped) failBootstrap('agent_bootstrap_stopped')
              if (staged?.revision !== snapshot.revision || staged?.model_config_revision !== snapshot.model_config_revision ||
                  typeof staged.commit !== 'function' || typeof staged.discard !== 'function') failBootstrap('agent_bootstrap_apply_failed')
              if (this.#time() - now >= this.#maxAge || this.#time() < now) failBootstrap('agent_bootstrap_stale')
              staged.commit()
            }
          }
        }
        // There is no await between complete preparation and publication. A
        // reader sees all providers/MCP/keys/revisions from exactly one response.
        this.#current = candidate
        this.#confirmedAt = now
        this.#phase = 'current'
        this.#error = undefined
        return this.status()
      } catch (error) {
        const code = new BootstrapError(this.#stopped ? 'agent_bootstrap_stopped'
          : error instanceof BootstrapError ? error.code : 'agent_bootstrap_unavailable').code
        this.#invalidate(code)
        try { await staged?.discard?.() } catch { /* gate is already closed */ }
        throw new BootstrapError(code)
      }
    })().finally(() => { this.#pending = undefined })
    return this.#pending
  }

  verifyPrompt(request, now = Date.now()) {
    const current = this.#requireCurrent()
    const run = current.verifier.verifyPrompt(request, now)
    const binding = Object.freeze({ session_id: run.sessionId, run_id: run.runId,
      client_request_id: run.claims.envelope.client_request_id, request_hash: run.claims.request_hash,
      execution_envelope_digest: run.claims.execution_envelope_digest })
    // Never return the old verifier's captured-key closure. Each subsequent
    // Tool check consults the current keys, model revision and live policy.
    // A transport rotation also invalidates an existing Gateway context.
    const mcp = current.snapshot.mcp
    return Object.freeze({ ...run, authorize: () => {
      const active = this.#requireCurrent()
      if (active.snapshot.mcp.url !== mcp.url || active.snapshot.mcp.runtime_token !== mcp.runtime_token) {
        failBootstrap('agent_capability_rejected')
      }
      active.verifier.verify(run.capability, binding)
      return true
    } })
  }

  start() {
    if (this.#stopped) failBootstrap('agent_bootstrap_stopped')
    if (this.#polling) return
    this.#polling = true
    const poll = async () => {
      try { await this.refresh() } catch { /* status exposes only fixed codes */ }
      if (!this.#stopped) this.#timer = setTimeout(poll, this.#interval)
    }
    void poll()
  }
  async stop() {
    this.#stopped = true
    this.#polling = false
    clearTimeout(this.#timer)
    this.#invalidate('agent_bootstrap_stopped')
    this.#controller.abort()
    try { await this.#pending } catch { /* cancellation is expected */ }
    await this.#applier?.drain()
  }
}
