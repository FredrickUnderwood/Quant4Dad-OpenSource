import { randomBytes } from 'node:crypto'
import Ajv from 'ajv'
import { Client } from '@modelcontextprotocol/sdk/client/index.js'
import { StreamableHTTPClientTransport } from '@modelcontextprotocol/sdk/client/streamableHttp.js'
import { strategyDiagnosticSchema } from './strategy-diagnostics.mjs'

const reserved = new Set([
  'actor_id', 'session_id', 'run_id', 'profile', 'tool_call_id', 'idempotency_key',
  'arguments_hash', 'approval_receipt', 'run_capability', 'runtime_token', '_meta',
])
const identifier = /^[A-Za-z0-9_-]{1,128}$/
const headerValue = /^[\x21-\x7e]{1,8192}$/

export class GatewayError extends Error {
  constructor(code, validation) {
    super(code)
    this.name = 'GatewayError'
    this.code = code
    if (validation) this.validation = validation
  }
}

function fail(code) { throw new GatewayError(code) }
function requireString(value, pattern) {
  if (typeof value !== 'string' || !pattern.test(value)) fail('agent_invalid_context')
}

// ULID: 48-bit millisecond time and 80 bits of randomness. No model input or
// upstream callId participates in the identity of a logical business call.
export function invocationId() {
  const alphabet = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'
  let value = (BigInt(Date.now()) << 80n) | BigInt(`0x${randomBytes(10).toString('hex')}`)
  let result = ''
  for (let i = 0; i < 26; i++, value >>= 5n) result = alphabet[Number(value & 31n)] + result
  return result
}

function freeze(value) {
  if (value && typeof value === 'object') {
    for (const item of Object.values(value)) freeze(item)
    Object.freeze(value)
  }
  return value
}

/**
 * T-00 candidate transport boundary. Only the trusted host supplies Run
 * contexts/receipts; model input supplies name/arguments. This class neither
 * signs capabilities nor replaces the Go Gateway's independent authorization.
 * Contexts and call ownership are intentionally memory-only: durable call
 * restoration is a separate T-02/T-09 obligation.
 */
export class TrustedGateway {
  #url
  #runtimeToken
  #catalog = new Map()
  #diagnostics = new Map()
  #runs = new Map()
  #calls = new WeakMap()
  #timeoutMs
  #maxResponseBytes

  constructor({ url, runtimeToken, catalog, timeoutMs = 30_000, maxResponseBytes = 256 * 1024 }) {
    this.#url = new URL(url)
    if (!['http:', 'https:'].includes(this.#url.protocol) || this.#url.username || this.#url.password ||
        this.#url.search || this.#url.hash || this.#url.pathname !== '/internal/mcp') {
      fail('agent_invalid_gateway')
    }
    requireString(runtimeToken, headerValue)
    if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 300_000 ||
        !Number.isSafeInteger(maxResponseBytes) || maxResponseBytes < 1 || maxResponseBytes > 1024 * 1024) {
      fail('agent_invalid_gateway')
    }
    this.#runtimeToken = runtimeToken
    this.#timeoutMs = timeoutMs
    this.#maxResponseBytes = maxResponseBytes
    const ajv = new Ajv({ strict: true, allErrors: false })
    for (const tool of catalog) {
      requireString(tool.name, identifier)
      if (this.#catalog.has(tool.name)) fail('agent_invalid_catalog')
      this.#catalog.set(tool.name, ajv.compile(structuredClone(tool.inputSchema)))
      if (['validate_strategy', 'create_strategy', 'update_strategy'].includes(tool.name)) {
        const variants = new Map()
        for (const mode of ['config', 'script']) for (let mask = 0; mask < 4; mask++) {
          variants.set(`${mode}:${mask}`, ajv.compile(strategyDiagnosticSchema(tool.inputSchema,
            { strategy: { body: { mode, indicators: mask & 1 ? [] : null, rules: mask & 2 ? [] : null } } })))
        }
        this.#diagnostics.set(tool.name, variants)
      }
    }
  }

  beginRun(dshSessionId, { sessionId, runId, capability, expiresAt, allowedTools, authorize }) {
    requireString(dshSessionId, identifier)
    requireString(sessionId, identifier)
    requireString(runId, identifier)
    requireString(capability, headerValue)
    if (!Number.isSafeInteger(expiresAt) || expiresAt <= Date.now() || expiresAt - Date.now() > 86_400_000 ||
        !Array.isArray(allowedTools) || allowedTools.some(name => !this.#catalog.has(name)) ||
        (authorize !== undefined && typeof authorize !== 'function') ||
        (capability.split('.').length === 3 && typeof authorize !== 'function')) {
      fail('agent_invalid_context')
    }
    if (authorize) {
      try { if (authorize() !== true) fail('agent_capability_rejected') }
      catch { fail('agent_capability_rejected') }
    }
    if (this.#runs.has(dshSessionId) || [...this.#runs.values()].some(run => run.runId === runId)) {
      fail('agent_run_in_progress')
    }
    this.#runs.set(dshSessionId, {
      sessionId, runId, capability, expiresAt, authorize, allowedTools: new Set(allowedTools), controller: new AbortController(),
    })
  }

