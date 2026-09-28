import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { readFile, readdir, rm } from 'node:fs/promises'
import { join } from 'node:path'
import test from 'node:test'
import { fixture, request, sse, bridgeToken } from '../helpers/durable-host.mjs'
import { gatewayFixture } from '../helpers/mcp-gateway.mjs'
import { sessionRequest, fixtureProfileRevision } from '../../../fixtures/session-config.mjs'

const headers = { authorization: `Bearer ${bridgeToken}`, 'content-type': 'application/json' }
async function json(response, status = 200) {
  const body = await response.json()
  assert.equal(response.status, status, JSON.stringify(body))
  return body
}
async function error(response, status, code) { assert.deepEqual(await json(response, status), { error: { code } }) }
async function snapshot(directory) {
  const entries = await readdir(directory, { recursive: true, withFileTypes: true })
  return Promise.all(entries.filter(entry => entry.isFile()).map(async entry => {
    const path = join(entry.parentPath, entry.name)
    return [path, await readFile(path, 'utf8')]
  }))
}
async function modelCalls(directory) {
  const raw = await readFile(join(directory, 'model-calls.jsonl'), 'utf8').catch(error => { if (error.code === 'ENOENT') return ''; throw error })
  return raw.trim() ? raw.trim().split('\n').map(JSON.parse) : []
}
async function setup(t) {
  const gateway = await gatewayFixture(t)
  const environment = await fixture(t)
  const launch = async (env = {}, onEvent) => {
    const host = await environment.launch({ env: {
      Q4D_T00_GATEWAY_URL: gateway.url, Q4D_T00_RUNTIME_TOKEN: gateway.runtimeToken,
      Q4D_T00_CAPABILITY_PUBLIC_KEY: gateway.publicKey, ...env,
    }, onEvent })
    host.post = (path, body) => fetch(host.url + path, { method: 'POST', headers, body: JSON.stringify(body) })
    host.create = (body = sessionRequest()) => host.post('/q4d/v1/sessions', body)
    host.unload = (id = 'session-1') => host.post(`/q4d/v1/sessions/${id}/close`, {})
    host.prompt = body => host.post(`/q4d/v1/sessions/${body.q4d_session_id}/prompts`, body)
    host.cancel = (runId = 'run-1') => host.post(`/q4d/v1/runs/${runId}/cancel`, {})
    host.run = (runId = 'run-1') => fetch(`${host.url}/q4d/v1/runs/${runId}`, { headers })
    return host
  }
  const issue = (text = 'query', runId = 'run-1', sessionId = 'session-1', tools = ['query_kline']) => {
    const input = request(text, runId)
    // Recompute the existing prompt fixture domain if the Session differs.
    input.q4d_session_id = sessionId
    input.request_hash = 'sha256:' + createHash('sha256').update(JSON.stringify({ session_id: sessionId, content: input.content })).digest('hex')
    const run = gateway.issueRun(runId, { sessionId, allowedTools: tools })
    return { ...input, run_capability: run.capability }
  }
  return { gateway, environment, launch, issue }
}

test('TEST-SESSION-HTTP-01 concurrent empty creation and repeated close survive restart without model activity', { timeout: 20_000 }, async t => {
  const { launch, environment, gateway } = await setup(t)
  const host = await launch()
  const responses = await Promise.all(Array.from({ length: 5 }, () => host.create().then(response => json(response))))
  for (const response of responses) assert.deepEqual(response, responses[0])
  assert.equal(responses[0].durable, true)
  assert.equal(responses[0].dsh_session_id, responses[0].session_id)
  assert.deepEqual((await json(await host.transcript())).items, [])
  const closed = await json(await host.unload())
  assert.equal(closed.loaded, false)
  assert.deepEqual(await json(await host.unload()), closed)
  assert.equal(await host.call('inspect', { sessionId: 'session-1' }), undefined)
  assert.deepEqual(await json(await host.create()), responses[0], 'create retry does not reopen a closed Session')
  assert.equal(await host.call('inspect', { sessionId: 'session-1' }), undefined)
  await host.close()
  const second = await launch()
  const before = await snapshot(environment.directory)
  assert.deepEqual(await json(await second.create()), responses[0])
  assert.deepEqual(await json(await second.unload()), closed)
  assert.deepEqual((await json(await second.transcript())).items, [])
  assert.deepEqual(await snapshot(environment.directory), before)
  assert.deepEqual(await modelCalls(environment.directory), [])
  assert.equal(gateway.attempts.length, 0)
})

