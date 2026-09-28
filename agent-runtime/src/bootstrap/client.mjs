import { request as httpRequest } from 'node:http'
import { request as httpsRequest } from 'node:https'
import { inspect } from 'node:util'
import { BootstrapError, bootstrapPath, bootstrapURL, bootstrapRevision, controlTokenPattern,
  decodeBootstrap, failBootstrap, maxBootstrapBytes } from './validation.mjs'

/** Internal control-plane reader. No cookies, redirects, proxies, retry, body
 * logging, secret persistence or response-selected destination. TLS uses Node's
 * certificate validation. A timeout covers DNS through the final body byte. */
export class BootstrapClient {
  #url
  #token
  #timeoutMs
  /** @param {{url: string, controlToken: string, timeoutMs?: number}} options */
  constructor({ url, controlToken, timeoutMs = 5000 } = {}) {
    try {
      this.#url = bootstrapURL(url, bootstrapPath)
      if (typeof controlToken !== 'string' || !controlTokenPattern.test(controlToken) ||
          !Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 5000) failBootstrap()
      this.#token = controlToken
      this.#timeoutMs = timeoutMs
    } catch { failBootstrap('agent_bootstrap_configuration_invalid') }
  }
  toJSON() { return { type: 'BootstrapClient', redacted: true } }
  [inspect.custom]() { return 'BootstrapClient { redacted }' }

  read(known = '', { signal } = {}) {
    if (typeof known !== 'string' || (known !== '' && !bootstrapRevision.test(known)) ||
        (signal !== undefined && !(signal instanceof AbortSignal))) {
      return Promise.reject(new BootstrapError('agent_bootstrap_input_invalid'))
    }
    return new Promise((resolve, reject) => {
      let request, response, timer, done = false
      const finish = (error, value) => {
        if (done) return
        done = true
        clearTimeout(timer)
        signal?.removeEventListener('abort', abort)
        if (error) { response?.destroy(); request?.destroy(); reject(new BootstrapError(error)) }
        else resolve(value)
      }
      const abort = () => finish('agent_bootstrap_cancelled')
      try {
        if (signal?.aborted) { abort(); return }
        signal?.addEventListener('abort', abort, { once: true })
        const url = new URL(this.#url)
        if (known) url.searchParams.set('revision', known)
        timer = setTimeout(() => finish('agent_bootstrap_timeout'), this.#timeoutMs)
        request = (url.protocol === 'https:' ? httpsRequest : httpRequest)(url, {
          method: 'GET', agent: false, maxHeaderSize: 16 * 1024,
          headers: { authorization: `Bearer ${this.#token}`, accept: 'application/json', 'accept-encoding': 'identity', 'x-q4d-bootstrap-profiles': '1' },
        }, res => {
          response = res
          const invalid = () => finish('agent_bootstrap_invalid')
          // Install error handlers even when rejecting headers before reading.
          res.on('error', () => finish('agent_bootstrap_unavailable'))
          res.on('aborted', () => finish('agent_bootstrap_unavailable'))
          if (res.statusCode === 401 || res.statusCode === 403) { finish('agent_bootstrap_unauthorized'); return }
          if (![200, 304].includes(res.statusCode)) { finish('agent_bootstrap_unavailable'); return }
          const headers = res.headers
          const seen = new Set()
          for (let i = 0; i < res.rawHeaders.length; i += 2) {
            const name = res.rawHeaders[i].toLowerCase()
            if (seen.has(name)) { invalid(); return }
            seen.add(name)
          }
          if (headers['cache-control'] !== 'no-store' || headers['x-content-type-options'] !== 'nosniff' ||
              headers['content-encoding'] || headers['set-cookie']) { invalid(); return }
          if (res.statusCode === 304) {
            if (!known || headers.etag !== `"${known}"` || headers['transfer-encoding'] ||
                (headers['content-length'] !== undefined && headers['content-length'] !== '0')) { invalid(); return }
          } else if (!/^application\/json(?:;\s*charset=utf-8)?$/i.test(headers['content-type'] ?? '') ||
              (headers['content-length'] !== undefined &&
                (!/^\d+$/.test(headers['content-length']) || Number(headers['content-length']) > maxBootstrapBytes))) {
            invalid(); return
          }
          let size = 0
          const chunks = []
          res.on('data', chunk => {
            size += chunk.length
            if (size > maxBootstrapBytes || (res.statusCode === 304 && size)) { invalid(); return }
            chunks.push(chunk)
          })
          res.on('end', () => {
            if (done) return
            if (!res.complete) { invalid(); return }
            if (res.statusCode === 304) { finish(null, Object.freeze({ unchanged: true, revision: known })); return }
            try {
              const snapshot = decodeBootstrap(Buffer.concat(chunks, size))
              if (headers.etag !== `"${snapshot.revision}"` || snapshot.mcp.runtime_token === this.#token) {
                invalid(); return
              }
              finish(null, Object.freeze({ unchanged: false, snapshot }))
            } catch { invalid() }
          })
        })
        request.on('error', () => finish('agent_bootstrap_unavailable'))
        request.end()
      } catch { finish('agent_bootstrap_configuration_invalid') }
    })
  }
}
