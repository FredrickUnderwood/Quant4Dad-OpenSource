import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { join } from 'node:path'
import test, { before, after } from 'node:test'
import { fixture, sse, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'
import { authorizationHarness, compileAuthorizationHarness } from '../helpers/go-authorization.mjs'
import { sessionRequest, fixtureProfileRevision } from '../../../fixtures/session-config.mjs'
import { executionEnvelopeDigest, promptRequestHash } from '../../../src/auth/run-capability.mjs'

let compiled
before(async () => { compiled = await compileAuthorizationHarness() }, { timeout: 120_000 })
after(async () => { await compiled?.close() })
const hash = 'sha256:' + 'a'.repeat(64)
const headers = { authorization: `Bearer ${bridgeToken}`, 'content-type': 'application/json' }
async function json(response, expected = 200) { const value = await response.json(); assert.equal(response.status, expected, 'unexpected HTTP status'); return value }

async function setup(t, profile = 'research') {
  const go = await authorizationHarness(t, compiled.binary)
  let verifiedCalls = 0
  const gateway = await gatewayFixture(t, { verifyCapability: async (token, run, tool) => {
    verifiedCalls++
    return go.call('verify', { token, binding: run.binding, tool })
  } })
  const environment = await fixture(t)
  const session = sessionRequest('session-1', { profile, profile_revision: fixtureProfileRevision(profile) })
  const policies = {}
  const issued = []
  const issue = async (text = 'query', runId = 'run-1', edits = {}, tools = profile === 'fixture_empty' ? [] : ['query_kline']) => {
    const envelope = { run_id: runId, client_request_id: `client-${runId}`, actor_id: 'local-user', q4d_version: '0.0.0',
      agent_image_digest: hash, agent_runtime_version: 'fixture-v1', bridge_protocol: 1, dsh_version: '0.1.2-alpha.5',
      adapter_version: '0.0.0-t00', product_profile: profile, profile_revision: session.profile_revision,
      prompt_bundle_digest: hash, skills_digest: hash, tool_catalog_revision: hash, provider: session.provider, model: session.model,
      model_config_revision: session.model_config_revision,
      budgets: { max_turns: 12, max_tool_calls: 20, max_input_tokens: 120000, max_output_tokens: 8192, wall_time_ms: 300000 }, ...edits }
    const signed = await go.call('issue', { session_id: session.q4d_session_id, text, envelope, allowed_tools: tools })
    assert.equal(signed.claims.execution_envelope_digest, executionEnvelopeDigest(envelope))
    assert.equal(signed.claims.request_hash, promptRequestHash(session.q4d_session_id, text))
    gateway.registerSignedRun(signed)
    policies[runId] = signed.claims
    issued.push(signed)
    return { q4d_session_id: session.q4d_session_id, run_id: runId, client_request_id: envelope.client_request_id,
      request_hash: signed.claims.request_hash, execution_envelope_digest: signed.claims.execution_envelope_digest,
      run_capability: signed.token, content: [{ type: 'text', text }] }
  }
  const launch = async () => {
    const host = await environment.launch({ env: { Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken,
      Q4D_T00_AUTHORIZATION: JSON.stringify({ keys: go.keys, issuer: go.issuer, policies }) } })
    host.post = (path, body) => fetch(host.url + path, { method: 'POST', headers, body: JSON.stringify(body) })
    host.prompt = input => host.post(`/q4d/v1/sessions/${input.q4d_session_id}/prompts`, input)
    await json(await host.post('/q4d/v1/sessions', session))
    return host
  }
  const noSecrets = async () => {
    const files = await readdir(environment.directory, { recursive: true, withFileTypes: true })
    for (const file of files.filter(file => file.isFile())) {
      const content = await readFile(join(file.parentPath, file.name), 'utf8')
      for (const secret of [bridgeToken, gateway.runtimeToken, ...issued.map(value => value.token)]) assert.ok(!content.includes(secret), 'credential persisted')
    }
  }
  return { go, gateway, environment, session, issue, launch, policies, noSecrets, verifiedCalls: () => verifiedCalls }
}

async function until(host, type) {
  const response = await host.events('run-1')
  assert.equal(response.status, 200)
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  const events = []
  try {
    for (;;) {
      const { value, done } = await reader.read(); assert.equal(done, false, 'stream ended before target event')
      buffer += decoder.decode(value, { stream: true })
      while (buffer.includes('\n\n')) {
        const index = buffer.indexOf('\n\n'), block = buffer.slice(0, index); buffer = buffer.slice(index + 2)
        const line = block.split('\n').find(line => line.startsWith('data: ')); if (!line) continue
        const event = JSON.parse(line.slice(6)); events.push(event)
        if (event.type === type) return events
      }
    }
  } finally { await reader.cancel().catch(() => {}) }
}

test('TEST-RUN-AUTH-HTTP-01 Go issues, Runtime verifies and Go independently authorizes real Cordis dispatch', { timeout: 25_000 }, async t => {
  const setupValue = await setup(t)
  const { issue, launch, gateway, verifiedCalls, noSecrets } = setupValue
  const input = await issue()
  const host = await launch()
  const ack = await json(await host.prompt(input), 202)
  const events = await sse(await host.events('run-1'))
  assert.equal(events[0].data.execution_envelope_digest, input.execution_envelope_digest)
  assert.equal(events.at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
  assert.equal(verifiedCalls(), 1)
  await noSecrets()
  await host.close()
  const cold = await launch()
  assert.deepEqual(await json(await cold.prompt(input), 202), ack)
  assert.deepEqual(await sse(await cold.events('run-1')), events)
  assert.equal(gateway.executions.length, 1)
  assert.equal(verifiedCalls(), 1)
  await noSecrets()
})

test('long-run budgets pass Go issuance, Runtime admission and tool gateway authorization', { timeout: 25_000 }, async t => {
  const { issue, launch, gateway, verifiedCalls, noSecrets } = await setup(t)
  const budgets = JSON.parse(await readFile(new URL('../../../fixtures/long-run-budgets.json', import.meta.url), 'utf8'))
  const input = await issue('query', 'run-1', { budgets })
  const host = await launch()
  await json(await host.prompt(input), 202)
  assert.equal((await sse(await host.events('run-1'))).at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
  assert.equal(verifiedCalls(), 1)
  await noSecrets()
})

test('TEST-RUN-AUTH-HTTP-02 tampering and signed Session/config/artifact/Tool mismatches have zero admission', { timeout: 25_000 }, async t => {
  const { issue, launch, gateway, environment, noSecrets } = await setup(t)
  const valid = await issue()
  const inputs = [
    { ...valid, content: [{ type: 'text', text: 'changed' }] },
    { ...valid, content: [{ type: 'text', text: 'changed' }], request_hash: promptRequestHash('session-1', 'changed') },
    { ...valid, client_request_id: 'other' }, { ...valid, execution_envelope_digest: 'sha256:' + 'b'.repeat(64) },
    { ...valid, run_capability: valid.run_capability + '=' },
  ]
  for (const [i, edit] of [{ provider: 'other' }, { model: 'other' }, { product_profile: 'other' },
    { profile_revision: hash }, { model_config_revision: 'stale' }, { adapter_version: 'other' }, { dsh_version: 'other' }].entries()) {
    inputs.push(await issue('query', `bad-${i}`, edit))
  }
  inputs.push(await issue('query', 'bad-tools', {}, ['query_kline', 'shell']))
  const host = await launch()
  for (const input of inputs) assert.deepEqual(await json(await host.prompt(input), 403), { error: { code: 'agent_capability_rejected' } })
  assert.equal(await readFile(join(environment.directory, 'request-index.jsonl'), 'utf8'), '')
  assert.equal(gateway.attempts.length, 0)
  assert.equal(gateway.executions.length, 0)
  await json(await host.prompt(valid), 202)
  assert.equal((await sse(await host.events('run-1'))).at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
  await noSecrets()
})

for (const mode of ['revoke', 'stale_model', 'runtime_revoke']) {
  test(`TEST-RUN-AUTH-HTTP-03 ${mode} after approval challenge blocks the receipt retry`, { timeout: 25_000 }, async t => {
    const { go, issue, launch, gateway, verifiedCalls, noSecrets } = await setup(t)
    const input = await issue('gateway-approval')
    const host = await launch()
    await json(await host.prompt(input), 202)
    const prefix = await until(host, 'approval.required')
    const proposed = prefix.find(event => event.type === 'tool.proposed').data
    const approval = prefix.at(-1).data
    if (mode === 'runtime_revoke') await host.call('revokeAuthorization', { runId: 'run-1' })
    else await go.call(mode, { run_id: 'run-1' })
    const receipt = gateway.approve(proposed)
    await json(await host.post(`/q4d/v1/approvals/${encodeURIComponent(approval.approval_id)}/decision`, {
      run_id: 'run-1', tool_call_id: approval.tool_call_id, decision: 'allow_once', approval_receipt: receipt,
    }))
    const events = await sse(await host.events('run-1'))
    assert.equal(events.at(-1).type, 'run.completed')
    assert.equal(events.some(event => event.type === 'tool.started'), false)
    assert.ok(events.some(event => event.type === 'tool.failed'))
    assert.equal(gateway.executions.length, 0)
    assert.equal(verifiedCalls(), mode === 'runtime_revoke' ? 1 : 2)
    await noSecrets()
  })
}

test('TEST-RUN-AUTH-HTTP-04 signed empty Profile stays tool-free', { timeout: 25_000 }, async t => {
  const { issue, launch, gateway, noSecrets } = await setup(t, 'fixture_empty')
  const input = await issue('no-tools')
  const host = await launch()
  await json(await host.prompt(input), 202)
  const events = await sse(await host.events('run-1'))
  assert.equal(events.at(-1).type, 'run.completed')
  assert.equal(events.some(event => event.type.startsWith('tool.')), false)
  assert.equal(gateway.attempts.length, 0)
  await noSecrets()
})