test('TEST-SESSION-HTTP-02 configuration conflicts, unavailable identities and forged hashes never provision a second Session', { timeout: 20_000 }, async t => {
  const { launch, environment } = await setup(t)
  const host = await launch()
  const created = await json(await host.create())
  for (const override of [{ provider: 'unknown' }, { model: 'other' }, { profile: 'ops' },
    { model_config_revision: 'revision-2' }, { profile_revision: 'sha256:' + 'a'.repeat(64) }]) {
    await error(await host.create(sessionRequest('session-1', override)), 409, 'agent_session_conflict')
  }
  const before = await readFile(join(environment.directory, 'session-bindings.jsonl'), 'utf8')
  await error(await host.create({ ...sessionRequest('session-2'), provision_request_hash: 'sha256:' + '0'.repeat(64) }), 409, 'agent_session_conflict')
  await error(await host.create(sessionRequest('session-2', { model: 'not-in-catalog' })), 503, 'agent_configuration_unavailable')
  await error(await host.create(sessionRequest('session-2', { model_config_revision: 'stale' })), 409, 'agent_configuration_conflict')
  assert.equal(await readFile(join(environment.directory, 'session-bindings.jsonl'), 'utf8'), before)
  assert.deepEqual(await json(await host.create()), created)
  await error(await host.unload('missing'), 404, 'agent_session_not_found')
})

test('TEST-SESSION-HTTP-03 close flushes history; HTTP prompt resumes it in-process and after restart', { timeout: 20_000 }, async t => {
  const { launch, issue, environment, gateway } = await setup(t)
  const host = await launch()
  await json(await host.create())
  const first = issue('query preserved memory')
  const ack = await json(await host.prompt(first), 202)
  const events = await sse(await host.events('run-1'))
  const before = await json(await host.transcript())
  await json(await host.unload())
  assert.deepEqual((await json(await host.transcript())).items, before.items)
  assert.deepEqual(await json(await host.prompt(first), 202), ack)
  assert.deepEqual(await sse(await host.events('run-1')), events)
  assert.equal(gateway.executions.length, 1)
  await json(await host.prompt(issue('query second round', 'run-2')), 202)
  await sse(await host.events('run-2'))
  await json(await host.unload())
  await host.close()
  const second = await launch()
  await json(await second.prompt(issue('query after restart', 'run-3')), 202)
  const finalEvents = await sse(await second.events('run-3'))
  assert.equal(finalEvents.at(-1).type, 'run.completed')
  const actual = JSON.stringify((await modelCalls(environment.directory)).at(-1).messages)
  for (const expected of ['query preserved memory', 'query second round', 'query after restart', 'Q4D_T00_OK', 'close']) assert.ok(actual.includes(expected))
  assert.equal(gateway.executions.length, 3)
  assert.equal((await json(await second.transcript())).items.filter(item => item.role === 'user').length, 3)
})

