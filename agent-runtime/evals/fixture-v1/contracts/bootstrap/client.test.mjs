import test from 'node:test'
import assert from 'node:assert/strict'
import { inspect } from 'node:util'
import { BootstrapClient } from '../../../../src/bootstrap/client.mjs'
import { maxBootstrapBytes } from '../../../../src/bootstrap/validation.mjs'
import { host, send, clone, fixture, headers, controlToken } from './helpers.mjs'

test('TEST-BOOTSTRAP-CLIENT-01 fixed GET sends only service authority and binds 200/304 ETags', async t => {
  const server = await host(t, (req, res) => send(res, fixture, req.url.includes('?') ? 304 : 200))
  const first = await server.client.read()
  assert.deepEqual(first, { unchanged: false, snapshot: fixture })
  const second = await server.client.read(fixture.revision)
  assert.deepEqual(second, { unchanged: true, revision: fixture.revision })
  for (const request of server.requests) {
    assert.equal(request.method, 'GET')
    assert.equal(request.headers.authorization, `Bearer ${controlToken}`)
    for (const name of ['cookie', 'content-length', 'transfer-encoding', 'if-none-match']) assert.equal(request.headers[name], undefined)
  }
  assert.equal(server.requests[0].url, '/internal/v1/agent/bootstrap')
  assert.equal(server.requests[1].url, '/internal/v1/agent/bootstrap?revision=' + fixture.revision)
})

test('TEST-BOOTSTRAP-CLIENT-02 invalid configuration and known revisions never reach HTTP or expose secrets', async t => {
  const server = await host(t)
  for (const edit of [{ url: server.url + '?token=' + controlToken }, { url: server.url + '#' },
    { url: server.url.replace('/internal/', '/x/../internal/') }, { controlToken: 'short' },
    { controlToken: controlToken + '\n' }, { timeoutMs: 0 }, { timeoutMs: 5001 }]) {
    assert.throws(() => new BootstrapClient({ url: server.url, controlToken, ...edit }), { message: 'agent_bootstrap_configuration_invalid' })
  }
  for (const known of [null, 'invalid', fixture.revision + '\n']) {
    await assert.rejects(server.client.read(known), { message: 'agent_bootstrap_input_invalid' })
  }
  await assert.rejects(server.client.read('', { signal: {} }), { message: 'agent_bootstrap_input_invalid' })
  assert.throws(() => new BootstrapClient(), { message: 'agent_bootstrap_configuration_invalid' })
  assert.equal(server.requests.length, 0)
  for (const text of [JSON.stringify(server.client), inspect(server.client), inspect(server.client, { showHidden: true })]) {
    assert.ok(!text.includes(controlToken)); assert.ok(!text.includes(server.url))
  }
})

test('TEST-BOOTSTRAP-CLIENT-03 redirects and errors do not forward authority or echo response diagnostics', async t => {
  const destination = await host(t)
  let code = 302
  const server = await host(t, (_req, res) => {
    res.writeHead(code, { location: destination.url })
    res.end('secret diagnostic ' + controlToken)
  })
  for (code of [301, 302, 303, 307, 308, 401, 403, 404, 500, 503]) {
    await assert.rejects(server.client.read(), { message: [401, 403].includes(code) ? 'agent_bootstrap_unauthorized' : 'agent_bootstrap_unavailable' })
  }
  assert.equal(destination.requests.length, 0)
})

