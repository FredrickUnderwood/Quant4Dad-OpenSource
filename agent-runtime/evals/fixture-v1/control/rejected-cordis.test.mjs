import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { fixture, request, sse } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'

async function setup(t, overrides = {}) {
  const gateway = await gatewayFixture(t)
  const env = await fixture(t)
  const launch = options => env.launch({ ...options, env: { Q4D_T00_GATEWAY_URL: gateway.url,
    Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken, ...options?.env } })
  const host = await launch(overrides)
  await host.call('create', { sessionId: 'session-1' })
  const run = gateway.issueRun('run-1', { sessionId: 'session-1' })
  return { gateway, env, launch, host, run }
}
const tools = events => events.filter(event => event.type.startsWith('tool.'))

for (const prompt of ['unknown-tool', 'forge-field:run_capability', 'invalid-json', 'invalid-array']) {
  test(`TEST-REJECT-01 ${prompt}: durable rejection, no start or HTTP, unchanged after restart`, { timeout: 20_000 }, async t => {
    const { gateway, host, run, launch } = await setup(t)
    const input = request(prompt)
    await host.call('prompt', { request: input, run })
    const events = await sse(await host.events('run-1'))
    assert.deepEqual(tools(events).map(event => event.type), ['tool.proposed', 'tool.failed'])
    assert.equal(tools(events)[1].data.code, prompt === 'unknown-tool' ? 'agent_tool_forbidden' : 'agent_tool_context_or_arguments_rejected')
    const proposed = tools(events)[0]
    assert.deepEqual(proposed.data.arguments, {})
    assert.equal(proposed.data.arguments_omitted, true)
    assert.equal(tools(events)[1].data.tool_call_id, proposed.data.tool_call_id)
    assert.equal(events.at(-1).type, 'run.completed')
    assert.equal(gateway.attempts.length, 0)
    const native = await host.call('inspect', { sessionId: 'session-1' })
    assert.equal(native.find(event => String(event.seq) === proposed.data.source_seq).type, 'tool/call')
    await host.close()
    const second = await launch()
    await second.call('resume', { sessionId: 'session-1' })
    assert.deepEqual(await sse(await second.events('run-1')), events)
    assert.equal(gateway.attempts.length, 0)
  })
}

for (const prompt of ['repeat-tool', 'mixed-tools']) {
  test(`TEST-REJECT-02 ${prompt}: reused model ID maps to distinct durable call sequences and ULIDs`, { timeout: 20_000 }, async t => {
    const { gateway, host, run, launch } = await setup(t)
    await host.call('prompt', { request: request(prompt), run })
    const events = await sse(await host.events('run-1'))
    const proposals = events.filter(event => event.type === 'tool.proposed')
    assert.equal(proposals.length, 2)
    assert.equal(new Set(proposals.map(event => event.data.tool_call_id)).size, 2)
    assert.equal(new Set(proposals.map(event => event.data.source_seq)).size, 2)
    const native = (await host.call('inspect', { sessionId: 'session-1' })).filter(event => event.type === 'tool/call')
    assert.equal(native.length, 2)
    assert.equal(new Set(native.map(event => event.data.callId)).size, 1)
    assert.deepEqual(proposals.map(event => event.data.source_seq), native.map(event => String(event.seq)))
    assert.equal(gateway.executions.length, prompt === 'repeat-tool' ? 2 : 1)
    assert.ok(events.filter(event => event.type === 'usage.updated').every(event => Number.isInteger(event.data.usage.input_tokens)))
    await host.close()
    const second = await launch()
    await second.call('resume', { sessionId: 'session-1' })
    assert.deepEqual(await sse(await second.events('run-1')), events)
  })
}

