import { timingSafeEqual } from 'node:crypto'
import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'
import { BridgeError, sendJSON } from './http-errors.mjs'
import { serveEventStream } from './event-stream.mjs'
import { serveTranscript } from './transcript.mjs'
import { validSessionCreate, validSessionCreated } from '../persistence/session-bindings.mjs'

const ajv = new Ajv2020({ strict: true })
const schema = name => ajv.compile(JSON.parse(readFileSync(new URL(`../../contracts/bridge-v1/${name}.schema.json`, import.meta.url))))
const prompt = schema('prompt-request')
const ack = schema('prompt-response')
const run = schema('run')
const decision = schema('approval-decision')
const closed = schema('session-closed')
const probeRequest = schema('model-probe-request')
const probeResult = schema('model-probe-result')
const empty = ajv.compile({ type: 'object', additionalProperties: false })
const backendErrors = new Map([
  ['agent_session_not_found', 404], ['agent_run_not_found', 404], ['agent_approval_missing', 409],
  ['agent_request_conflict', 409], ['agent_run_in_progress', 409], ['agent_run_recovering', 409],
  ['agent_invalid_request', 400], ['agent_invalid_context', 403], ['agent_capability_expired', 403],
  ['agent_capability_rejected', 403], ['agent_runtime_unavailable', 503],
  ['agent_session_conflict', 409], ['agent_session_unbound', 409], ['agent_session_provisioning', 409],
  ['agent_session_storage_missing', 503], ['agent_configuration_unavailable', 503], ['agent_configuration_conflict', 409],
])

function fail(code, status = 400) { throw new BridgeError(code, status) }

function readJSON(req, { maxBodyBytes, bodyTimeoutMs }) {
  if (!/^application\/json(?:\s*;\s*charset=utf-8)?$/i.test(req.headers['content-type'] ?? '') ||
      (req.headers['content-encoding'] && req.headers['content-encoding'] !== 'identity')) fail('agent_unsupported_media_type', 415)
  if (Number(req.headers['content-length']) > maxBodyBytes) fail('agent_request_too_large', 413)
  return new Promise((resolve, reject) => {
    const chunks = []
    let bytes = 0
    const cleanup = () => {
      clearTimeout(timer)
      req.off('data', data); req.off('end', end); req.off('aborted', aborted); req.off('error', aborted)
    }
    const stop = error => { cleanup(); req.pause(); reject(error) }
    const aborted = () => stop(new BridgeError('agent_request_aborted', 400))
    const data = chunk => {
      bytes += chunk.length
      if (bytes > maxBodyBytes) return stop(new BridgeError('agent_request_too_large', 413))
      chunks.push(chunk)
    }
    const end = () => {
      cleanup()
      try { resolve(JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Buffer.concat(chunks)))) }
      catch { reject(new BridgeError('agent_invalid_json', 400)) }
    }
    const timer = setTimeout(() => stop(new BridgeError('agent_request_timeout', 408)), bodyTimeoutMs)
    timer.unref()
    req.on('data', data); req.once('end', end); req.once('aborted', aborted); req.once('error', aborted)
  })
}

/** Internal Bridge transport, independent of DSH. Backends own durable admission,
 * capability verification, recovery and atomic control. Timeout/disconnect does
 * not cancel admitted work: the caller reconciles by run_id before retrying. */
