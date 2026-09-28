import test from 'node:test'
import assert from 'node:assert/strict'
import { RunSupervisor } from '../../../../src/runtime/run-supervisor.mjs'
import { prepareBudgetedRequest, compactionOutputLimit, canRecoverOutput } from '../../../../src/plugins/q4d-run-policy.ts'

function leaseFor(t, budgets = {}) {
  const supervisor = new RunSupervisor({ currentStatus: () => ({ configuration_current: true, configuration_applied: true, revision: 'one' }) })
  t.after(() => supervisor.close())
  return supervisor.begin({ sessionId: 's', runId: 'r', expiresAt: Date.now() + 10000, authorize: () => true,
    claims: { envelope: { budgets: { max_turns: 12, max_tool_calls: 16, max_input_tokens: 2000000, max_output_tokens: 16384, ...budgets } } } }, () => {})
}
const request = { provider: 'fixture', model: 'fixture', messages: [], tools: [{ name: 'query_kline' }] }

test('large provider ceiling leaves tools enabled and reserves a final answer under the actual Run budget', t => {
  const lease = leaseFor(t)
  const { outgoing, finalizing } = prepareBudgetedRequest(request, lease, 204800)
  assert.equal(finalizing, false)
  assert.deepEqual(outgoing.tools, request.tools)
  assert.equal(outgoing.maxTokens, 12288)
  assert.equal(compactionOutputLimit(lease.snapshot(), 204800), 8192)
  lease.reserveModel(1000, outgoing.maxTokens).settle({ inputTokens: 1000, outputTokens: 12288 })
  const last = prepareBudgetedRequest(request, lease, 204800)
  assert.equal(last.finalizing, true)
  assert.equal(last.outgoing.maxTokens, 4096)
  assert.equal(last.outgoing.tools, undefined)
})
test('provider ceilings, explicit request caps, remaining calls and tool budgets all remain effective', t => {
  const lease = leaseFor(t, { max_turns: 2, max_tool_calls: 1 })
  assert.equal(prepareBudgetedRequest({ ...request, maxTokens: 512 }, lease, 204800).outgoing.maxTokens, 512)
  assert.equal(prepareBudgetedRequest(request, lease, 128).outgoing.maxTokens, 128)
  lease.reserveModel(100, 128).settle({ inputTokens: 100, outputTokens: 20 })
  assert.equal(prepareBudgetedRequest(request, lease, 128).finalizing, true)
  assert.equal(compactionOutputLimit(lease.snapshot(), 128), 0)
  const tools = leaseFor(t); for (let i = 0; i < 16; i++) tools.tool()
  assert.equal(prepareBudgetedRequest(request, tools, 204800).finalizing, true)
})
test('output recovery cannot consume the final reserve or grow beyond two attempts per Run', t => {
  const lease = leaseFor(t), execution = { lease, modelOutputLimit: 8192 }
  assert.equal(canRecoverOutput(execution, 8192), true)
  assert.equal(canRecoverOutput({ ...execution, outputRecoveries: 2 }, 8192), false)
  lease.reserveModel(100, 12288).settle({ inputTokens: 100, outputTokens: 12288 })
  assert.equal(canRecoverOutput(execution, 8192), false)
  assert.equal(compactionOutputLimit(lease.snapshot(), 8192), 0)
})
test('recovery expands a working allocation within provider and Run limits and clears no existing history', t => {
  const lease = leaseFor(t, { max_output_tokens: 65536 })
  const history = [{ role: 'tool', content: 'already saved strategy 42' }]
  const initial = prepareBudgetedRequest({ ...request, messages: history }, lease, 204800)
  assert.equal(initial.outgoing.maxTokens, 16384)
  const recovered = prepareBudgetedRequest({ ...request, messages: history }, lease, 204800, 32768)
  assert.equal(recovered.outgoing.maxTokens, 32768)
  assert.equal(recovered.outgoing.messages, history)
  assert.match(recovered.outgoing.system, /工具调用均未执行/)
})
