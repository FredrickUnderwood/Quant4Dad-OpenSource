import { inspect } from 'node:util'

const failure = code => { throw new Error(code) }
const codes = new Set(['agent_configuration_unavailable', 'agent_capability_rejected', 'agent_capability_expired',
  'agent_run_budget_exceeded', 'agent_model_request_failed', 'agent_runtime_interrupted', 'agent_input_measurement_unavailable'])

/** Process-local execution authority. Durable stop recording belongs to the
 * host; secrets and verifier closures must never enter its journal. */
export class RunSupervisor {
  #status
  #active = new Map()
  #closed = false
  #timer
  constructor({ currentStatus, intervalMs = 100 }) {
    if (typeof currentStatus !== 'function' || !Number.isInteger(intervalMs) || intervalMs < 1 || intervalMs > 100) {
      failure('agent_runtime_configuration_invalid')
    }
    this.#status = currentStatus
    this.#timer = setInterval(() => this.recheck(), intervalMs)
    this.#timer.unref()
  }
  toJSON() { return { type: 'RunSupervisor', active_runs: this.#active.size, closed: this.#closed } }
  [inspect.custom]() { return this.toJSON() }
  begin(run, onStop) {
    if (this.#closed) failure('agent_runtime_unavailable')
    if (this.#active.has(run.sessionId)) failure('agent_run_in_progress')
    const status = this.#status()
    const abort = new AbortController()
    let stopped, calls = 0, tools = 0, inputs = 0, outputs = 0, lastInput = 0
    const stop = (code, budget) => {
      if (stopped) return
      stopped = code === 'user' ? code : codes.has(code) ? code : 'agent_capability_rejected'
      // The host records the first cause before cancelling the public Agent.
      // Even a storage failure must abort the HTTP request and shut the gate.
      try { onStop(stopped, budget) } finally { abort.abort() }
    }
    const check = () => {
      if (stopped) failure(stopped === 'user' ? 'agent_capability_rejected' : stopped)
      const now = this.#status()
      if (this.#closed || now.configuration_current !== true || now.configuration_applied !== true ||
          now.revision !== status.revision) failure('agent_configuration_unavailable')
      if (Date.now() >= run.expiresAt) failure('agent_capability_expired')
      if (run.authorize() !== true) failure('agent_capability_rejected')
      return true
    }
    check()
    const budgets = run.claims.envelope.budgets
    const snapshot = () => ({
      model_calls: { used: calls, limit: budgets.max_turns, remaining: Math.max(0, budgets.max_turns - calls) },
      tool_calls: { used: tools, limit: budgets.max_tool_calls, remaining: Math.max(0, budgets.max_tool_calls - tools) },
      input_tokens: { used: inputs, limit: budgets.max_input_tokens, remaining: Math.max(0, budgets.max_input_tokens - inputs) },
      output_tokens: { used: outputs, limit: budgets.max_output_tokens, remaining: Math.max(0, budgets.max_output_tokens - outputs) },
      last_input: lastInput,
    })
    const exceed = (dimension, used, limit, requested = 1) => {
      stop('agent_run_budget_exceeded', { dimension, used, limit, requested })
      failure('agent_run_budget_exceeded')
    }
    const reserveModel = (inputTokens, providerLimit) => {
      check()
      if (!Number.isSafeInteger(providerLimit) || providerLimit < 1 || !Number.isSafeInteger(inputTokens) || inputTokens < 0) {
        stop('agent_input_measurement_unavailable'); failure('agent_input_measurement_unavailable')
      }
      if (calls >= budgets.max_turns) exceed('model_calls', calls, budgets.max_turns)
      if (inputs + inputTokens > budgets.max_input_tokens) exceed('input_tokens', inputs, budgets.max_input_tokens, inputTokens)
      if (outputs >= budgets.max_output_tokens) exceed('output_tokens', outputs, budgets.max_output_tokens, providerLimit)
      calls++; inputs += inputTokens; lastInput = inputTokens
      const cap = Math.min(providerLimit, budgets.max_output_tokens - outputs)
      outputs += cap
      let settled = false
      return Object.freeze({ maxTokens: cap,
        // Only for a confirmed context rejection before any generated block.
        // Count the attempted call and retain the conservative input charge.
        rejectBeforeOutput() {
          if (settled || stopped) return
          settled = true
          outputs -= cap
        },
        // A reservation belongs to one completed provider request. Missing,
        // interrupted or invalid usage never refunds its conservative bounds.
        settle(usage) {
          if (settled || stopped) return
          const counts = [usage?.inputTokens, usage?.cacheReadTokens ?? 0, usage?.cacheWriteTokens ?? 0, usage?.outputTokens]
          const input = counts[0] + counts[1] + counts[2]
          if (counts.some(n => !Number.isSafeInteger(n) || n < 0) || input > inputTokens || counts[3] > cap ||
              (usage.reasoningTokens !== undefined && (!Number.isSafeInteger(usage.reasoningTokens) || usage.reasoningTokens < 0 || usage.reasoningTokens > counts[3]))) {
            stop('agent_input_measurement_unavailable'); failure('agent_input_measurement_unavailable')
          }
          settled = true
          // Some providers/adapters emit zero-filled usage when accounting is
          // unavailable. Do not turn that placeholder into a free request.
          if (input === 0 || counts[3] === 0) return
          inputs -= inputTokens - input
          outputs -= cap - counts[3]
        },
      })
    }
    const lease = Object.freeze({ runId: run.runId, signal: abort.signal, check, stop, snapshot, exceed, reserveModel,
      tool() {
        check()
        if (tools >= budgets.max_tool_calls) exceed('tool_calls', tools, budgets.max_tool_calls)
        tools++
      },
      // Compatibility for callers that cannot provide verified usage.
      dispatch(inputTokens, providerLimit) { return reserveModel(inputTokens, providerLimit).maxTokens },
      finish: () => { if (this.#active.get(run.sessionId) === lease) this.#active.delete(run.sessionId) },
    })
    this.#active.set(run.sessionId, lease)
    return lease
  }
  recheck() {
    for (const lease of this.#active.values()) {
      try { lease.check() } catch (error) {
        try { lease.stop(error.message) } catch { this.#closed = true }
      }
    }
  }
  close() {
    this.#closed = true
    clearInterval(this.#timer)
    for (const lease of this.#active.values()) {
      try { lease.stop('agent_runtime_interrupted') } catch { /* already fail closed */ }
    }
  }
}
