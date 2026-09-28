import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { setTimeout as delay } from 'node:timers/promises'
import test from 'node:test'
import { GatewayError, TrustedGateway } from '../../../../src/mcp/trusted-gateway.mjs'
import { gatewayFixture, queryTool } from '../../helpers/mcp-gateway.mjs'

async function setup(t, options = {}) {
  const gateway = await gatewayFixture(t)
  const adapter = new TrustedGateway({ ...gateway, catalog: [queryTool], ...options })
  const run = gateway.issueRun('run-1')
  adapter.beginRun('dsh-1', run)
  return { gateway, adapter, run }
}
const args = symbol => ({ symbol, limit: 2 })
const denied = code => ({ name: 'GatewayError', code })

test('TEST-MCP-HTTP-11 dispatch progress is bound to one request; approval and absent markers cannot claim started', async t => {
  const { adapter, gateway } = await setup(t)
  let starts = 0
  const call = adapter.prepare('dsh-1', 'query_kline', args('APPROVAL'))
  await adapter.invoke(call, { onStarted: () => starts++ })
  assert.equal(starts, 0)
  await adapter.invoke(call, { receipt: gateway.approve(call), onStarted: () => starts++ })
  assert.equal(starts, 1)
  const observed = gateway.attempts.at(-1)
  assert.ok(observed.body.params._meta.progressToken !== undefined)
  assert.deepEqual(observed.body.params.arguments, args('APPROVAL'))
  await adapter.invoke(call, { receipt: observed.headers['x-q4d-approval-receipt'], onStarted: () => starts++ })
  assert.equal(starts, 2)
  assert.equal(gateway.executions.length, 1)
  await assert.rejects(adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('NO_START')), {
    onStarted: () => starts++,
  }), denied('agent_tool_start_unconfirmed'))
  assert.equal(starts, 2)
})

test('TEST-MCP-HTTP-01 concurrent sessions carry their own signed Run context', async t => {
  const { gateway, adapter, run } = await setup(t)
  const second = gateway.issueRun('run-2')
  adapter.beginRun('dsh-2', second)
  const calls = [adapter.prepare('dsh-1', 'query_kline', args('000001')), adapter.prepare('dsh-2', 'query_kline', args('000002'))]
  await Promise.all(calls.map(call => adapter.invoke(call)))
  assert.equal(gateway.executions.length, 2)
  for (const [i, call] of calls.entries()) {
    const observed = gateway.attempts.find(item => item.headers['x-q4d-tool-call-id'] === call.tool_call_id)
    assert.equal(observed.headers['x-q4d-run-capability'], [run, second][i].capability)
    assert.deepEqual(observed.body.params, { name: 'query_kline', arguments: call.arguments })
    assert.equal(observed.headers['x-q4d-approval-receipt'], undefined)
    assert.ok(!JSON.stringify(call).includes([run, second][i].capability))
  }
  assert.notEqual(calls[0].tool_call_id, calls[1].tool_call_id)
})

test('TEST-MCP-HTTP-02 forged metadata, unknown tools and missing context make zero HTTP calls', async t => {
  const { adapter, gateway } = await setup(t)
  for (const field of ['actor_id', 'session_id', 'run_id', 'profile', 'tool_call_id', 'idempotency_key',
    'arguments_hash', 'approval_receipt', 'run_capability', 'runtime_token', '_meta']) {
    assert.throws(() => adapter.prepare('dsh-1', 'query_kline', { ...args('000001'), [field]: 'forged' }), denied('agent_invalid_arguments'))
  }
  assert.throws(() => adapter.prepare('missing', 'query_kline', args('000001')), denied('agent_run_context_missing'))
  assert.throws(() => adapter.prepare('dsh-1', 'shell', args('000001')), denied('agent_tool_forbidden'))
  for (const limit of [0, 501, 1.5]) assert.throws(() => adapter.prepare('dsh-1', 'query_kline', { symbol: '000001', limit }), denied('agent_invalid_arguments'))
  assert.throws(() => adapter.prepare('dsh-1', 'query_kline', { symbol: 'private-value', limit: 501 }), error => {
    assert.equal(error.code, 'agent_invalid_arguments')
    assert.deepEqual(error.validation, [{ path: '/limit', message: 'must be <= 500' }])
    assert.ok(!JSON.stringify(error).includes('private-value'))
    return true
  })
  await assert.rejects(adapter.invoke({ ...adapter.prepare('dsh-1', 'query_kline', args('000001')) }), denied('agent_unknown_tool_call'))
  assert.equal(gateway.attempts.length, 0)
  assert.equal(gateway.executions.length, 0)
})

