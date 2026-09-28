import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import test from 'node:test'
import { methods } from '@agentclientprotocol/sdk'
import { launch, fixtureDirectory, mcpServers, prompt } from '../helpers/acp-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'

async function setup(t, onEvent) {
  const directory = await fixtureDirectory(t)
  const gateway = await gatewayFixture(t)
  const host = await launch(t, directory, undefined, {
    env: { Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken },
    onEvent: (event, host) => onEvent?.(event, host, gateway),
  })
  const session = await host.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  return { directory, gateway, host, sessionId: session.sessionId }
}
async function begin(host, gateway, dshSessionId, runId, overrides) {
  const run = gateway.issueRun(runId, overrides)
  await host.control('beginRun', { dshSessionId, run })
  return run
}
async function noGenericCalls(directory) {
  await assert.rejects(readFile(join(directory, 'calls.jsonl')), { code: 'ENOENT' })
}
function lastToolResult(host) {
  return host.updates.findLast(({ update }) => update.sessionUpdate === 'tool_call_update')?.update
}

test('TEST-DSH-08A actual DSH calls use per-Run HTTP metadata and independent logical IDs', { timeout: 20_000 }, async t => {
  const { host, gateway, directory, sessionId } = await setup(t)
  const second = await host.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  const firstRun = await begin(host, gateway, sessionId, 'run-1')
  const secondRun = await begin(host, gateway, second.sessionId, 'run-2')
  const outcomes = await Promise.all([prompt(host, sessionId, 'first query'), prompt(host, second.sessionId, 'second query')])
  assert.ok(outcomes.every(result => result.stopReason === 'end_turn'))
  assert.equal(gateway.executions.length, 2)
  assert.equal(lastToolResult(host).status, 'completed')
  assert.deepEqual(new Set(gateway.executions.map(call => call.claims.runId)), new Set(['run-1', 'run-2']))
  assert.equal(new Set(gateway.executions.map(call => call.id)).size, 2)
  assert.ok(gateway.executions.every(call => call.id !== 'q4d-query'))
  assert.ok(host.updates.filter(({ update }) => update.sessionUpdate === 'tool_call').every(({ update }) => update.toolCallId === 'q4d-query'))

  await host.control('endRun', { dshSessionId: sessionId, runId: firstRun.runId })
  const thirdRun = await begin(host, gateway, sessionId, 'run-3')
  await prompt(host, sessionId, 'query with a new capability')
  assert.equal(gateway.executions.at(-1).claims.runId, 'run-3')
  assert.equal(new Set(gateway.executions.map(call => call.id)).size, 3)
  await noGenericCalls(directory)
  await host.close()

  // Inspect every persisted fixture artifact, including actual model input.
  // Capability/token/receipt values must remain outside DSH/model history.
  for (const entry of await readdir(directory, { recursive: true })) {
    if (!entry.endsWith('.jsonl')) continue
    const stored = await readFile(join(directory, entry), 'utf8')
    for (const secret of [gateway.runtimeToken, firstRun.capability, secondRun.capability, thirdRun.capability]) {
      assert.equal(stored.includes(secret), false, `credential leaked into ${entry}`)
    }
  }
})

test('TEST-DSH-08B forged model arguments and unknown tool names execute nothing', { timeout: 20_000 }, async t => {
  const { host, gateway, directory, sessionId } = await setup(t)
  await begin(host, gateway, sessionId, 'run-1')
  for (const field of ['actor_id', 'session_id', 'run_id', 'profile', 'tool_call_id', 'idempotency_key',
    'arguments_hash', 'approval_receipt', 'run_capability', '_meta']) {
    await prompt(host, sessionId, `forge-field:${field}`)
    assert.equal(lastToolResult(host).status, 'failed')
  }
  await prompt(host, sessionId, 'unknown-tool')
  assert.equal(lastToolResult(host).status, 'failed')
  assert.equal(gateway.attempts.length, 0)
  assert.equal(gateway.executions.length, 0)
  await noGenericCalls(directory)
})

test('TEST-DSH-08C missing and revoked Run context fail closed in the DSH dispatch path', { timeout: 20_000 }, async t => {
  const { host, gateway, directory, sessionId } = await setup(t)
  await prompt(host, sessionId, 'query without a run')
  assert.equal(lastToolResult(host).status, 'failed')
  assert.equal(gateway.attempts.length, 0)
  const run = await begin(host, gateway, sessionId, 'run-1')
  gateway.revoke(run.capability)
  await prompt(host, sessionId, 'query with revoked capability')
  assert.equal(lastToolResult(host).status, 'failed')
  assert.equal(gateway.executions.length, 0)
  await noGenericCalls(directory)
})

for (const approved of [true, false]) {
  test(`TEST-DSH-08D DSH approval ${approved ? 'allow' : 'reject'} keeps trusted identity outside model input`, { timeout: 20_000 }, async t => {
    let receipt
    const { host, gateway, directory, sessionId } = await setup(t, async (event, host, gateway) => {
      if (event.event !== 'approval_required') return
      if (approved) receipt = gateway.approve(event.call)
      await host.control('approvalDecision', { toolCallId: event.call.tool_call_id, receipt })
    })
    await begin(host, gateway, sessionId, 'run-1')
    assert.equal((await prompt(host, sessionId, 'gateway-approval')).stopReason, 'end_turn')
    assert.equal(lastToolResult(host).status, approved ? 'completed' : 'failed')
    assert.equal(gateway.executions.length, approved ? 1 : 0)
    assert.equal(gateway.attempts.length, approved ? 2 : 1)
    assert.equal(new Set(gateway.attempts.map(item => item.headers['x-q4d-tool-call-id'])).size, 1)
    assert.equal(new Set(gateway.attempts.map(item => item.headers['idempotency-key'])).size, 1)
    if (approved) {
      assert.equal(gateway.attempts[0].headers['x-q4d-approval-receipt'], undefined)
      assert.equal(gateway.attempts[1].headers['x-q4d-approval-receipt'], receipt)
      assert.equal((await readFile(join(directory, 'model-calls.jsonl'), 'utf8')).includes(receipt), false)
    }
    await noGenericCalls(directory)
  })
}

test('TEST-DSH-08E cancelled DSH Tool aborts the downstream HTTP request', { timeout: 20_000 }, async t => {
  const { host, gateway, directory, sessionId } = await setup(t)
  await begin(host, gateway, sessionId, 'run-1')
  const pending = prompt(host, sessionId, 'gateway-wait')
  const deadline = Date.now() + 5000
  while (gateway.attempts.length === 0 && Date.now() < deadline) await delay(5)
  assert.equal(gateway.attempts.length, 1, 'cancel only after the downstream HTTP request actually starts')
  await host.cancel(sessionId)
  assert.equal((await pending).stopReason, 'cancelled')
  assert.equal(gateway.executions.length, 0)
  assert.equal(gateway.attempts.length, 1, 'cancellation must not retry the HTTP call')
  await noGenericCalls(directory)
})