test('TEST-REJECT-03 SIGKILL after rejected proposal restores the known result with its original ID', { timeout: 20_000 }, async t => {
  const { gateway, host, run, launch, env } = await setup(t, { env: { Q4D_T00_CRASH_AFTER_REJECT_PROPOSED: '1' } })
  await host.call('prompt', { request: request('unknown-tool'), run }).catch(error => assert.match(error.message, /fixture exited/))
  assert.equal((await host.exited)[1], 'SIGKILL')
  const before = (await readFile(join(env.directory, 'events-run-1.jsonl'), 'utf8')).trim().split('\n').map(line => JSON.parse(line).data.event)
  const original = before.find(event => event.type === 'tool.proposed')
  assert.ok(original)
  const second = await launch()
  await second.call('resume', { sessionId: 'session-1' })
  const events = await sse(await second.events('run-1'))
  assert.deepEqual(events.slice(0, before.length), before)
  assert.equal(tools(events).length, 2)
  assert.equal(tools(events)[1].data.tool_call_id, original.data.tool_call_id)
  assert.equal(tools(events)[1].data.code, 'agent_tool_forbidden')
  assert.equal(gateway.attempts.length, 0)
  const nextRun = gateway.issueRun('run-2', { sessionId: 'session-1' })
  await second.call('prompt', { request: request('query', 'run-2'), run: nextRun })
  assert.equal((await sse(await second.events('run-2'))).at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
})

test('TEST-REJECT-04 cancellation before dispatch closes native Tool and Run without HTTP', { timeout: 20_000 }, async t => {
  const barrier = Promise.withResolvers()
  const { gateway, host, run } = await setup(t, { env: { Q4D_T00_PAUSE_BEFORE_TOOL: '1' },
    onEvent: event => { if (event.event === 'before_tool') barrier.resolve() } })
  await host.call('prompt', { request: request(), run })
  await barrier.promise
  await host.call('cancel', { sessionId: 'session-1', runId: 'run-1' })
  const events = await sse(await host.events('run-1'))
  assert.deepEqual(tools(events).map(event => event.type), ['tool.proposed', 'tool.failed'])
  assert.equal(tools(events)[1].data.code, 'agent_tool_cancelled')
  assert.equal(events.at(-1).type, 'run.cancelled')
  assert.deepEqual(events.at(-1).data, { reason: 'user' })
  assert.equal(gateway.attempts.length, 0)
})

test('TEST-REJECT-05 approval expires with a durable failed event and late receipt is rejected', { timeout: 20_000 }, async t => {
  const { gateway, host, run, launch } = await setup(t)
  gateway.approvalTtlMs = 500
  await host.call('prompt', { request: request('gateway-approval'), run })
  const events = await sse(await host.events('run-1'))
  const approval = events.find(event => event.type === 'approval.required')
  assert.equal(approval.data.risk, 'R3')
  assert.match(approval.data.arguments_hash, /^sha256:[0-9a-f]{64}$/)
  assert.ok(Date.parse(approval.data.expires_at) <= Date.now())
  assert.equal(tools(events).at(-1).data.code, 'agent_approval_timeout')
  assert.ok(!events.some(event => event.type === 'tool.started'))
  const proposal = events.find(event => event.type === 'tool.proposed')
  await assert.rejects(host.call('approvalDecision', { runId: 'run-1', toolCallId: proposal.data.tool_call_id,
    receipt: gateway.approve(proposal.data) }), /agent_approval_missing/)
  assert.equal(gateway.attempts.length, 1)
  assert.equal(gateway.executions.length, 0)
  await host.close()
  const second = await launch()
  await second.call('resume', { sessionId: 'session-1' })
  assert.deepEqual(await sse(await second.events('run-1')), events)
})

test('TEST-REJECT-06 malformed approval metadata never creates a waiting approval or exposes raw data', { timeout: 20_000 }, async t => {
  const { gateway, host, run } = await setup(t)
  gateway.approvalOverrides = { arguments_hash: 'sensitive-invalid-hash', risk: 'R1' }
  await host.call('prompt', { request: request('gateway-approval'), run })
  const events = await sse(await host.events('run-1'))
  assert.equal(tools(events).at(-1).data.code, 'agent_tool_protocol_error')
  assert.ok(!events.some(event => ['approval.required', 'tool.started'].includes(event.type)))
  assert.ok(!JSON.stringify(events).includes('sensitive-invalid-hash'))
  assert.equal(gateway.attempts.length, 1)
  assert.equal(gateway.executions.length, 0)
})
