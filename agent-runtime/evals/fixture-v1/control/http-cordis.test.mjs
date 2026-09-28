import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { fixture, request, sse, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'
import { sessionRequest } from '../../../fixtures/session-config.mjs'

const headers = { authorization: `Bearer ${bridgeToken}`, 'content-type': 'application/json' }
async function setup(t, text = 'query', fault) {
  const gateway = await gatewayFixture(t)
  const environment = await fixture(t)
  const launch = async (fault) => {
    const host = await environment.launch({ env: {
      Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken,
      Q4D_T00_CAPABILITY_PUBLIC_KEY: gateway.publicKey, ...(fault ? { Q4D_T00_HTTP_FAULT: fault } : {}),
    } })
    host.post = (path, body) => fetch(host.url + path, { method: 'POST', headers, body: JSON.stringify(body) })
    host.prompt = input => host.post(`/q4d/v1/sessions/${input.q4d_session_id}/prompts`, input)
    host.run = (id = 'run-1') => fetch(`${host.url}/q4d/v1/runs/${id}`, { headers })
    host.cancel = (id = 'run-1') => host.post(`/q4d/v1/runs/${id}/cancel`, {})
    host.decide = (approvalId, body) => host.post(`/q4d/v1/approvals/${encodeURIComponent(approvalId)}/decision`, body)
    return host
  }
  const host = await launch(fault)
  await json(await host.post('/q4d/v1/sessions', sessionRequest()))
  const issue = (text = 'query', id = 'run-1', overrides = {}) => {
    const input = request(text, id)
    return { ...input, run_capability: gateway.issueRun(id, { sessionId: 'session-1', ...overrides }).capability }
  }
  return { host, gateway, environment, launch, issue, input: issue(text) }
}

async function json(response, status = 200) {
  const body = await response.json()
  assert.equal(response.status, status, JSON.stringify(body))
  return body
}
async function error(response, status, code) {
  assert.deepEqual(await json(response, status), { error: { code } })
}
async function until(host, type, runId = 'run-1') {
  const response = await host.events(runId)
  assert.equal(response.status, 200)
  const reader = response.body.getReader()
  let buffer = ''
  const events = []
  try {
    for (;;) {
      const { value, done } = await reader.read()
      assert.equal(done, false, `stream ended before ${type}`)
      buffer += new TextDecoder().decode(value)
      while (buffer.includes('\n\n')) {
        const end = buffer.indexOf('\n\n')
        const block = buffer.slice(0, end)
        buffer = buffer.slice(end + 2)
        const data = block.split('\n').find(line => line.startsWith('data: '))
        if (!data) continue
        const event = JSON.parse(data.slice(6))
        events.push(event)
        if (event.type === type) return events
      }
    }
  } finally { await reader.cancel().catch(() => {}) }
}
async function noSecrets(directory, secrets) {
  const files = await readdir(directory, { recursive: true, withFileTypes: true })
  for (const file of files.filter(file => file.isFile())) {
    const content = await readFile(join(file.parentPath, file.name), 'utf8')
    for (const secret of secrets.filter(Boolean)) assert.ok(!content.includes(secret), `credential found in ${file.name}`)
  }
}

test('TEST-HTTP-DSH-01 parallel HTTP retries preserve one durable admission, query and replay after restart', { timeout: 20_000 }, async t => {
  const { host, gateway, environment, launch, input } = await setup(t)
  const responses = await Promise.all(Array.from({ length: 5 }, () => host.prompt(input).then(response => json(response, 202))))
  assert.ok(responses.every(result => JSON.stringify(result) === JSON.stringify(responses[0])))
  assert.equal(responses[0].durable, true)
  assert.equal(responses[0].state, 'accepted')
  const events = await sse(await host.events(input.run_id))
  assert.equal(events[0].data.message_id, responses[0].message_id)
  assert.equal(events.at(-1).type, 'run.completed')
  const status = await json(await host.run())
  assert.equal(status.state, 'completed')
  assert.equal(status.last_event_id, events.at(-1).id)
  assert.deepEqual(await json(await host.cancel()), status, 'late cancel cannot rewrite completion')
  assert.equal(gateway.executions.length, 1)
  await host.close()
  const second = await launch()
  assert.deepEqual(await json(await second.run()), status, 'completed Run can be queried cold')
  assert.deepEqual(await json(await second.prompt(input), 202), responses[0])
  assert.deepEqual(await sse(await second.events(input.run_id)), events)
  const raw = await second.call('inspect', { sessionId: 'session-1' })
  assert.equal(raw.filter(event => event.type === 'agent/inbox/spliced' && event.data.inserted.some(message => message.id === status.message_id)).length, 1)
  assert.equal(gateway.executions.length, 1)
  await noSecrets(environment.directory, [bridgeToken, gateway.runtimeToken, input.run_capability])
})

test('TEST-HTTP-DSH-02 busy concurrent submissions leave no rejected request intent and can retry after cancellation', { timeout: 20_000 }, async t => {
  const { host, gateway, environment, input, issue } = await setup(t, 'gateway-wait')
  await json(await host.prompt(input), 202)
  await until(host, 'tool.started')
  const inputs = [issue('query', 'run-2'), issue('query', 'run-3')]
  for (const response of await Promise.all(inputs.map(value => host.prompt(value)))) await error(response, 409, 'agent_run_in_progress')
  const index = await readFile(join(environment.directory, 'request-index.jsonl'), 'utf8')
  assert.ok(!index.includes('run-2') && !index.includes('run-3'))
  await error(await host.run('run-2'), 404, 'agent_run_not_found')
  await json(await host.cancel())
  assert.equal((await sse(await host.events(input.run_id))).at(-1).type, 'run.cancelled')
  await json(await host.prompt(inputs[0]), 202)
  assert.equal((await sse(await host.events('run-2'))).at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
})

test('TEST-HTTP-DSH-03 invalid Capability, hash and conflicting identities are rejected without execution', { timeout: 20_000 }, async t => {
  const { host, gateway, environment, input, issue } = await setup(t)
  const badInputs = [
    { ...input, run_capability: 'forged.invalid' },
    { ...input, run_capability: issue('query', 'other-run').run_capability },
    { ...input, run_capability: issue('query', 'run-1', { sessionId: 'other-session' }).run_capability },
    { ...input, run_capability: issue('query', 'run-1', { expiresAt: Date.now() - 1000 }).run_capability },
    { ...input, run_capability: issue('query', 'run-1', { allowedTools: ['shell'] }).run_capability },
  ]
  for (const body of badInputs) await error(await host.prompt(body), 403, 'agent_capability_rejected')
  const valid = issue()
  await error(await host.prompt({ ...valid, request_hash: 'sha256:' + '0'.repeat(64) }), 409, 'agent_request_conflict')
  assert.equal((await readFile(join(environment.directory, 'request-index.jsonl'), 'utf8')), '')
  assert.equal(gateway.attempts.length, 0)
  await json(await host.prompt(valid), 202)
  await sse(await host.events(valid.run_id))
  await error(await host.prompt({ ...valid, execution_envelope_digest: 'sha256:' + 'b'.repeat(64) }), 409, 'agent_request_conflict')
  await error(await host.prompt({ ...valid, client_request_id: 'changed' }), 409, 'agent_request_conflict')
  const reused = { ...issue('query', 'run-2'), client_request_id: valid.client_request_id }
  await error(await host.prompt(reused), 409, 'agent_request_conflict')
  assert.equal(gateway.executions.length, 1)
  await error(await host.run('run-2'), 404, 'agent_run_not_found')
})

for (const allow of [true, false]) {
  test(`TEST-HTTP-DSH-04 HTTP approval ${allow ? 'allow_once' : 'reject'} binds approval/Run/Tool and refuses reuse`, { timeout: 20_000 }, async t => {
    const { host, gateway, environment, input } = await setup(t, 'gateway-approval')
    await json(await host.prompt(input), 202)
    const prefix = await until(host, 'approval.required')
    const approval = prefix.at(-1).data
    const proposed = prefix.find(event => event.type === 'tool.proposed').data
    assert.equal((await json(await host.run())).state, 'waiting_approval')
    const body = { run_id: input.run_id, tool_call_id: approval.tool_call_id, decision: 'reject' }
    await error(await host.decide('other-approval', body), 409, 'agent_approval_missing')
    await error(await host.decide(approval.approval_id, { ...body, run_id: 'other-run' }), 409, 'agent_approval_missing')
    await error(await host.decide(approval.approval_id, { ...body, tool_call_id: '01K4M000000000000000000000' }), 409, 'agent_approval_missing')
    assert.equal(gateway.executions.length, 0)
    const receipt = allow ? gateway.approve(proposed) : undefined
    const answer = { ...body, decision: allow ? 'allow_once' : 'reject', ...(allow ? { approval_receipt: receipt } : {}) }
    const acknowledged = await json(await host.decide(approval.approval_id, answer))
    assert.equal(Object.hasOwn(acknowledged, 'approval_receipt'), false)
    await error(await host.decide(approval.approval_id, answer), 409, 'agent_approval_missing')
    const events = await sse(await host.events(input.run_id))
    assert.equal(events.at(-1).type, 'run.completed', 'reject lets the model explain; it does not cancel the Run')
    assert.equal(events.some(event => event.type === 'tool.started'), allow)
    assert.equal(gateway.executions.length, allow ? 1 : 0)
    await noSecrets(environment.directory, [input.run_capability, receipt, gateway.runtimeToken])
  })
}

test('TEST-HTTP-DSH-05 HTTP cancel is idempotent during live execution and durable after restart', { timeout: 20_000 }, async t => {
  const { host, launch, gateway, input } = await setup(t, 'gateway-wait')
  await json(await host.prompt(input), 202)
  await until(host, 'tool.started')
  assert.equal((await json(await host.run())).state, 'running')
  const cancelled = await Promise.all([host.cancel(), host.cancel()])
  for (const response of cancelled) assert.ok(['cancelling', 'cancelled'].includes((await json(response)).state))
  const events = await sse(await host.events(input.run_id))
  assert.equal(events.filter(event => event.type === 'run.cancelled').length, 1)
  assert.equal(events.at(-1).type, 'run.cancelled')
  assert.equal(gateway.executions.length, 0)
  await host.close()
  const second = await launch()
  assert.equal((await json(await second.run())).state, 'cancelled')
  assert.equal((await json(await second.cancel())).state, 'cancelled')
  assert.deepEqual(await sse(await second.events(input.run_id)), events)
})

for (const fault of ['after_intent', 'after_inbox_flush']) {
  test(`TEST-HTTP-DSH-06 HTTP response loss at ${fault} reconciles the same message boundary`, { timeout: 20_000 }, async t => {
    const { host, launch, input } = await setup(t, 'query', fault)
    await assert.rejects(host.prompt(input), /fetch failed/)
    assert.equal((await host.exited)[1], 'SIGKILL')
    const second = await launch()
    const cold = await json(await second.run())
    assert.equal(cold.state, 'recovering')
    assert.equal(cold.terminal, false)
    await error(await second.cancel(), 409, 'agent_run_recovering')
    const accepted = await json(await second.prompt(input), 202)
    assert.equal(accepted.message_id, cold.message_id)
    const events = await sse(await second.events(input.run_id))
    assert.equal(events[0].type, 'run.started')
    assert.ok(['run.completed', 'run.interrupted'].includes(events.at(-1).type))
    const raw = await second.call('inspect', { sessionId: 'session-1' })
    assert.equal(raw.filter(event => event.type === 'agent/inbox/spliced' && event.data.inserted.some(message => message.id === cold.message_id)).length, 1)
    assert.deepEqual(await json(await second.prompt(input), 202), accepted)
  })
}
