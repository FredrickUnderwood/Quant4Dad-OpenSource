import { createHash, createPublicKey, verify } from 'node:crypto'
import { readFileSync } from 'node:fs'
import Ajv2020 from 'ajv/dist/2020.js'

export const runtimeAudience = 'q4d-agent-runtime'
export const gatewayAudience = 'q4d-internal-mcp'
export const tokenType = 'q4d-run-capability-v1+jwt'
// Unlike $, the final assertion cannot match before a trailing line terminator.
const id = /^[A-Za-z0-9_-]{1,128}(?![\s\S])/
const digest = /^sha256:[0-9a-f]{64}(?![\s\S])/
const issuerPattern = /^[A-Za-z0-9_.:/-]{1,128}(?![\s\S])/
const ajv = new Ajv2020({ strict: true })
for (const name of ['execution-envelope', 'run-capability', 'protected-header']) {
  ajv.addSchema(JSON.parse(readFileSync(new URL(`../../contracts/authorization-v1/${name}.schema.json`, import.meta.url))), name)
}
const envelopeSchema = ajv.getSchema('execution-envelope')
const claimsSchema = ajv.getSchema('run-capability')
const headerSchema = ajv.getSchema('protected-header')
const fail = (code = 'agent_capability_rejected') => { throw new Error(code) }

// Only the schema's ASCII-string/integer domain is hashed. This deliberately
// does not implement arbitrary Tool-argument canonicalization or general JCS.
export function canonicalAuthorizationJSON(value) {
  if (Array.isArray(value)) return '[' + value.map(canonicalAuthorizationJSON).join(',') + ']'
  if (value !== null && typeof value === 'object') return '{' + Object.keys(value).sort().map(key => JSON.stringify(key) + ':' + canonicalAuthorizationJSON(value[key])).join(',') + '}'
  return JSON.stringify(value)
}
export function executionEnvelopeDigest(envelope) {
  if (!envelopeSchema(envelope)) fail('agent_capability_input_invalid')
  return 'sha256:' + createHash('sha256').update(canonicalAuthorizationJSON(envelope)).digest('hex')
}
export function promptRequestHash(sessionId, text) {
  if (typeof sessionId !== 'string' || !id.test(sessionId) || typeof text !== 'string' || !text.isWellFormed() ||
      [...text].length < 1 || [...text].length > 32000) fail('agent_capability_input_invalid')
  return 'sha256:' + createHash('sha256').update(JSON.stringify({ session_id: sessionId, content: [{ type: 'text', text }] })).digest('hex')
}
function decode64(value) {
  if (typeof value !== 'string' || !/^[A-Za-z0-9_-]+$/.test(value)) fail()
  const bytes = Buffer.from(value, 'base64url')
  if (bytes.toString('base64url') !== value) fail()
  return bytes
}
function decodeCanonical(bytes) {
  // Preserve a BOM so JSON.parse rejects it rather than silently changing the
  // signed wire bytes before the canonical comparison.
  const text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
  const value = JSON.parse(text)
  if (canonicalAuthorizationJSON(value) !== text) fail()
  return value
}
function freeze(value) {
  if (value !== null && typeof value === 'object') {
    for (const child of Object.values(value)) freeze(child)
    Object.freeze(value)
  }
  return value
}
function bindingMatches(claims, binding) {
  return binding && typeof binding === 'object' && Object.keys(binding).sort().join(',') ===
    'client_request_id,execution_envelope_digest,request_hash,run_id,session_id' &&
    id.test(binding.session_id) && id.test(binding.run_id) && id.test(binding.client_request_id) &&
    digest.test(binding.request_hash) && digest.test(binding.execution_envelope_digest) &&
    claims.session_id === binding.session_id && claims.envelope.run_id === binding.run_id &&
    claims.envelope.client_request_id === binding.client_request_id && claims.request_hash === binding.request_hash &&
    claims.execution_envelope_digest === binding.execution_envelope_digest
}