  endRun(dshSessionId, runId) {
    const run = this.#runs.get(dshSessionId)
    if (!run || run.runId !== runId) fail('agent_run_context_missing')
    this.#runs.delete(dshSessionId)
    run.controller.abort()
  }

  #active(dshSessionId, expectedRun) {
    const run = this.#runs.get(dshSessionId)
    if (!run || (expectedRun && run !== expectedRun)) fail('agent_run_context_missing')
    if (run.expiresAt <= Date.now()) fail('agent_capability_expired')
    if (run.authorize) {
      try { if (run.authorize() !== true) fail('agent_capability_rejected') }
      catch { fail('agent_capability_rejected') }
    }
    return run
  }

  prepare(dshSessionId, name, args) {
    const run = this.#active(dshSessionId)
    if (!run.allowedTools.has(name)) fail('agent_tool_forbidden')
    if (!args || typeof args !== 'object' || Array.isArray(args) ||
        Object.keys(args).some(key => reserved.has(key))) fail('agent_invalid_arguments')
    // DSH gives the plugin a frozen lossless-JSON snapshot; snapshot again at
    // this boundary so an approval retry cannot change the submitted arguments.
    let snapshot
    try { snapshot = structuredClone(args) } catch { fail('agent_invalid_arguments') }
    const validate = this.#catalog.get(name)
    if (!validate(snapshot)) {
      let diagnostics = validate.errors
      const mode = snapshot.strategy?.body?.mode ?? 'config'
      const mask = (Array.isArray(snapshot.strategy?.body?.indicators) ? 1 : 0) | (Array.isArray(snapshot.strategy?.body?.rules) ? 2 : 0)
      const focused = this.#diagnostics.get(name)?.get(`${mode}:${mask}`)
      // Null remains legal; if this diagnostic projection disagrees, retain
      // the authoritative original errors instead of inventing a new cause.
      if (focused && !focused(snapshot)) diagnostics = focused.errors
      // Only local schema diagnostics enter model context; never include the
      // submitted values, credentials, HTTP responses or internal exceptions.
      const validation = (diagnostics ?? []).slice(0, 3).map(error => ({
        path: (error.instancePath || '/').slice(0, 512),
        message: [error.message, error.params.additionalProperty ?? error.params.missingProperty].filter(Boolean).join(': ').slice(0, 256),
      }))
      throw new GatewayError('agent_invalid_arguments', validation)
    }
    let encoded
    try { encoded = JSON.stringify(snapshot) } catch { fail('agent_invalid_arguments') }
    if (Buffer.byteLength(encoded) > 64 * 1024) fail('agent_invalid_arguments')
    const id = invocationId()
    const invocation = freeze({
      session_id: run.sessionId, run_id: run.runId, tool_call_id: id,
      idempotency_key: `q4d:${run.runId}:${id}`, name, arguments: snapshot,
    })
    this.#calls.set(invocation, { dshSessionId, run, inFlight: false })
    return invocation
  }

  async invoke(invocation, { receipt, signal, onStarted } = {}) {
    const owned = this.#calls.get(invocation)
    if (!owned) fail('agent_unknown_tool_call')
    const { dshSessionId, run } = owned
    this.#active(dshSessionId, run)
    if (owned.inFlight) fail('agent_tool_call_in_progress')
    if (receipt !== undefined) requireString(receipt, headerValue)
    const deadline = AbortSignal.timeout(Math.min(this.#timeoutMs, run.expiresAt - Date.now()))
    const combined = AbortSignal.any([deadline, run.controller.signal, ...(signal ? [signal] : [])])
    if (combined.aborted) fail('agent_tool_cancelled')
    owned.inFlight = true

    // One transport per attempt captures an immutable logical call context.
    // No shared mutable headers, auth discovery, arbitrary URL, or automatic
    // retry of tools/call. An approval retry reuses the same invocation object.
    const guardedFetch = async (url, init = {}) => {
      this.#active(dshSessionId, run)
      combined.throwIfAborted()
      if (String(url) !== this.#url.href) fail('agent_invalid_gateway')
      const headers = new Headers(init.headers)
      headers.set('Authorization', `Bearer ${this.#runtimeToken}`)
      headers.set('X-Q4D-Run-Capability', run.capability)
      headers.set('X-Q4D-Tool-Call-ID', invocation.tool_call_id)
      headers.set('Idempotency-Key', invocation.idempotency_key)
      headers.delete('X-Q4D-Approval-Receipt')
      if (receipt !== undefined) headers.set('X-Q4D-Approval-Receipt', receipt)
      const response = await fetch(this.#url, {
        ...init, headers, redirect: 'error',
        signal: AbortSignal.any([combined, ...(init.signal ? [init.signal] : [])]),
      })
      if (!response.body) return response
      // Cap decompressed bytes while preserving streaming. Buffering until
      // EOF would stall a valid MCP result on a still-open SSE connection.
      const reader = response.body.getReader()
      let bytes = 0
      const limit = this.#maxResponseBytes
      const body = new ReadableStream({
        async pull(controller) {
          try {
            const { value, done } = await reader.read()
            if (done) { controller.close(); return }
            bytes += value.length
            if (bytes > limit) fail('agent_tool_result_too_large')
            controller.enqueue(value)
          } catch (error) {
            controller.error(error)
            await reader.cancel().catch(() => {})
          }
        },
        cancel: reason => reader.cancel(reason),
      })
      return new Response(body, { status: response.status, headers: response.headers })
    }
    const client = new Client({ name: 'q4d-trusted-adapter', version: '0.0.0' })
    const transport = new StreamableHTTPClientTransport(this.#url, {
      fetch: guardedFetch, reconnectionOptions: { maxRetries: 0 },
    })
    let started = false
    let observationError
    try {
      await client.connect(transport, { signal: combined, timeout: this.#timeoutMs })
      const result = await client.callTool({ name: invocation.name, arguments: invocation.arguments }, undefined, {
        signal: combined, timeout: this.#timeoutMs,
        ...(onStarted ? { onprogress(progress) {
          // T-00 Gateway handshake: only an authenticated, authorized dispatch
          // emits this marker. A transport attempt or approval challenge does
          // not prove execution. SDK owns progressToken, outside arguments.
          if (started || progress.progress !== 0 || progress.total !== 1 || progress.message !== 'q4d.tool.started.v1') return
          started = true
          try { onStarted() } catch (error) { observationError = error }
        } } : {}),
      })
      if (observationError) throw observationError
      if (onStarted && !started && !result.isError && result.structuredContent?.error?.code !== 'agent_approval_required') {
        fail('agent_tool_start_unconfirmed')
      }
      return result
    } catch (error) {
      if (error instanceof GatewayError) throw error
      if (combined.aborted) fail('agent_tool_cancelled')
      // SDK/HTTP errors may include a remote body or URL. Never pass those
      // diagnostics into model context, persisted events, or external logs.
      fail('agent_tool_transport_error')
    } finally {
      await client.close().catch(() => {})
      owned.inFlight = false
    }
  }
}
