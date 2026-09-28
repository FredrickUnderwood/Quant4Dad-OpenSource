import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { inspect } from 'node:util'
import { bootstrapPath, bootstrapURL, controlTokenPattern } from '../bootstrap/validation.mjs'
import { canonicalAuthorizationJSON } from './run-capability.mjs'

const runID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}(?![\s\S])/
const rejected = (reason = 'invalid_request', status) => Object.assign(new Error('agent_capability_rejected'), { reason, status })
const diagnosticReasons = new Set(['grant_stale', 'grant_expired', 'refresh_stale', 'refresh_failed',
  'transport_failed', 'request_timeout', 'invalid_headers', 'http_status', 'body_too_large', 'invalid_body',
  'grant_missing', 'claims_mismatch', 'policy_closed'])

/** Current Go policy, separate from model/bootstrap generations. Read before
 * admission, refresh every half second, fail closed after two seconds (monotonic).
 * The refresh cadence leaves scheduling margin before the one-second request
 * deadline can consume the two-second grant age. It never extends authority.
 * The supervisor can therefore check synchronously even while a model is silent.
 * Only non-secret original claims are cached; tokens and prompts never enter it. */
export class RunPolicy {
  #url; #token; #timer; #closed = false
  #grants = new Map(); #pending = new Map(); #requests = new Set(); #discard = new Set()
  #pendingStarted = new Map(); #nextPollAt; #pollDelayMs = 0
  #catchup = new Map()
  #slowReportedAt = new Map()
  #diagnostic; #reported = new Set()
  constructor({ bootstrapURL: url, controlToken, diagnostic = row => process.stdout.write(JSON.stringify(row) + '\n') }) {
    this.#url = bootstrapURL(url, bootstrapPath)
    if (typeof controlToken !== 'string' || !controlTokenPattern.test(controlToken)) throw rejected()
    this.#token = controlToken
    this.#diagnostic = diagnostic
    this.#nextPollAt = performance.now() + 500
    this.#timer = setInterval(() => {
      const now = performance.now()
      this.#pollDelayMs = Math.max(0, Math.floor(now - this.#nextPollAt))
      this.#nextPollAt = now + 500
      for (const [id, grant] of this.#grants) {
        if (Date.now() >= grant.deadline) { this.#report(id, 'grant_expired'); this.#grants.delete(id) }
        else void this.#refresh(id).catch(() => {})
      }
    }, 500)
    this.#timer.unref()
  }
  toJSON() { return { type: 'RunPolicy', redacted: true } }
  [inspect.custom]() { return 'RunPolicy { redacted }' }
  #report(id, reason, fields = {}) {
    if (typeof id !== 'string' || !runID.test(id) || this.#reported.has(id)) return
    this.#reported.add(id)
    if (this.#reported.size > 64) this.#reported.delete(this.#reported.values().next().value)
    // Only locally selected fields reach logs; never serialize claims, tokens,
    // response bodies, headers or transport Error objects.
    try { this.#diagnostic({ event: 'agent_run_policy_rejected', run_id: id,
      reason: diagnosticReasons.has(reason) ? reason : 'refresh_failed', ...fields }) } catch { /* diagnostics cannot change authority */ }
  }
  authorize = claims => {
    const id = claims?.envelope?.run_id
    const grant = this.#grants.get(id)
    let reason
    if (this.#closed) reason = 'policy_closed'
    else if (!grant) reason = 'grant_missing'
    else if (performance.now() - grant.checked >= 2000) reason = 'grant_stale'
    else if (Date.now() >= grant.deadline) reason = 'grant_expired'
    else if (grant.canonical !== canonicalAuthorizationJSON(claims)) reason = 'claims_mismatch'
    if (!reason) return true
    this.#report(id, reason, reason === 'grant_stale' ? {
      age_ms: Math.floor(performance.now() - grant.checked), refresh_pending: this.#pending.has(id),
      ...(this.#pendingStarted.has(id) ? { pending_age_ms: Math.floor(performance.now() - this.#pendingStarted.get(id)) } : {}),
      poll_delay_ms: this.#pollDelayMs,
      last_refresh_elapsed_ms: Math.floor(grant.received - grant.checked),
      since_refresh_completed_ms: Math.floor(performance.now() - grant.received),
    } : {})
    return false
  }
  prepare = async request => {
    const id = request?.run_id
    if (this.#closed || typeof id !== 'string' || !runID.test(id)) throw rejected()
    await this.#refresh(id)
  }
  forget = id => {
    this.#grants.delete(id)
    this.#reported.delete(id)
    this.#slowReportedAt.delete(id)
    clearTimeout(this.#catchup.get(id)); this.#catchup.delete(id)
    if (this.#pending.has(id)) this.#discard.add(id)
  }
  #refresh(id) {
    if (this.#pending.has(id)) return this.#pending.get(id)
    if (this.#closed || (!this.#grants.has(id) && this.#grants.size + this.#pending.size >= 32)) return Promise.reject(rejected())
    clearTimeout(this.#catchup.get(id)); this.#catchup.delete(id)
    const checked = performance.now()
    this.#pendingStarted.set(id, checked)
    const promise = this.#read(id).then(({ claims, canonical }) => {
      const deadline = Math.min(claims.exp * 1000, claims.iat * 1000 + claims.envelope.budgets.wall_time_ms)
      if (this.#closed || this.#discard.has(id)) throw rejected('discarded')
      if (!Number.isSafeInteger(deadline) || Date.now() >= deadline) throw rejected('grant_expired')
      if (performance.now() - checked >= 2000) throw rejected('refresh_stale')
      this.#grants.set(id, { canonical, checked, deadline, received: performance.now() })
      this.#reported.delete(id)
      const elapsed = Math.floor(performance.now() - checked)
      const lastSlow = this.#slowReportedAt.get(id)
      if (elapsed >= 500 && (lastSlow === undefined || performance.now() - lastSlow >= 10000)) {
        this.#slowReportedAt.set(id, performance.now())
        try { this.#diagnostic({ event: 'agent_run_policy_refresh_slow', run_id: id,
          elapsed_ms: elapsed, poll_delay_ms: this.#pollDelayMs }) } catch { /* logs cannot change authority */ }
      }
    }).catch(error => {
      this.#grants.delete(id)
      if (!this.#closed && !this.#discard.has(id)) this.#report(id, error.reason ?? 'refresh_failed', {
        elapsed_ms: Math.floor(performance.now() - checked), ...(Number.isInteger(error.status) ? { http_status: error.status } : {}) })
      throw rejected()
    }).finally(() => {
      this.#pending.delete(id); this.#pendingStarted.delete(id); this.#discard.delete(id)
      const grant = this.#grants.get(id)
      // An in-flight request can consume a periodic tick. Once its successful
      // response arrives, do not wait for another (possibly delayed) tick when
      // the next refresh is already due from the original request start.
      // A macrotask yields to I/O; each catch-up requires >=500ms of elapsed
      // request time, and failed/discarded requests never schedule a retry.
      if (!this.#closed && grant?.checked === checked && Date.now() < grant.deadline && performance.now() - checked >= 500) {
        const timer = setTimeout(() => {
          this.#catchup.delete(id)
          if (!this.#closed && this.#grants.get(id) === grant && Date.now() < grant.deadline) void this.#refresh(id).catch(() => {})
        }, 0)
        timer.unref(); this.#catchup.set(id, timer)
      }
    })
    this.#pending.set(id, promise)
    return promise
  }
  #read(id) {
    return new Promise((resolve, reject) => {
      let req, res, timer, done = false
      const finish = (error, value) => {
        if (done) return
        done = true; clearTimeout(timer); this.#requests.delete(abort)
        if (error) { res?.destroy(); req?.destroy(); reject(error) } else resolve(value)
      }
      const abort = () => finish(rejected('transport_failed'))
      this.#requests.add(abort)
      try {
        const url = new URL(this.#url)
        url.pathname = `/internal/v1/agent/runs/${id}/authorization`
        timer = setTimeout(() => finish(rejected('request_timeout')), 1000)
        req = (url.protocol === 'https:' ? httpsRequest : httpRequest)(url, {
          method: 'GET', agent: false, maxHeaderSize: 16384,
          headers: { authorization: `Bearer ${this.#token}`, accept: 'application/json', 'accept-encoding': 'identity' },
        }, response => {
          res = response
          res.on('error', abort); res.on('aborted', abort)
          const headers = res.headers, seen = new Set()
          for (let i = 0; i < res.rawHeaders.length; i += 2) {
            const name = res.rawHeaders[i].toLowerCase()
            if (seen.has(name)) { finish(rejected('invalid_headers')); return }; seen.add(name)
          }
          if (res.statusCode !== 200) { finish(rejected('http_status', res.statusCode)); return }
          if (headers['cache-control'] !== 'no-store' || headers['x-content-type-options'] !== 'nosniff' ||
              headers['content-encoding'] || headers['set-cookie'] || !/^application\/json(?:;\s*charset=utf-8)?$/i.test(headers['content-type'] ?? '') ||
              (headers['content-length'] !== undefined && (!/^\d+$/.test(headers['content-length']) || Number(headers['content-length']) > 8192))) { finish(rejected('invalid_headers')); return }
          const chunks = []; let size = 0
          res.on('data', chunk => { size += chunk.length; if (size > 8192) finish(rejected('body_too_large')); else chunks.push(chunk) })
          res.on('end', () => {
            if (done) return
            try {
              if (!res.complete) throw rejected()
              const canonical = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(Buffer.concat(chunks, size))
              const claims = JSON.parse(canonical)
              // Full schema, cryptography, time and binding validation still run
              // in BootstrapSync.verifyPrompt before this cache grants authority.
              if (canonicalAuthorizationJSON(claims) !== canonical || claims?.envelope?.run_id !== id) throw rejected()
              finish(false, { claims, canonical })
            } catch { finish(rejected('invalid_body')) }
          })
        })
        req.on('error', abort); req.end()
      } catch { abort() }
    })
  }
  async close() {
    this.#closed = true; clearInterval(this.#timer); this.#grants.clear(); this.#reported.clear(); this.#slowReportedAt.clear()
    for (const timer of this.#catchup.values()) clearTimeout(timer)
    this.#catchup.clear()
    for (const abort of this.#requests) abort()
    await Promise.allSettled([...this.#pending.values()])
  }
}