/** Keys are a trusted, local kid -> raw Ed25519 public-key base64url map. No
 * private keys, token-supplied key URLs, algorithm negotiation or key discovery.
 * authorize is mandatory and must synchronously return true only for CURRENT
 * ownership/configuration/policy/revocation state. It sees immutable claims.
 * Call this boundary on admission and again before each authorized dispatch. */
export function createRunVerifier({ issuer, audience, keys, authorize }) {
  if (typeof issuer !== 'string' || !issuerPattern.test(issuer) || ![runtimeAudience, gatewayAudience].includes(audience) ||
      !keys || typeof keys !== 'object' || Array.isArray(keys) || typeof authorize !== 'function' ||
      Object.keys(keys).length < 1 || Object.keys(keys).length > 16) fail('agent_capability_configuration_invalid')
  const publicKeys = new Map()
  try {
    for (const [kid, raw] of Object.entries(keys)) {
      if (!id.test(kid) || decode64(raw).length !== 32) fail()
      publicKeys.set(kid, createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: raw }, format: 'jwk' }))
    }
  } catch { fail('agent_capability_configuration_invalid') }

  function verifyToken(token, binding, now = Date.now()) {
    let claims
    try {
      if (typeof token !== 'string' || token.length > 8192 || !Number.isSafeInteger(now) || now < 1000) fail()
      const parts = token.split('.')
      if (parts.length !== 3) fail()
      const headerBytes = decode64(parts[0])
      if (headerBytes.length > 512) fail()
      const header = decodeCanonical(headerBytes)
      if (!headerSchema(header) || !publicKeys.has(header.kid)) fail()
      const signature = decode64(parts[2])
      if (signature.length !== 64 || !verify(null, Buffer.from(parts[0] + '.' + parts[1]), publicKeys.get(header.kid), signature)) fail()
      claims = decodeCanonical(decode64(parts[1]))
      if (!claimsSchema(claims) || claims.iss !== issuer || !claims.aud.includes(audience) ||
          claims.iat > Math.floor(now / 1000) || claims.exp <= claims.iat ||
          claims.exp - claims.iat > Math.ceil(claims.envelope.budgets.wall_time_ms / 1000) ||
          claims.allowed_tools.some((name, i) => i > 0 && name <= claims.allowed_tools[i - 1]) ||
          executionEnvelopeDigest(claims.envelope) !== claims.execution_envelope_digest || !bindingMatches(claims, binding)) fail()
    } catch { fail() }
    if (now >= Math.min(claims.exp * 1000, claims.iat * 1000 + claims.envelope.budgets.wall_time_ms)) fail('agent_capability_expired')
    claims = freeze(claims)
    let allowed
    try { allowed = authorize(claims) } catch { fail() }
    // A Promise, undefined or an unavailable policy snapshot is never authority.
    if (allowed !== true) {
      if (allowed instanceof Promise) allowed.catch(() => {})
      fail()
    }
    return claims
  }

  function verifyPrompt(request, now = Date.now()) {
    let binding
    try {
      if (!Array.isArray(request.content) || request.content.length !== 1 ||
          Object.keys(request.content[0]).sort().join(',') !== 'text,type' || request.content[0].type !== 'text' ||
          promptRequestHash(request.q4d_session_id, request.content[0].text) !== request.request_hash) fail()
      binding = { session_id: request.q4d_session_id, run_id: request.run_id, client_request_id: request.client_request_id,
        request_hash: request.request_hash, execution_envelope_digest: request.execution_envelope_digest }
    } catch { fail() }
    const claims = verifyToken(request.run_capability, binding, now)
    const capability = request.run_capability
    return Object.freeze({ sessionId: claims.session_id, runId: claims.envelope.run_id,
      expiresAt: Math.min(claims.exp * 1000, claims.iat * 1000 + claims.envelope.budgets.wall_time_ms),
      allowedTools: claims.allowed_tools, capability, claims,
      // Rechecks expiry AND live policy; never put this closure in a journal.
      authorize: () => { verifyToken(capability, binding); return true },
    })
  }
  return Object.freeze({ verify: verifyToken, verifyPrompt })
}
