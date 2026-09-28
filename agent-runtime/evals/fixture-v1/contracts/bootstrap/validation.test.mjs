import test from 'node:test'
import assert from 'node:assert/strict'
import { decodeBootstrap, validateBootstrap } from '../../../../src/bootstrap/validation.mjs'
import { clone, fixture } from './helpers.mjs'

const reject = fn => assert.throws(fn, { message: 'agent_bootstrap_invalid' })

test('research and strategy catalogs admit read-only analyze_kline; pipeline and elevated risks remain forbidden', () => {
  for (const profile of ['research', 'strategy_lab']) {
    const value = clone()
    value.tool_catalogs = [{ profile, revision: 'sha256:' + 'a'.repeat(64), tools: [{ name: 'analyze_kline',
      description: 'fixture', inputSchema: { type: 'object' }, outputSchema: { type: 'object' },
      risk: 'R0', timeout_ms: 5000, max_result_bytes: 131072 }] }]
    assert.equal(validateBootstrap(value).tool_catalogs[0].tools[0].name, 'analyze_kline')
    for (const risk of ['R1', 'R2', 'R3']) {
      const bad = structuredClone(value); bad.tool_catalogs[0].tools[0].risk = risk
      reject(() => validateBootstrap(bad))
    }
    value.tool_catalogs[0].profile = 'pipeline_builder'
    reject(() => validateBootstrap(value))
  }
})

test('TEST-BOOTSTRAP-RESEARCH optional catalog admits only four sorted R0 market definitions', () => {
  const value = clone()
  value.tool_catalog = { profile: 'research', revision: 'sha256:' + 'a'.repeat(64),
    tools: ['get_instrument', 'latest_bar_date', 'list_instruments', 'query_kline'].map(name => ({ name,
      description: 'fixture', inputSchema: { type: 'object' }, outputSchema: { type: 'object' }, risk: 'R0', timeout_ms: 5000, max_result_bytes: 131072 })) }
  const accepted = validateBootstrap(value)
  assert.equal(accepted.tool_catalog.tools.length, 4)
  assert.throws(() => { accepted.tool_catalog.tools[0].inputSchema.type = 'array' }, TypeError)
  for (const mutate of [v => { v.tool_catalog.tools.reverse() }, v => { v.tool_catalog.tools[0].name = 'create_strategy' },
    v => { v.tool_catalog.tools[0].risk = 'R2' }, v => { v.tool_catalog.tools[0].max_result_bytes++ },
    v => { v.tool_catalog.tools[0].runtime_token = 'forged' }, v => { v.tool_catalog.profile = 'strategy_lab' }]) {
    const bad = structuredClone(value); mutate(bad); reject(() => validateBootstrap(bad))
  }
})

test('TEST-BOOTSTRAP-VALIDATE-01 shared Go wire is immutable and explicit empty/keyless replacement is valid', () => {
  const input = clone()
  const value = decodeBootstrap(Buffer.from(JSON.stringify(input)))
  assert.deepEqual(value, fixture)
  input.providers[0].api_key = 'different'
  assert.equal(value.providers[0].api_key, fixture.providers[0].api_key)
  assert.throws(() => { value.capability.public_keys['key-1'] = 'other' }, TypeError)
  assert.throws(() => { value.providers.push({}) }, TypeError)
  input.providers = []
  assert.deepEqual(validateBootstrap(input).providers, [])
  const keyless = clone()
  keyless.providers[0].api_key = ''
  keyless.providers[0].base_url = ''
  keyless.providers[0].agent.reasoning_effort = 'high'
  assert.equal(validateBootstrap(keyless).providers[0].api_key, '')
})

test('TEST-BOOTSTRAP-VALIDATE-02 semantic checks reject mismatched revisions, protocols, budgets and canonical key bits', () => {
  const changes = [
    v => { v.revision = '0'.repeat(32) + '.' + '0'.repeat(32) },
    v => { v.providers.push(structuredClone(v.providers[0])) },
    v => { v.providers.push({ ...v.providers[0], id: 'aaa' }) },
    v => { v.providers[0].agent.protocol = 'anthropic-messages' },
    v => { v.providers[0].agent.max_output_tokens = v.providers[0].agent.context_window },
    v => { v.providers[0].api_key = '\ud800' },
    v => { v.providers[0].agent.reasoning_effort = '\udfff' },
    v => { v.providers[0].agent.enabled = false },
    v => { v.providers[0].credential_revoked = true },
    v => { v.control_token = 'forbidden' },
    v => { v.capability.public_keys['key-1'] = v.capability.public_keys['key-1'].slice(0, -1) + 'l' },
    v => { v.capability.public_keys['key-1'] += '=' },
  ]
  for (const change of changes) { const value = clone(); change(value); reject(() => validateBootstrap(value)) }
  const both = clone()
  both.providers.push({ ...structuredClone(both.providers[0]), id: 'second', type: 'anthropic',
    agent: { ...both.providers[0].agent, protocol: 'anthropic-messages' } })
  assert.equal(validateBootstrap(both).providers.length, 2)
})

test('TEST-BOOTSTRAP-VALIDATE-03 URL parsing never silently repairs control targets or credential-bearing endpoints', () => {
  for (const url of ['http://mcp/internal/mcp?', 'http://mcp/internal/mcp#', 'http://user@mcp/internal/mcp',
    'http://@mcp/internal/mcp', 'http://mcp/x/../internal/mcp', 'http://mcp/internal/%6dcp',
    'http:\\mcp\\internal\\mcp', 'http:///mcp/internal/mcp', 'http://mcp/internal/mcp\n',
    'http://mcp/internal/mcp?token=secret', 'file:///internal/mcp']) {
    const value = clone(); value.mcp.url = url; reject(() => validateBootstrap(value))
  }
  for (const url of ['http://user:secret@model/v1', 'https://model/v1?', 'https://model/v1#',
    'https://model/v1?key=secret', 'https://model/white space', 'https://model/\ud800']) {
    const value = clone(); value.providers[0].base_url = url; reject(() => validateBootstrap(value))
  }
  const ipv6 = clone(); ipv6.mcp.url = 'http://[::1]:8081/internal/mcp'
  assert.equal(validateBootstrap(ipv6).mcp.url, ipv6.mcp.url)
})

test('TEST-BOOTSTRAP-VALIDATE-04 duplicate escaped names, malformed UTF-8, surrogates, BOM, depth and size fail without echo', () => {
  const text = JSON.stringify(fixture)
  const raws = [text.replace('"revision":', '"revision":"ignored","revi\\u0073ion":'),
    text.replace('"api_key":', '"api_key":"secret","api_key":'),
    text.replace('"key-1":', '"key-1":"secret","key-1":'),
    text + '{}', '\ufeff' + text, '[', '['.repeat(30) + '0' + ']'.repeat(30)]
  for (const raw of raws) reject(() => decodeBootstrap(Buffer.from(raw)))
  reject(() => decodeBootstrap(Buffer.from([0xff])))
  reject(() => decodeBootstrap(Buffer.alloc(1024 * 1024 + 1)))
  const escaped = text.replace('fixture-model-private-key', 'quote\\"brace}colon:comma,slash\\\\unicode\\u0061')
  assert.equal(decodeBootstrap(Buffer.from(escaped)).providers[0].api_key, 'quote"brace}colon:comma,slash\\unicodea')
})
