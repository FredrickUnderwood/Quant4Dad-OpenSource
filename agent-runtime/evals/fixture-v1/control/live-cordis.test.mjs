import assert from 'node:assert/strict'
import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { fixture, request, sse, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'

async function setup(t) {
  const gateway = await gatewayFixture(t)
  const environment = await fixture(t)
  const launch = () => environment.launch({ env: {
    Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken,
  } })
  const host = await launch()
  await host.call('create', { sessionId: 'session-1' })
  const input = request()
  const run = gateway.issueRun(input.run_id, { sessionId: input.q4d_session_id })
  return { gateway, environment, host, launch, input, run }
}

function stream(t, response) {
  assert.equal(response.status, 200)
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffered = ''
  t.after(() => reader.cancel().catch(() => {}))
  const events = []
  return {
    events,
    close: () => reader.cancel(),
    async next() {
      while (!buffered.includes('\n\n')) {
        const { value, done } = await reader.read()
        assert.equal(done, false, `stream ended; observed ${events.map(event => event.type)}`)
        buffered += decoder.decode(value, { stream: true })
      }
      const end = buffered.indexOf('\n\n')
      const block = buffered.slice(0, end)
      buffered = buffered.slice(end + 2)
      if (block.startsWith(':')) return this.next()
      const lines = block.split('\n')
      const event = JSON.parse(lines.find(line => line.startsWith('data: ')).slice(6))
      assert.equal(lines.find(line => line.startsWith('id: ')).slice(4), event.id)
      assert.equal(lines.find(line => line.startsWith('event: ')).slice(7), event.type)
      events.push(event)
      return event
    },
    async until(type) {
      for (;;) { const event = await this.next(); if (event.type === type) return event }
    },
  }
}

async function persisted(directory) {
  const files = await readdir(directory, { recursive: true, withFileTypes: true })
  return (await Promise.all(files.filter(file => file.isFile()).map(file => readFile(join(file.parentPath, file.name), 'utf8')))).join('\n')
}
const toolTypes = events => events.filter(event => event.type.startsWith('tool.') || event.type === 'approval.required').map(event => event.type)

for (const allow of [true, false]) {
  test(`TEST-LIVE-01 approval ${allow ? 'allow' : 'reject'}: early durable ack, live reconnect and restart preserve one logical Tool`, { timeout: 20_000 }, async t => {
    const { host, gateway, environment, launch, run } = await setup(t)
    const input = request('gateway-approval')
    const [accepted, retried] = await Promise.all([
      host.call('prompt', { request: input, run }), host.call('prompt', { request: input, run }),
    ])
    assert.deepEqual(retried, accepted)
    assert.equal(accepted.durable, true)
    assert.equal(accepted.state, 'accepted')
    const live = stream(t, await host.events(input.run_id))
    const proposed = await live.until('tool.proposed')
    assert.equal(live.events[0].type, 'run.started')
    const approval = await live.until('approval.required')
    assert.equal(gateway.executions.length, 0)
    assert.equal(live.events.some(event => event.type === 'tool.started'), false)
    await live.close()
    const reconnected = stream(t, await host.events(input.run_id, proposed.id))
    assert.deepEqual(await reconnected.next(), approval)
    await assert.rejects(host.call('approvalDecision', { runId: 'other-run', toolCallId: proposed.data.tool_call_id }), /agent_approval_missing/)
    const receipt = allow ? gateway.approve(proposed.data) : undefined
    await host.call('approvalDecision', { runId: input.run_id, toolCallId: proposed.data.tool_call_id, receipt })
    await reconnected.until('run.completed')
    const all = [...live.events.slice(0, -1), ...reconnected.events]
    assert.deepEqual(all.map(event => event.id), all.map((_event, i) => String(i + 1)))
    assert.deepEqual(toolTypes(all), allow
      ? ['tool.proposed', 'approval.required', 'tool.started', 'tool.completed']
      : ['tool.proposed', 'approval.required', 'tool.failed'])
    assert.equal(new Set(all.filter(event => event.data.tool_call_id).map(event => event.data.tool_call_id)).size, 1)
    assert.equal(gateway.executions.length, allow ? 1 : 0)
    assert.equal(gateway.attempts.length, allow ? 2 : 1)
    assert.ok(all.some(event => event.type === 'message.delta' && event.data.text === 'Q4D_T00_OK'))
    assert.ok(all.some(event => event.type === 'usage.updated'))
    if (!allow) assert.equal(all.find(event => event.type === 'tool.failed').data.code, 'agent_approval_denied')
    const cap = await (await fetch(`${host.url}/q4d/v1/capabilities`, { headers: { authorization: `Bearer ${bridgeToken}` } })).json()
    assert.equal(cap.features.cancel, true)
    assert.equal(cap.features.approval, true)
    assert.equal(cap.features.raw_provider_delta, false)
    await host.close()
    const second = await launch()
    await second.call('resume', { sessionId: input.q4d_session_id })
    assert.deepEqual(await second.call('prompt', { request: input }), accepted)
    assert.deepEqual(await sse(await second.events(input.run_id)), all)
    assert.deepEqual(await sse(await second.events(input.run_id, all.at(-1).id)), [])
    const content = await persisted(environment.directory)
    for (const secret of [gateway.runtimeToken, run.capability, receipt].filter(Boolean)) assert.ok(!content.includes(secret))
    const raw = await second.call('inspect', { sessionId: input.q4d_session_id })
    assert.equal(raw.filter(event => event.type === 'user/message' && event.data.id === accepted.message_id).length, 1)
    assert.equal(gateway.executions.length, allow ? 1 : 0)
  })
}

for (const waitingFor of ['approval.required', 'tool.started']) {
  test(`TEST-LIVE-02 cancellation during ${waitingFor} persists one cancelled terminal and rejects late approval`, { timeout: 20_000 }, async t => {
    const { host, gateway, launch, run } = await setup(t)
    const input = request(waitingFor === 'approval.required' ? 'gateway-approval' : 'gateway-wait')
    await host.call('prompt', { request: input, run })
    const live = stream(t, await host.events(input.run_id))
    await live.until(waitingFor)
    await assert.rejects(host.call('cancel', { runId: input.run_id, sessionId: 'wrong-session' }), /agent_run_not_found/)
    await Promise.all([host.call('cancel', { runId: input.run_id, sessionId: input.q4d_session_id }),
      host.call('cancel', { runId: input.run_id, sessionId: input.q4d_session_id })])
    await live.until('run.cancelled')
    assert.equal(live.events.filter(event => event.type === 'tool.failed').length, 1)
    assert.equal(live.events.find(event => event.type === 'tool.failed').data.code, 'agent_tool_cancelled')
    assert.equal(live.events.filter(event => /^run\.(completed|cancelled|failed|interrupted)$/.test(event.type)).length, 1)
    const proposed = live.events.find(event => event.type === 'tool.proposed')
    await assert.rejects(host.call('approvalDecision', { runId: input.run_id, toolCallId: proposed.data.tool_call_id }), /agent_approval_missing/)
    assert.equal(gateway.attempts.length, 1)
    assert.equal(gateway.executions.length, 0)
    await host.close()
    const second = await launch()
    await second.call('resume', { sessionId: input.q4d_session_id })
    assert.deepEqual(await sse(await second.events(input.run_id)), live.events)
  })
}

for (const waitingFor of ['approval.required', 'tool.started', 'tool.completed']) {
  test(`TEST-LIVE-03 SIGKILL after ${waitingFor} never redispatches a Tool or restores a secret`, { timeout: 20_000 }, async t => {
    const { host, gateway, environment, launch, run } = await setup(t)
    const input = request(waitingFor === 'approval.required' ? 'gateway-approval'
      : waitingFor === 'tool.started' ? 'gateway-wait' : 'cancel after tool')
    const accepted = await host.call('prompt', { request: input, run })
    const live = stream(t, await host.events(input.run_id))
    await live.until(waitingFor)
    await live.close()
    const callsBefore = gateway.attempts.length
    await host.kill()
    const second = await launch()
    await second.call('resume', { sessionId: input.q4d_session_id })
    const recovered = await sse(await second.events(input.run_id))
    assert.deepEqual(recovered.slice(0, live.events.length), live.events)
    assert.equal(recovered.at(-1).type, 'run.interrupted')
    assert.equal(gateway.attempts.length, callsBefore)
    assert.deepEqual(await second.call('prompt', { request: input }), accepted)
    if (waitingFor !== 'tool.completed') {
      assert.equal(recovered.find(event => event.type === 'tool.failed').data.code, 'agent_tool_outcome_unknown')
    } else assert.equal(recovered.some(event => event.type === 'tool.failed'), false)
    const next = request('query after crash', 'run-2')
    await second.call('prompt', { request: next, run: gateway.issueRun(next.run_id, { sessionId: next.q4d_session_id }) })
    const nextEvents = await sse(await second.events(next.run_id))
    assert.deepEqual(toolTypes(nextEvents), ['tool.proposed', 'tool.started', 'tool.completed'])
    assert.equal(nextEvents.at(-1).type, 'run.completed')
    const content = await persisted(environment.directory)
    assert.ok(!content.includes(run.capability))
    assert.ok(!content.includes(gateway.runtimeToken))
  })
}

test('TEST-LIVE-04 Tool failure has a trusted start and failed result; model can finish the Run', { timeout: 20_000 }, async t => {
  const { host, gateway, run } = await setup(t)
  await host.call('prompt', { request: request('failure'), run })
  const events = await sse(await host.events(run.runId))
  assert.deepEqual(toolTypes(events), ['tool.proposed', 'tool.started', 'tool.failed'])
  assert.equal(events.at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
})

test('TEST-LIVE-05 model failure closes a Run; terminal cancel and next Run cannot reuse or corrupt its context', { timeout: 20_000 }, async t => {
  const { host, gateway, run } = await setup(t)
  const first = request('model-error')
  await host.call('prompt', { request: first, run })
  const failed = await sse(await host.events(run.runId))
  assert.deepEqual(failed.map(event => event.type), ['run.started', 'run.failed'])
  await host.call('cancel', { runId: run.runId, sessionId: first.q4d_session_id })
  const second = request('query', 'run-2')
  await host.call('prompt', { request: second, run: gateway.issueRun(second.run_id, { sessionId: second.q4d_session_id }) })
  const completed = await sse(await host.events(second.run_id))
  assert.equal(completed.at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
  assert.equal(gateway.executions[0].claims.runId, second.run_id)
  assert.deepEqual(await sse(await host.events(first.run_id)), failed)
})
