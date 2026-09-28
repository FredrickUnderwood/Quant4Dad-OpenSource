import test from 'node:test'
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, readdir, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { bootstrapDshHost } from '../helpers/bootstrap-dsh-host.mjs'
import { host, clone, revise, send, controlToken } from '../contracts/bootstrap/helpers.mjs'
import { deriveDshConfiguration, keylessMarker } from '../../../src/bootstrap/derive.mjs'

async function modelServer(t) {
  const calls = [], arrival = Promise.withResolvers(), closed = Promise.withResolvers()
  let hold = false
  const server = createServer((req, res) => {
    let body = ''
    req.on('data', bytes => { body += bytes })
    req.on('end', () => {
      calls.push({ path: req.url, authorization: req.headers.authorization, apiKey: req.headers['x-api-key'], body: JSON.parse(body) })
      if (hold) { res.on('close', closed.resolve); arrival.resolve(); return }
      res.writeHead(200, { 'content-type': 'text/event-stream' })
      if (req.url === '/v1/messages') {
        const events = [
          { type: 'message_start', message: { id: 'msg_fixture', type: 'message', role: 'assistant', model: 'fixture-model',
            content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 3, output_tokens: 0 } } },
          { type: 'content_block_start', index: 0, content_block: { type: 'text', text: '' } },
          { type: 'content_block_delta', index: 0, delta: { type: 'text_delta', text: 'hello' } },
          { type: 'content_block_stop', index: 0 },
          { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 1 } },
          { type: 'message_stop' },
        ]
        for (const event of events) res.write(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
        res.end(); return
      }
      for (const value of [
        { choices: [{ delta: { role: 'assistant', content: 'hello' }, index: 0, finish_reason: null }] },
        { choices: [{ delta: {}, index: 0, finish_reason: 'stop' }], usage: { prompt_tokens: 3, completion_tokens: 1 } },
      ]) res.write('data: ' + JSON.stringify(value) + '\n\n')
      res.end('data: [DONE]\n\n')
    })
  })
  t.after(() => { server.closeAllConnections(); return new Promise(resolve => server.close(resolve)) })
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  return { url: `http://127.0.0.1:${server.address().port}/v1`, calls, arrival, closed, hold() { hold = true } }
}
async function setup(t, key = 'fixture-provider-key-one') {
  const model = await modelServer(t)
  let value = clone()
  value.providers[0].base_url = model.url; value.providers[0].api_key = key
  const source = await host(t, (req, res) => send(res, value, req.url.endsWith(value.revision) ? 304 : 200))
  const derived = deriveDshConfiguration(value)
  const ref = Object.keys(derived.credentials.refs)[0]
  const runtime = await bootstrapDshHost(t, { url: source.url, token: controlToken,
    env: { OPENAI_API_KEY: 'poison-ambient-provider-key', [ref]: 'poison-ambient-reference-key' } })
  return { model, source, runtime, get value() { return value }, set value(next) { value = next },
    request: () => runtime.call('model', { provider: 'fixture', model: 'fixture-model' }) }
}

test('TEST-BOOTSTRAP-DSH-01 actual public settings/credentials/pi-ai apply one generation and use explicit protocol/key/budget', { timeout: 20000 }, async t => {
  const { runtime, model, source, request, value } = await setup(t)
  const status = await runtime.call('refresh')
  assert.equal(status.configuration_applied, true)
  assert.equal(status.revision, value.revision)
  assert.deepEqual(await request(), { finish: 'stop', content: [{ type: 'text', text: 'hello' }] })
  assert.equal(model.calls[0].path, '/v1/chat/completions')
  assert.equal(model.calls[0].authorization, 'Bearer fixture-provider-key-one')
  assert.equal(model.calls[0].body.max_tokens ?? model.calls[0].body.max_completion_tokens, 512)
  const [generation] = await readdir(runtime.directory)
  assert.match(generation, /^generation-/)
  for (const [name, mode] of [['settings.json', 0o640], ['credentials.json', 0o600]]) {
    assert.equal((await stat(join(runtime.directory, generation, name))).mode & 0o777, mode)
  }
  const settings = await readFile(join(runtime.directory, generation, 'settings.json'), 'utf8')
  assert.ok(!settings.includes(value.providers[0].api_key))
  assert.ok(!settings.includes(value.mcp.runtime_token))
  await runtime.call('refresh')
  assert.equal(source.requests.length, 2)
  assert.deepEqual(await readdir(runtime.directory), [generation]) // 304 does not rebuild.
  await runtime.close()
  assert.deepEqual(await readdir(runtime.directory), [])
  for (const secret of [controlToken, value.providers[0].api_key, value.mcp.runtime_token, 'poison-ambient']) assert.ok(!runtime.logs().includes(secret))
})