for (const text of ['gateway-wait', 'gateway-approval']) {
  test(`TEST-SESSION-HTTP-04 close during ${text} refuses to cancel or unload active work`, { timeout: 20_000 }, async t => {
    const { launch, issue, gateway } = await setup(t)
    const waiting = Promise.withResolvers()
    const host = await launch({}, event => {
      if (event.event === (text === 'gateway-approval' ? 'approval_required' : 'tool_dispatch')) waiting.resolve()
    })
    await json(await host.create())
    await json(await host.prompt(issue(text)), 202)
    await waiting.promise
    const before = await json(await host.run())
    await error(await host.unload(), 409, 'agent_run_in_progress')
    assert.equal((await json(await host.run())).state, before.state)
    assert.equal(gateway.executions.length, 0)
    await json(await host.cancel())
    assert.equal((await sse(await host.events('run-1'))).at(-1).type, 'run.cancelled')
    assert.equal((await json(await host.unload())).loaded, false)
  })
}

for (const fault of ['after_session_intent', 'after_session_materialized', 'after_session_commit']) {
  test(`TEST-SESSION-HTTP-05 SIGKILL ${fault} retries one provisioning binding and durable header`, { timeout: 20_000 }, async t => {
    const { launch, environment, issue, gateway } = await setup(t)
    const host = await launch({ Q4D_T00_SESSION_FAULT: fault })
    await assert.rejects(host.create(), /fetch failed/)
    assert.equal((await host.exited)[1], 'SIGKILL')
    const second = await launch()
    if (fault !== 'after_session_commit') await error(await second.prompt(issue()), 409, 'agent_session_provisioning')
    const recovered = await json(await second.create())
    assert.deepEqual(await json(await second.create()), recovered)
    const records = (await readFile(join(environment.directory, 'session-bindings.jsonl'), 'utf8')).trim().split('\n').map(JSON.parse)
    assert.deepEqual(records.map(row => row.data.kind), ['intent', 'materialized'])
    const artifacts = await snapshot(join(environment.directory, 'sessions'))
    assert.equal(artifacts.filter(([name]) => name.endsWith('.jsonl')).length, 1)
    assert.deepEqual(await modelCalls(environment.directory), [])
    await json(await second.prompt(issue()), 202)
    assert.equal((await sse(await second.events('run-1'))).at(-1).type, 'run.completed')
    assert.equal(gateway.executions.length, 1)
  })
}

test('TEST-SESSION-HTTP-06 close response loss leaves durable history readable and permits safe retry', { timeout: 20_000 }, async t => {
  const { launch, issue } = await setup(t)
  const host = await launch({ Q4D_T00_SESSION_FAULT: 'after_session_close' })
  await json(await host.create())
  await json(await host.prompt(issue()), 202)
  const events = await sse(await host.events('run-1'))
  const before = await json(await host.transcript())
  await assert.rejects(host.unload(), /fetch failed/)
  assert.equal((await host.exited)[1], 'SIGKILL')
  const second = await launch()
  assert.equal((await json(await second.unload())).loaded, false)
  assert.deepEqual((await json(await second.transcript())).items, before.items)
  assert.deepEqual(await sse(await second.events('run-1')), events)
})

test('TEST-SESSION-HTTP-07 unbound storage is not adopted, and missing acknowledged storage is never recreated', { timeout: 20_000 }, async t => {
  const { launch, issue, environment } = await setup(t)
  const host = await launch()
  // Deliberately create an unbound upstream artifact to exercise orphan safety.
  await host.call('create', { sessionId: 'orphan' })
  await error(await host.create(sessionRequest('orphan')), 409, 'agent_session_unbound')
  await json(await host.create())
  await json(await host.unload())
  await host.close()
  const artifacts = await snapshot(join(environment.directory, 'sessions'))
  const file = artifacts.find(([, contents]) => JSON.parse(contents.split('\n')[0]).id === 'session-1')
  assert.ok(file, 'locate the owned Session by its stored header')
  await rm(file[0])
  const second = await launch()
  await error(await second.create(), 503, 'agent_session_storage_missing')
  await error(await second.prompt(issue()), 503, 'agent_session_storage_missing')
  await error(await second.unload(), 503, 'agent_session_storage_missing')
  assert.deepEqual(await snapshot(join(environment.directory, 'sessions')), artifacts.filter(([name]) => name !== file[0]))
})