test('TEST-MCP-HTTP-03 approval retry freezes arguments and identity; receipt cannot authorize another call', async t => {
  const { adapter, gateway } = await setup(t)
  const input = args('APPROVAL')
  const call = adapter.prepare('dsh-1', 'query_kline', input)
  input.symbol = '000001'
  const proposed = await adapter.invoke(call)
  assert.equal(proposed.structuredContent.error.code, 'agent_approval_required')
  assert.equal(gateway.executions.length, 0)
  const receipt = gateway.approve(call)
  await adapter.invoke(call, { receipt })
  await adapter.invoke(call, { receipt })
  assert.equal(gateway.executions.length, 1)
  assert.equal(new Set(gateway.attempts.map(item => item.headers['idempotency-key'])).size, 1)
  assert.ok(gateway.attempts.every(item => item.body.params.arguments.symbol === 'APPROVAL'))
  const other = adapter.prepare('dsh-1', 'query_kline', args('APPROVAL'))
  await assert.rejects(adapter.invoke(other, { receipt }), GatewayError)
  assert.equal(gateway.executions.length, 1)
  assert.throws(() => { call.arguments.symbol = '000001' }, TypeError)
})

test('TEST-MCP-HTTP-04 invalid, revoked and expired capabilities never execute a business tool', async t => {
  const { adapter, gateway, run } = await setup(t)
  gateway.revoke(run.capability)
  await assert.rejects(adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('000001'))), GatewayError)
  adapter.endRun('dsh-1', run.runId)
  const second = gateway.issueRun('run-2')
  adapter.beginRun('dsh-1', { ...second, capability: 'forged.signature' })
  await assert.rejects(adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('000001'))), GatewayError)
  assert.throws(() => adapter.beginRun('dsh-3', { ...gateway.issueRun('run-3'), expiresAt: Date.now() - 1 }), denied('agent_invalid_context'))
  assert.equal(gateway.executions.length, 0)
})

test('TEST-MCP-HTTP-05 ended Runs cannot be reused when the same DSH session starts another Run', async t => {
  const { adapter, gateway, run } = await setup(t)
  const oldCall = adapter.prepare('dsh-1', 'query_kline', args('000001'))
  assert.throws(() => adapter.beginRun('dsh-1', gateway.issueRun('busy')), denied('agent_run_in_progress'))
  assert.throws(() => adapter.endRun('dsh-1', 'wrong-run'), denied('agent_run_context_missing'))
  adapter.endRun('dsh-1', run.runId)
  const second = gateway.issueRun('run-2')
  adapter.beginRun('dsh-1', second)
  await assert.rejects(adapter.invoke(oldCall), denied('agent_run_context_missing'))
  await adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('000002')))
  assert.equal(gateway.executions.length, 1)
  assert.equal(gateway.executions[0].claims.runId, second.runId)
})

test('TEST-MCP-HTTP-06 cancellation stops an in-flight HTTP call without retry', async t => {
  const { adapter, gateway, run } = await setup(t)
  const call = adapter.prepare('dsh-1', 'query_kline', args('WAIT'))
  const pending = assert.rejects(adapter.invoke(call), denied('agent_tool_cancelled'))
  while (gateway.attempts.length === 0) await delay(5)
  await assert.rejects(adapter.invoke(call), denied('agent_tool_call_in_progress'))
  adapter.endRun('dsh-1', run.runId)
  await pending
  assert.equal(gateway.attempts.length, 1)
  assert.equal(gateway.executions.length, 0)
})

test('TEST-MCP-HTTP-07 redirects cannot send credentials to another HTTP endpoint', async t => {
  const { adapter, gateway } = await setup(t)
  let leakedRequests = 0
  const sink = createServer((_req, res) => { leakedRequests++; res.end('{}') })
  await new Promise(resolve => sink.listen(0, '127.0.0.1', resolve))
  t.after(() => new Promise(resolve => sink.close(resolve)))
  gateway.redirect = `http://127.0.0.1:${sink.address().port}/capture`
  await assert.rejects(adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('REDIRECT'))), denied('agent_tool_transport_error'))
  assert.equal(leakedRequests, 0)
  assert.equal(gateway.executions.length, 0)
})

test('TEST-MCP-HTTP-08 failed results stay failed; oversized responses are rejected', async t => {
  const { adapter } = await setup(t)
  const failed = await adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('FAIL')))
  assert.equal(failed.isError, true)
  await assert.rejects(adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('LARGE'))), GatewayError)
})

test('TEST-MCP-HTTP-09 capability expiry between prepare and dispatch sends no request', async t => {
  const { gateway, adapter, run } = await setup(t)
  adapter.endRun('dsh-1', run.runId)
  const expiring = gateway.issueRun('expiring-run', { expiresAt: Date.now() + 300 })
  adapter.beginRun('dsh-1', expiring)
  const call = adapter.prepare('dsh-1', 'query_kline', args('000001'))
  await delay(Math.max(0, expiring.expiresAt - Date.now()) + 10)
  await assert.rejects(adapter.invoke(call), denied('agent_capability_expired'))
  assert.equal(gateway.attempts.length, 0)
})

test('TEST-MCP-HTTP-10 streamed MCP response settles without waiting for SSE EOF', { timeout: 5000 }, async t => {
  const { gateway, adapter } = await setup(t, { timeoutMs: 1000 })
  const result = await adapter.invoke(adapter.prepare('dsh-1', 'query_kline', args('SSE')))
  assert.equal(result.isError, undefined)
  assert.equal(gateway.executions.length, 1)
})