export function createBridgeHandler({ token, backend, transcriptProjection,
  maxBodyBytes = 256 * 1024, bodyTimeoutMs = 5000, operationTimeoutMs = 10_000, maxPendingOperations = 64 }) {
  if (typeof token !== 'string' || !/^[!-~]{1,8192}$/.test(token)) throw new Error('agent_bridge_token_required')
  for (const value of [maxBodyBytes, bodyTimeoutMs, operationTimeoutMs, maxPendingOperations]) {
    if (!Number.isSafeInteger(value) || value < 1) throw new Error('agent_invalid_http_limits')
  }
  const authorization = Buffer.from(`Bearer ${token}`)
  let pending = 0
  async function invoke(method, ...args) {
    if (typeof backend[method] !== 'function') fail('agent_runtime_unavailable', 503)
    if (pending >= maxPendingOperations) fail('agent_bridge_busy', 503)
    pending++
    // Keep the slot until the actual operation settles, even after HTTP timeout.
    const operation = Promise.resolve().then(() => backend[method](...args)).finally(() => { pending-- })
    let timer
    try {
      return await Promise.race([operation, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new BridgeError('agent_operation_timeout', 504)), method === 'probe' ? 17000 : operationTimeoutMs)
        timer.unref()
      })])
    } finally { clearTimeout(timer) }
  }
  return async function bridge(req, res) {
    try {
      const supplied = Buffer.from(req.headers.authorization ?? '')
      const authCount = req.rawHeaders.filter((_, i) => i % 2 === 0 && req.rawHeaders[i].toLowerCase() === 'authorization').length
      if (authCount !== 1 || supplied.length !== authorization.length || !timingSafeEqual(supplied, authorization)) {
        res.setHeader('www-authenticate', 'Bearer')
        fail('agent_unauthorized', 401)
      }
      if (!req.url || req.url.length > 2048 || !req.url.startsWith('/') || req.url.includes('#')) fail('agent_invalid_request')
      // Match raw paths: URL dot-segment normalization must not alias resources.
      const [pathname, ...queryParts] = req.url.split('?')
      const id = '([A-Za-z0-9_-]{1,128})'
      const createSession = pathname === '/q4d/v1/sessions'
      const probe = pathname === '/q4d/v1/models/probe'
      const session = pathname.match(new RegExp(`^/q4d/v1/sessions/${id}(?:/(prompts|close))?$`))
      const runs = pathname.match(new RegExp(`^/q4d/v1/runs/${id}(?:/(events|cancel|reconcile))?$`))
      const approval = pathname.match(/^\/q4d\/v1\/approvals\/([^/]{1,768})\/decision$/)
      const basic = { '/q4d/v1/health': 'health', '/q4d/v1/runtime-health': 'runtimeHealth', '/q4d/v1/capabilities': 'capabilities' }[pathname]
      const maintenance = pathname.match(/^\/q4d\/v1\/maintenance(?:\/(drain|resume))?$/)
      if (!probe && !createSession && !session && !runs && !approval && !basic && !maintenance) fail('agent_not_found', 404)
      const transcript = session && !session[2]
      if (queryParts.length && !transcript) fail('agent_invalid_query')
      const method = probe || createSession || session?.[2] || ['cancel', 'reconcile'].includes(runs?.[2]) || approval || maintenance?.[1] ? 'POST' : 'GET'
      if (req.method !== method) { res.setHeader('allow', method); fail('agent_method_not_allowed', 405) }
      if (method === 'GET' && (req.headers['transfer-encoding'] || Number(req.headers['content-length']) > 0)) fail('agent_invalid_request')
      if (basic) return sendJSON(res, 200, await invoke(basic))
      if (maintenance && !maintenance[1]) return sendJSON(res, 200, await invoke('maintenance'))
      if (transcript) return await serveTranscript(req, res, session[1], backend.readTranscript, transcriptProjection)
      if (runs?.[2] === 'events') {
        const stream = await invoke('events', runs[1])
        return serveEventStream(req, res, stream.journal, { live: stream.live })
      }
      if (runs && !runs[2]) {
        const result = await invoke('getRun', runs[1])
        if (!run(result) || result.run_id !== runs[1]) fail('agent_backend_contract_error', 500)
        return sendJSON(res, 200, result)
      }
      const body = await readJSON(req, { maxBodyBytes, bodyTimeoutMs })
      if (res.destroyed) return
      if (maintenance) {
        if (!empty(body)) fail('agent_invalid_request')
        return sendJSON(res, 200, await invoke('maintenance', maintenance[1]))
      }
      if (probe) {
        if (!probeRequest(body)) fail('agent_invalid_request')
        const abort = new AbortController()
        const disconnected = () => { if (!res.writableEnded) abort.abort() }
        res.once('close', disconnected)
        try {
          const result = await invoke('probe', body, abort.signal)
          if (!probeResult(result) || ['provider', 'model', 'model_config_revision', 'probe_version'].some(key => result[key] !== body[key])) fail('agent_backend_contract_error', 500)
          return sendJSON(res, 200, result)
        } finally { abort.abort(); res.off('close', disconnected) }
      }
      if (createSession) {
        if (!validSessionCreate(body)) fail('agent_invalid_request')
        const result = await invoke('createSession', body)
        if (!validSessionCreated(result) || result.session_id !== body.q4d_session_id ||
            result.dsh_session_id !== body.q4d_session_id || result.provision_request_hash !== body.provision_request_hash ||
            result.provider !== body.provider || result.model !== body.model || result.profile !== body.profile ||
            result.created_profile_revision !== body.profile_revision || result.created_model_config_revision !== body.model_config_revision) {
          fail('agent_backend_contract_error', 500)
        }
        return sendJSON(res, 200, result)
      }
      if (session?.[2] === 'close') {
        if (!empty(body)) fail('agent_invalid_request')
        const result = await invoke('closeSession', session[1])
        if (!closed(result) || result.session_id !== session[1] || result.dsh_session_id !== session[1]) fail('agent_backend_contract_error', 500)
        return sendJSON(res, 200, result)
      }
      if (session) {
        if (!prompt(body) || body.q4d_session_id !== session[1]) fail('agent_invalid_request')
        const result = await invoke('admit', body)
        if (!ack(result) || result.run_id !== body.run_id) fail('agent_backend_contract_error', 500)
        return sendJSON(res, 202, result)
      }
      if (runs) {
        if (!empty(body)) fail('agent_invalid_request')
        const result = await invoke(runs[2] === 'reconcile' ? 'reconcileRun' : 'cancel', runs[1])
        if (!run(result) || result.run_id !== runs[1]) fail('agent_backend_contract_error', 500)
        return sendJSON(res, 200, result)
      }
      let approvalId
      try { approvalId = decodeURIComponent(approval[1]) } catch { fail('agent_invalid_request') }
      if (!/^[A-Za-z0-9_:-]{1,256}$/.test(approvalId) || !decision(body)) fail('agent_invalid_request')
      await invoke('decide', approvalId, body)
      return sendJSON(res, 200, { run_id: body.run_id, tool_call_id: body.tool_call_id, decision: body.decision })
    } catch (error) {
      if (res.headersSent) { res.destroy(); return }
      // Never return backend diagnostics, validation values, credentials or body.
      const status = error instanceof BridgeError ? error.status : backendErrors.get(error?.message)
      if (!req.complete) {
        res.setHeader('connection', 'close')
        res.once('finish', () => req.destroy())
      }
      sendJSON(res, status ?? 500, { error: { code: status ? error.message : 'agent_internal_error' } })
    }
  }
}