test('TEST-BOOTSTRAP-DSH-02 rotation reaches the next real model request and empty replacement removes old files/routes', { timeout: 20000 }, async t => {
  const setupState = await setup(t), { runtime, model, request } = setupState
  await runtime.call('refresh'); await request()
  const changed = revise(structuredClone(setupState.value))
  changed.providers[0].api_key = 'fixture-provider-key-two'
  setupState.value = changed
  await runtime.call('refresh'); await request(); await runtime.call('drain')
  assert.deepEqual(model.calls.map(call => call.authorization), ['Bearer fixture-provider-key-one', 'Bearer fixture-provider-key-two'])
  assert.equal((await readdir(runtime.directory)).length, 1)
  setupState.value = { ...revise(structuredClone(changed), '4'), providers: [] }
  await runtime.call('refresh'); await runtime.call('drain')
  await assert.rejects(request(), { message: 'agent_capability_rejected' })
  assert.equal(model.calls.length, 2)
  const [generation] = await readdir(runtime.directory)
  assert.deepEqual(JSON.parse(await readFile(join(runtime.directory, generation, 'credentials.json'))).refs, {})
  assert.deepEqual(JSON.parse(await readFile(join(runtime.directory, generation, 'settings.json')))['llm-pi-ai'].providers, {})
})

test('TEST-BOOTSTRAP-DSH-03 keyless provider uses only the explicit marker and cannot discover ambient credentials', { timeout: 20000 }, async t => {
  const { runtime, model, request } = await setup(t, '')
  await runtime.call('refresh'); await request()
  assert.equal(model.calls[0].authorization, 'Bearer ' + keylessMarker)
  assert.ok(!JSON.stringify(model.calls).includes('poison-ambient'))
})

test('TEST-BOOTSTRAP-DSH-04 revoke aborts an active provider HTTP request and no next model call can dispatch', { timeout: 20000 }, async t => {
  const state = await setup(t), { runtime, model, request } = state
  await runtime.call('refresh')
  model.hold()
  const pending = assert.rejects(request(), { message: 'agent_configuration_unavailable' })
  await model.arrival.promise
  state.value = { ...revise(structuredClone(state.value)), providers: [] }
  await runtime.call('refresh')
  await pending; await model.closed.promise
  await assert.rejects(request(), { message: 'agent_capability_rejected' })
  assert.equal(model.calls.length, 1)
})

test('TEST-BOOTSTRAP-DSH-05 Anthropic route uses Messages protocol, explicit credential and output cap', { timeout: 20000 }, async t => {
  const state = await setup(t), { runtime, model, request } = state
  const provider = state.value.providers[0]
  provider.type = 'anthropic'; provider.agent.protocol = 'anthropic-messages'; provider.base_url = model.url.slice(0, -3)
  assert.equal((await runtime.call('refresh')).configuration_applied, true)
  assert.deepEqual(await request(), { finish: 'stop', content: [{ type: 'text', text: 'hello' }] })
  assert.equal(model.calls[0].path, '/v1/messages')
  assert.equal(model.calls[0].apiKey, provider.api_key)
  assert.equal(model.calls[0].body.max_tokens, 512)
})

test('TEST-BOOTSTRAP-DSH-06 application failure clears old routes/files and a later complete refresh recovers', { timeout: 20000 }, async t => {
  const state = await setup(t), { runtime, model, request } = state
  await runtime.call('refresh'); await request()
  state.value = revise(structuredClone(state.value))
  state.value.providers[0].agent.reasoning_effort = 'unsupported'
  await assert.rejects(runtime.call('refresh'), { message: 'agent_bootstrap_apply_failed' })
  await runtime.call('drain')
  assert.equal((await runtime.call('status')).configuration_applied, false)
  assert.deepEqual(await readdir(runtime.directory), [])
  await assert.rejects(request(), { message: 'agent_configuration_unavailable' })
  assert.equal(model.calls.length, 1)
  state.value = revise(structuredClone(state.value), '4')
  delete state.value.providers[0].agent.reasoning_effort
  state.value.providers[0].api_key = 'recovered-fixture-key'
  await runtime.call('refresh'); await request()
  assert.equal(model.calls[1].authorization, 'Bearer recovered-fixture-key')
})