test('TEST-SESSION-HTTP-08 a bound empty Tool profile stays empty after close/restart and rejects broader Capability', { timeout: 20_000 }, async t => {
  const { launch, issue, gateway, environment } = await setup(t)
  const host = await launch()
  await json(await host.create(sessionRequest('session-1', { profile: 'fixture_empty', profile_revision: fixtureProfileRevision('fixture_empty') })))
  const input = issue('no-tools', 'run-1', 'session-1', [])
  await json(await host.prompt(input), 202)
  const events = await sse(await host.events('run-1'))
  assert.equal(events.at(-1).type, 'run.completed')
  assert.equal(events.some(event => event.type.startsWith('tool.')), false)
  await json(await host.unload())
  await host.close()
  const second = await launch()
  const before = await modelCalls(environment.directory)
  await error(await second.prompt(issue('no-tools', 'run-2')), 403, 'agent_capability_rejected')
  assert.deepEqual(await modelCalls(environment.directory), before)
  assert.equal(await second.call('inspect', { sessionId: 'session-1' }), undefined)
  await json(await second.prompt(issue('no-tools', 'run-3', 'session-1', [])), 202)
  assert.equal((await sse(await second.events('run-3'))).at(-1).type, 'run.completed')
  assert.equal(gateway.attempts.length, 0)
})

test('TEST-SESSION-HTTP-09 creation provenance stays stable across runtime/config changes; removed models block resume', { timeout: 20_000 }, async t => {
  const { launch, issue, environment } = await setup(t)
  const host = await launch()
  const created = await json(await host.create())
  await host.close()
  const disabled = await launch({ Q4D_T00_DISABLE_MODEL: '1', Q4D_T00_ADAPTER_VERSION: 'new-version' })
  assert.deepEqual(await json(await disabled.create()), created)
  await error(await disabled.prompt(issue()), 503, 'agent_configuration_unavailable')
  assert.deepEqual(await modelCalls(environment.directory), [])
  await disabled.close()
  const upgraded = await launch({ Q4D_T00_MODEL_REVISION: 'fixture-v2', Q4D_T00_ADAPTER_VERSION: 'new-version' })
  assert.deepEqual(await json(await upgraded.create()), created)
  await json(await upgraded.prompt(issue()), 202)
  assert.equal((await sse(await upgraded.events('run-1'))).at(-1).type, 'run.completed')
})

test('TEST-SESSION-HTTP-10 interrupted Tool recovers through HTTP prompt without redispatch or create-side execution', { timeout: 20_000 }, async t => {
  const { launch, issue, environment, gateway } = await setup(t)
  const waiting = Promise.withResolvers()
  const host = await launch({}, event => { if (event.event === 'approval_required') waiting.resolve() })
  const created = await json(await host.create())
  const input = issue('gateway-approval')
  const accepted = await json(await host.prompt(input), 202)
  await waiting.promise
  const count = gateway.attempts.length
  await host.kill()
  const second = await launch()
  const callsBefore = await modelCalls(environment.directory)
  assert.deepEqual(await json(await second.create()), created)
  assert.deepEqual(await modelCalls(environment.directory), callsBefore)
  assert.equal((await json(await second.run())).state, 'recovering')
  assert.deepEqual(await json(await second.prompt(input), 202), accepted)
  const events = await sse(await second.events('run-1'))
  assert.equal(events.at(-1).type, 'run.interrupted')
  assert.equal(events.find(event => event.type === 'tool.failed').data.code, 'agent_tool_outcome_unknown')
  assert.equal(gateway.attempts.length, count)
  assert.deepEqual(await modelCalls(environment.directory), callsBefore)
  await json(await second.prompt(issue('query next', 'run-2')), 202)
  assert.equal((await sse(await second.events('run-2'))).at(-1).type, 'run.completed')
  assert.equal(gateway.executions.length, 1)
})
