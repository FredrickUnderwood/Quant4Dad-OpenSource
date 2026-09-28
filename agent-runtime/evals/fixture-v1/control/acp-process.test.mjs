import assert from 'node:assert/strict'
import { readFile, readdir, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { methods } from '@agentclientprotocol/sdk'
import { launch, fixtureDirectory, mcpServers, prompt } from '../helpers/acp-host.mjs'

async function callCount(directory) {
  try { return (await readFile(join(directory, 'calls.jsonl'), 'utf8')).trim().split('\n').length }
  catch (error) { if (error.code === 'ENOENT') return 0; throw error }
}
async function lifecycle(directory) {
  return (await readFile(join(directory, 'lifecycle.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse)
}

async function modelCalls(directory) {
  return (await readFile(join(directory, 'model-calls.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse)
}

async function assertResumedHistory(host, sessionId, directory) {
  const previousCalls = (await modelCalls(directory)).length
  assert.deepEqual(await prompt(host, sessionId, 'query after restart'), { stopReason: 'end_turn' })
  const request = (await modelCalls(directory))[previousCalls]
  assert.ok(request, 'resumed prompt must reach the model')
  const lastUserIndex = request.messages.findLastIndex(message => message.source.kind === 'user')
  assert.deepEqual(request.messages[lastUserIndex]?.content, [{ type: 'text', text: 'query after restart' }])
  const history = request.messages.slice(0, lastUserIndex)
    .filter(message => ['user', 'model', 'tool'].includes(message.source.kind))
    .map(message => ({ source: message.source.kind, content: message.content }))
  assert.deepEqual(history, [
    { source: 'user', content: [{ type: 'text', text: 'query before restart' }] },
    { source: 'model', content: [{ type: 'tool-call', id: 'q4d-query',
      name: 'mcp__q4d__query_kline', arguments: '{"symbol":"000001","limit":2}' }] },
    { source: 'tool', content: [{ type: 'tool-result', toolCallId: 'q4d-query', isError: false,
      content: [{ type: 'text', text: '{"symbol":"000001","bars":[{"date":"2026-09-04","close":100}]}' }] }] },
    { source: 'model', content: [{ type: 'text', text: 'Q4D_T00_OK' }] },
  ], 'resumed model input must preserve the prior user, tool call/result and assistant reply in order')
}

// Fault injection for this fixture's plaintext, pinned version-0 session format.
// Only truncate the test-owned session with the requested ID, after process exit.
async function discardSessionHistory(directory, sessionId) {
  const root = join(directory, 'sessions')
  for (const entry of await readdir(root, { recursive: true })) {
    if (!entry.endsWith('.jsonl')) continue
    const path = join(root, entry)
    const lines = (await readFile(path, 'utf8')).trim().split('\n')
    const header = JSON.parse(lines[0])
    if (header.type !== 'session' || header.id !== sessionId) continue
    assert.ok(lines.length > 1, 'fault injection requires persisted history')
    await writeFile(path, `${lines[0]}\n`)
    return
  }
  assert.fail('fault injection did not find the persisted session')
}

test('TEST-DSH-02/03/04/07: exact tools, committed text, close/restart/resume and active cancel', { timeout: 30_000 }, async t => {
  const directory = await fixtureDirectory(t)
  const first = await launch(t, directory)
  const created = await first.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  assert.deepEqual(await prompt(first, created.sessionId, 'query before restart'), { stopReason: 'end_turn' })
  assert.ok(first.updates.some(({ update }) => update.sessionUpdate === 'agent_message_chunk' && update.content.text === 'Q4D_T00_OK'))
  assert.ok(first.updates.some(({ update }) => update.sessionUpdate === 'tool_call_update' && update.status === 'completed'))
  assert.equal(await callCount(directory), 1)

  // Concurrent session cannot see the first session's MCP tool.
  const empty = await first.request(methods.agent.session.new, { cwd: directory, mcpServers: [] })
  assert.deepEqual(await prompt(first, empty.sessionId, 'no-tools'), { stopReason: 'end_turn' })
  assert.equal(await callCount(directory), 1)
  await first.request(methods.agent.session.close, { sessionId: empty.sessionId })
  await first.request(methods.agent.session.close, { sessionId: created.sessionId })
  await first.close()

  const second = await launch(t, directory)
  const listed = await second.request(methods.agent.session.list, { cwd: directory })
  assert.ok(listed.sessions.some(session => session.sessionId === created.sessionId))
  await second.request(methods.agent.session.resume, { sessionId: created.sessionId, cwd: directory, mcpServers: mcpServers(directory) })
  assert.equal(second.updates.length, 0, 'ACP resume provides no transcript replay')
  await assertResumedHistory(second, created.sessionId, directory)
  assert.equal(await callCount(directory), 2)
  const pending = prompt(second, created.sessionId, 'cancel after query')
  // Attach rejection handling immediately so a failing fixture cannot leak a rejection.
  const settled = pending.then(value => ({ value }), error => ({ error }))
  await second.waitUpdate(update => update.sessionUpdate === 'tool_call_update' && update.toolCallId === 'q4d-cancel-query')
  await second.cancel(created.sessionId)
  const result = await settled
  if (result.error) throw result.error
  assert.deepEqual(result.value, { stopReason: 'cancelled' })
  assert.equal(await callCount(directory), 3)
  await second.request(methods.agent.session.close, { sessionId: created.sessionId })
  await second.close()
})

test('TEST-DSH-03: resume verification rejects a session whose history was discarded', { timeout: 20_000 }, async t => {
  const directory = await fixtureDirectory(t)
  const first = await launch(t, directory)
  const { sessionId } = await first.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  assert.deepEqual(await prompt(first, sessionId, 'query before restart'), { stopReason: 'end_turn' })
  await first.request(methods.agent.session.close, { sessionId })
  await first.close()
  await discardSessionHistory(directory, sessionId)

  const second = await launch(t, directory)
  const listed = await second.request(methods.agent.session.list, { cwd: directory })
  assert.ok(listed.sessions.some(session => session.sessionId === sessionId))
  await second.request(methods.agent.session.resume, { sessionId, cwd: directory, mcpServers: mcpServers(directory) })
  await assert.rejects(assertResumedHistory(second, sessionId, directory), {
    code: 'ERR_ASSERTION', message: /resumed model input must preserve the prior user/,
  })
  await second.request(methods.agent.session.close, { sessionId })
  await second.close()
})

for (const outcome of ['allow-once', 'reject-once', 'cancelled']) {
  test(`TEST-DSH-05: one-shot permission ${outcome}`, { timeout: 20_000 }, async t => {
    const directory = await fixtureDirectory(t)
    const host = await launch(t, directory, () => ({ outcome: outcome === 'cancelled'
      ? { outcome: 'cancelled' } : { outcome: 'selected', optionId: outcome } }))
    const { sessionId } = await host.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
    assert.deepEqual(await prompt(host, sessionId, 'query'), { stopReason: 'end_turn' })
    assert.equal(host.permissions.length, 1)
    assert.equal(host.permissions[0].toolCall.toolCallId, 'q4d-query')
    assert.equal(await callCount(directory), outcome === 'allow-once' ? 1 : 0)
    const terminal = host.updates.find(({ update }) => update.sessionUpdate === 'tool_call_update')?.update
    assert.equal(terminal?.status, outcome === 'allow-once' ? 'completed' : 'failed')
    assert.deepEqual((await lifecycle(directory)).map(event => event.stage), outcome === 'allow-once'
      ? ['proposed', 'started', 'completed'] : ['proposed', 'failed'])
    assert.ok((await lifecycle(directory)).every(event => event.sessionId === sessionId && event.callId === 'q4d-query'))
    await host.request(methods.agent.session.close, { sessionId })
    await host.close()
  })
}

test('TEST-DSH-05: allow-once never becomes a durable grant', { timeout: 20_000 }, async t => {
  const directory = await fixtureDirectory(t)
  let asked = 0
  const host = await launch(t, directory, () => ({ outcome: {
    outcome: 'selected', optionId: ++asked === 1 ? 'allow-once' : 'reject-once',
  } }))
  const { sessionId } = await host.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  await prompt(host, sessionId, 'first query')
  await prompt(host, sessionId, 'second query')
  assert.equal(asked, 2)
  assert.equal(await callCount(directory), 1)
  assert.deepEqual((await lifecycle(directory)).map(event => event.stage), [
    'proposed', 'started', 'completed', 'proposed', 'failed',
  ])
  await host.request(methods.agent.session.close, { sessionId })
  await host.close()
})

test('TEST-DSH-05: MCP failure stays a failed tool result', { timeout: 20_000 }, async t => {
  const directory = await fixtureDirectory(t)
  const host = await launch(t, directory)
  const { sessionId } = await host.request(methods.agent.session.new, { cwd: directory, mcpServers: mcpServers(directory) })
  assert.deepEqual(await prompt(host, sessionId, 'failure'), { stopReason: 'end_turn' })
  assert.equal(await callCount(directory), 1)
  assert.ok(host.updates.some(({ update }) => update.sessionUpdate === 'tool_call_update' && update.status === 'failed'))
  await host.request(methods.agent.session.close, { sessionId })
  await host.close()
})
