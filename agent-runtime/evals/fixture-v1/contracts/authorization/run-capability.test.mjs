import assert from 'node:assert/strict'
import { generateKeyPairSync, sign } from 'node:crypto'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { createRunVerifier, canonicalAuthorizationJSON as canonical, executionEnvelopeDigest, promptRequestHash,
  runtimeAudience, gatewayAudience, tokenType } from '../../../../src/auth/run-capability.mjs'
import { TrustedGateway } from '../../../../src/mcp/trusted-gateway.mjs'
import { queryTool } from '../../helpers/mcp-gateway.mjs'

const fixtures = JSON.parse(readFileSync(new URL('./fixtures/valid.json', import.meta.url)))
const now = 1788900000000
const encode = value => Buffer.from(typeof value === 'string' ? value : canonical(value)).toString('base64url')
function setup(authorize = () => true) {
  const { privateKey, publicKey } = generateKeyPairSync('ed25519')
  const keys = { 'key-1': publicKey.export({ format: 'jwk' }).x }
  const config = { keys, issuer: 'q4d-test-issuer', audience: runtimeAudience, authorize }
  const claims = { iss: config.issuer, aud: [runtimeAudience, gatewayAudience], iat: now / 1000, exp: now / 1000 + 300,
    jti: 'a'.repeat(32), session_id: 'session-1', request_hash: promptRequestHash('session-1', 'query'),
    execution_envelope_digest: fixtures.execution_envelope_digest, envelope: structuredClone(fixtures.envelope), allowed_tools: ['query_kline'] }
  const header = { alg: 'Ed25519', kid: 'key-1', typ: tokenType }
  const signToken = (payload = claims, protectedHeader = header) => {
    const input = encode(protectedHeader) + '.' + encode(payload)
    return input + '.' + sign(null, Buffer.from(input), privateKey).toString('base64url')
  }
  const binding = { session_id: claims.session_id, run_id: claims.envelope.run_id, client_request_id: claims.envelope.client_request_id,
    request_hash: claims.request_hash, execution_envelope_digest: claims.execution_envelope_digest }
  return { config, claims, binding, signToken, header, verifier: createRunVerifier(config) }
}
const rejected = fn => assert.throws(fn, { message: 'agent_capability_rejected' })

test('long-run budget is admitted across the former 10-minute boundary and expires at 30 minutes', () => {
  const { verifier, claims, binding, signToken } = setup()
  claims.envelope.budgets = JSON.parse(readFileSync(new URL('../../../../fixtures/long-run-budgets.json', import.meta.url)))
  claims.exp = claims.iat + claims.envelope.budgets.wall_time_ms / 1000
  claims.execution_envelope_digest = executionEnvelopeDigest(claims.envelope)
  binding.execution_envelope_digest = claims.execution_envelope_digest
  const token = signToken()
  for (const elapsed of [600000, 1799999]) assert.equal(verifier.verify(token, binding, now + elapsed).jti, claims.jti)
  assert.throws(() => verifier.verify(token, binding, now + 1800000), { message: 'agent_capability_expired' })
  assert.throws(() => executionEnvelopeDigest({ ...claims.envelope, budgets: { ...claims.envelope.budgets, max_turns: 4097 } }), { message: 'agent_capability_input_invalid' })
})