test('TEST-BOOTSTRAP-CLIENT-04 headers, content encoding, validators and unsolicited 304 are strict', async t => {
  const server = await host(t)
  const variants = [
    { 'cache-control': 'public' }, { 'cache-control': undefined }, { 'x-content-type-options': undefined },
    { 'content-type': 'text/plain' }, { 'content-encoding': 'gzip' }, { etag: 'W/"' + fixture.revision + '"' },
    { etag: '"' + '0'.repeat(65) + '"' }, { etag: undefined }, { 'set-cookie': 'secret=value' },
    { etag: [headers(fixture).etag, headers(fixture).etag] },
  ]
  for (const edit of variants) {
    server.setHandler((_req, res) => {
      const merged = { ...headers(fixture), ...edit }
      for (const name of Object.keys(merged)) if (merged[name] === undefined) delete merged[name]
      res.writeHead(200, merged); res.end(JSON.stringify(fixture))
    })
    await assert.rejects(server.client.read(), { message: 'agent_bootstrap_invalid' })
  }
  server.setHandler((_req, res) => send(res, fixture, 304))
  await assert.rejects(server.client.read(), { message: 'agent_bootstrap_invalid' })
  await assert.rejects(server.client.read('0'.repeat(32) + '.' + '0'.repeat(32)), { message: 'agent_bootstrap_invalid' })
  server.setHandler((_req, res) => { res.writeHead(304, { ...headers(fixture), 'content-length': '5' }); res.end() })
  await assert.rejects(server.client.read(fixture.revision), { message: 'agent_bootstrap_invalid' })
})

test('TEST-BOOTSTRAP-CLIENT-05 fixed, chunked and incomplete responses are bounded before publication', async t => {
  const server = await host(t)
  for (const fixed of [true, false]) {
    server.setHandler((_req, res) => {
      res.writeHead(200, { ...headers(fixture), ...(fixed ? { 'content-length': maxBootstrapBytes + 1 } : {}) })
      res.end(Buffer.alloc(maxBootstrapBytes + 1, 32))
    })
    await assert.rejects(server.client.read(), { message: 'agent_bootstrap_invalid' })
  }
  server.setHandler((_req, res) => {
    res.writeHead(200, { ...headers(fixture), 'content-length': '10000' })
    res.write('{'); setImmediate(() => res.destroy())
  })
  await assert.rejects(server.client.read(), { message: 'agent_bootstrap_unavailable' })
})

test('TEST-BOOTSTRAP-CLIENT-06 total deadline includes missing headers and a continuously arriving body', async t => {
  const server = await host(t, () => {}, { timeoutMs: 80 })
  await assert.rejects(server.client.read(), { message: 'agent_bootstrap_timeout' })
  server.setHandler((_req, res) => {
    res.writeHead(200, headers(fixture)); res.write('{')
    const timer = setInterval(() => res.write(' '), 5)
    res.on('close', () => clearInterval(timer))
  })
  await assert.rejects(server.client.read(), { message: 'agent_bootstrap_timeout' })
})

test('TEST-BOOTSTRAP-CLIENT-07 cancellation tears down active requests and pre-abort performs no request', async t => {
  const arrived = Promise.withResolvers()
  const closed = Promise.withResolvers()
  const server = await host(t, (req) => { req.on('close', closed.resolve); arrived.resolve() })
  const controller = new AbortController()
  const pending = assert.rejects(server.client.read('', { signal: controller.signal }), { message: 'agent_bootstrap_cancelled' })
  await arrived.promise
  controller.abort(new Error('secret cancellation reason'))
  await pending
  await closed.promise
  await assert.rejects(server.client.read('', { signal: controller.signal }), { message: 'agent_bootstrap_cancelled' })
  assert.equal(server.requests.length, 1)
})

test('TEST-BOOTSTRAP-CLIENT-08 malformed wire and token reuse fail without returning secret partial snapshots', async t => {
  const server = await host(t)
  const reused = clone(); reused.mcp.runtime_token = controlToken
  for (const body of ['secret invalid JSON', Buffer.from([0xff]), JSON.stringify(reused),
    JSON.stringify(fixture).replace('"api_key":', '"api_key":"secret","api_key":')]) {
    server.setHandler((_req, res) => { res.writeHead(200, headers(fixture)); res.end(body) })
    await assert.rejects(server.client.read(), error => {
      assert.equal(error.message, 'agent_bootstrap_invalid')
      assert.ok(!inspect(error).includes(controlToken)); assert.equal(error.cause, undefined)
      return true
    })
  }
})