test('TEST-RUN-AUTH-01 shared envelope and Unicode prompt hash vectors', () => {
  assert.equal(executionEnvelopeDigest(fixtures.envelope), fixtures.execution_envelope_digest)
  for (const value of fixtures.prompt_cases) assert.equal(promptRequestHash('session-1', value.text), value.request_hash)
  assert.throws(() => promptRequestHash('session-1', '\ud800'), { message: 'agent_capability_input_invalid' })
  for (const ending of ['\n', '\r', '\u2028', '\u2029']) {
    assert.throws(() => promptRequestHash('session-1' + ending, 'query'), { message: 'agent_capability_input_invalid' })
    assert.throws(() => executionEnvelopeDigest({ ...fixtures.envelope, model: 'alpha' + ending }), { message: 'agent_capability_input_invalid' })
  }
})
test('TEST-RUN-AUTH-02 signature verifies and each request identity is bound', () => {
  const { verifier, claims, binding, signToken } = setup()
  assert.deepEqual(verifier.verify(signToken(), binding, now), claims)
  for (const field of Object.keys(binding)) rejected(() => verifier.verify(signToken(), { ...binding, [field]: field.includes('hash') || field.includes('digest') ? 'sha256:' + 'b'.repeat(64) : 'other' }, now))
})
test('TEST-RUN-AUTH-03 algorithms, types, keys and external key URLs cannot be substituted', () => {
  const { verifier, binding, signToken, header } = setup()
  for (const edit of [{ alg: 'none' }, { alg: 'EdDSA' }, { alg: 'HS256' }, { typ: 'JWT' }, { kid: 'other' }, { kid: 'key-1\n' }, { jku: 'https://untrusted.invalid' }, { crit: [] }]) {
    rejected(() => verifier.verify(signToken(undefined, { ...header, ...edit }), binding, now))
  }
  rejected(() => setup().verifier.verify(signToken(), binding, now))
})
test('TEST-RUN-AUTH-04 duplicate fields and noncanonical signed bytes are rejected', () => {
  const { verifier, claims, binding, signToken } = setup()
  const raw = canonical(claims)
  for (const payload of [
    raw.replace('"iat":1788900000', '"iat":1,"iat":1788900000'), raw.replace('"max_turns":12', '"max_turns":12.0'),
    raw.replace('"run-1"', '"\\u0072un-1"'), ' ' + raw, '\ufeff' + raw, raw.replace('"bridge_protocol":1,', ''),
    raw.replace('"max_turns":12', '"max_turns":null'), raw.slice(0, -1) + ',"secret":"do-not-return"}',
  ]) rejected(() => verifier.verify(signToken(payload), binding, now))
  const token = signToken()
  for (const value of [token + '=', token + '\n', token + '.extra', 'a'.repeat(8193), token.slice(0, token.lastIndexOf('.')) + '.AA']) rejected(() => verifier.verify(value, binding, now))
})
test('TEST-RUN-AUTH-05 expiry, future issue time, TTL and envelope/tool policy are checked', () => {
  const { verifier, binding, claims, signToken } = setup()
  assert.equal(verifier.verify(signToken(), binding, now + 299999).jti, claims.jti)
  assert.throws(() => verifier.verify(signToken(), binding, now + 300000), { message: 'agent_capability_expired' })
  rejected(() => verifier.verify(signToken(), binding, now - 1000))
  for (const edit of [{ exp: claims.exp + 1 }, { iss: 'other' }, { aud: [gatewayAudience] }, { allowed_tools: ['b', 'a'] }, { allowed_tools: ['a', 'a'] }, { allowed_tools: ['query_kline\n'] },
    { envelope: { ...claims.envelope, model: 'other' } }]) rejected(() => verifier.verify(signToken({ ...claims, ...edit }), binding, now))
})
test('TEST-RUN-AUTH-06 live policy is mandatory, immutable and rerun after revocation', () => {
  let active = true, checks = 0
  const { config, verifier, binding, signToken } = setup(claims => { checks++; assert.ok(Object.isFrozen(claims.envelope)); return active })
  assert.throws(() => createRunVerifier({ ...config, authorize: undefined }), { message: 'agent_capability_configuration_invalid' })
  assert.throws(() => createRunVerifier({ ...config, issuer: config.issuer + '\n' }), { message: 'agent_capability_configuration_invalid' })
  assert.throws(() => createRunVerifier({ ...config, keys: { 'key-1\n': config.keys['key-1'] } }), { message: 'agent_capability_configuration_invalid' })
  verifier.verify(signToken(), binding, now)
  active = false
  rejected(() => verifier.verify(signToken(), binding, now))
  rejected(() => verifier.verify('invalid', binding, now))
  assert.equal(checks, 2)
  for (const authorize of [() => { throw new Error('sensitive database details') }, async () => true, () => undefined]) {
    rejected(() => createRunVerifier({ ...config, authorize }).verify(signToken(), binding, now))
  }
})
test('TEST-RUN-AUTH-07 verifyPrompt binds actual content and returns only signed transport authority', () => {
  const { verifier, binding, signToken, claims } = setup()
  const request = { q4d_session_id: binding.session_id, run_id: binding.run_id, client_request_id: binding.client_request_id,
    request_hash: binding.request_hash, execution_envelope_digest: binding.execution_envelope_digest,
    run_capability: signToken(), content: [{ type: 'text', text: 'query' }] }
  const run = verifier.verifyPrompt(request, now)
  assert.equal(run.expiresAt, claims.exp * 1000)
  assert.deepEqual(run.allowedTools, ['query_kline'])
  rejected(() => verifier.verifyPrompt({ ...request, content: [{ type: 'text', text: 'changed' }] }, now))
  rejected(() => verifier.verifyPrompt({ ...request, content: [{ type: 'text', text: 'query', run_id: 'forged' }] }, now))
})
test('TEST-RUN-AUTH-08 trusted transport rechecks authorization before proposal and dispatch', async () => {
  let active = true
  const gateway = new TrustedGateway({ url: 'http://127.0.0.1:1/internal/mcp', runtimeToken: 'fixture-token', catalog: [queryTool] })
  const context = { sessionId: 'session-1', runId: 'run-1', capability: 'header.payload.signature', expiresAt: Date.now() + 60000, allowedTools: ['query_kline'] }
  assert.throws(() => gateway.beginRun('session-1', context), { message: 'agent_invalid_context' })
  gateway.beginRun('session-1', { ...context, authorize: () => active })
  const call = gateway.prepare('session-1', 'query_kline', { symbol: 'TEST', limit: 1 })
  active = false
  assert.throws(() => gateway.prepare('session-1', 'query_kline', { symbol: 'TEST', limit: 1 }), { message: 'agent_capability_rejected' })
  await assert.rejects(gateway.invoke(call), { message: 'agent_capability_rejected' })
  gateway.endRun('session-1', 'run-1')
})
